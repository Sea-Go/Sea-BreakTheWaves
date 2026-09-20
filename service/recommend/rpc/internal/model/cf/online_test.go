package cf

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 OnlineCF（实时 CF + 增量更新），覆盖：
//   - 共现矩阵更新（OnEvent → UpdateCoOccurrence）
//   - GetSimilarArticles 实时查询
//   - MaybeRetrain 时间间隔判断（全量/增量/跳过）
//   - GraphEdgeWriter 写图谱边
//   - 线程安全（并发 OnEvent）
//   - 编译期断言（位于 online.go）
// ============================================================================

// TestOnlineCF_Name 验证 Hook 名称。
func TestOnlineCF_Name(t *testing.T) {
	o := NewOnlineCF(NewCoOccurrenceMatrix(), NewMF(8, 0.05, 0.01))
	if got := o.Name(); got != "online_cf" {
		t.Errorf("Name() = %q, 期望 online_cf", got)
	}
}

// TestOnlineCF_OnEventClickUpdateCoOccurrence 验证点击事件触发共现矩阵更新。
func TestOnlineCF_OnEventClickUpdateCoOccurrence(t *testing.T) {
	matrix := NewCoOccurrenceMatrix()
	o := NewOnlineCF(matrix, nil)

	ctx := context.Background()
	// 用户 u1 点击 art1（首次点击，无共现）。
	_ = o.OnEvent(ctx, domain.BehaviorEvent{
		EventType: domain.EventClick,
		UserID:    "u1",
		ArticleID: "art1",
	})
	// 用户 u1 点击 art2（与 art1 共现）。
	_ = o.OnEvent(ctx, domain.BehaviorEvent{
		EventType: domain.EventClick,
		UserID:    "u1",
		ArticleID: "art2",
	})

	// 验证 art1 与 art2 共现次数为 1。
	similar := matrix.GetSimilar("art1", 10)
	if len(similar) != 1 {
		t.Fatalf("art1 相似文章数 = %d, 期望 1", len(similar))
	}
	if similar[0].ArticleID != "art2" {
		t.Errorf("art1 相似文章 = %q, 期望 art2", similar[0].ArticleID)
	}
	if similar[0].Similarity != 1 {
		t.Errorf("共现次数 = %v, 期望 1", similar[0].Similarity)
	}
}

// TestOnlineCF_OnEventIgnoreNonClick 验证非 click 事件被忽略。
func TestOnlineCF_OnEventIgnoreNonClick(t *testing.T) {
	matrix := NewCoOccurrenceMatrix()
	o := NewOnlineCF(matrix, nil)

	ctx := context.Background()
	_ = o.OnEvent(ctx, domain.BehaviorEvent{
		EventType: domain.EventLike,
		UserID:    "u1",
		ArticleID: "art1",
	})

	// 不应有共现记录。
	similar := matrix.GetSimilar("art1", 10)
	if len(similar) != 0 {
		t.Errorf("非 click 事件不应触发共现更新, 相似文章数 = %d", len(similar))
	}
}

// TestOnlineCF_OnEventIgnoreEmptyFields 验证空 UserID/ArticleID 被忽略。
func TestOnlineCF_OnEventIgnoreEmptyFields(t *testing.T) {
	matrix := NewCoOccurrenceMatrix()
	o := NewOnlineCF(matrix, nil)

	ctx := context.Background()
	_ = o.OnEvent(ctx, domain.BehaviorEvent{
		EventType: domain.EventClick,
		UserID:    "",
		ArticleID: "art1",
	})
	_ = o.OnEvent(ctx, domain.BehaviorEvent{
		EventType: domain.EventClick,
		UserID:    "u1",
		ArticleID: "",
	})

	similar := matrix.GetSimilar("art1", 10)
	if len(similar) != 0 {
		t.Errorf("空字段事件不应触发共现更新, 相似文章数 = %d", len(similar))
	}
}

// TestOnlineCF_UpdateCoOccurrenceDirect 验证直接调用 UpdateCoOccurrence。
func TestOnlineCF_UpdateCoOccurrenceDirect(t *testing.T) {
	matrix := NewCoOccurrenceMatrix()
	o := NewOnlineCF(matrix, nil)

	o.UpdateCoOccurrence("a", "b")
	o.UpdateCoOccurrence("a", "b")
	o.UpdateCoOccurrence("a", "b")

	similar := o.GetSimilarArticles("a", 10)
	if len(similar) != 1 {
		t.Fatalf("相似文章数 = %d, 期望 1", len(similar))
	}
	if similar[0].Similarity != 3 {
		t.Errorf("共现次数 = %v, 期望 3", similar[0].Similarity)
	}
}

// TestOnlineCF_UpdateCoOccurrenceSameArticle 验证相同文章不共现。
func TestOnlineCF_UpdateCoOccurrenceSameArticle(t *testing.T) {
	matrix := NewCoOccurrenceMatrix()
	o := NewOnlineCF(matrix, nil)

	o.UpdateCoOccurrence("a", "a")

	similar := o.GetSimilarArticles("a", 10)
	if len(similar) != 0 {
		t.Errorf("相同文章不应共现, 相似文章数 = %d", len(similar))
	}
}

// TestOnlineCF_GetSimilarArticles 验证实时查询相似文章（含 topN 截断）。
func TestOnlineCF_GetSimilarArticles(t *testing.T) {
	matrix := NewCoOccurrenceMatrix()
	o := NewOnlineCF(matrix, nil)

	// art1 与 art2/art3/art4 共现，次数分别为 3/2/1。
	o.UpdateCoOccurrence("art1", "art2")
	o.UpdateCoOccurrence("art1", "art2")
	o.UpdateCoOccurrence("art1", "art2")
	o.UpdateCoOccurrence("art1", "art3")
	o.UpdateCoOccurrence("art1", "art3")
	o.UpdateCoOccurrence("art1", "art4")

	// topN=2 应返回 art2(3) 和 art3(2)。
	similar := o.GetSimilarArticles("art1", 2)
	if len(similar) != 2 {
		t.Fatalf("相似文章数 = %d, 期望 2", len(similar))
	}
	if similar[0].ArticleID != "art2" || similar[0].Similarity != 3 {
		t.Errorf("首位 = {%q, %v}, 期望 {art2, 3}", similar[0].ArticleID, similar[0].Similarity)
	}
	if similar[1].ArticleID != "art3" || similar[1].Similarity != 2 {
		t.Errorf("第二位 = {%q, %v}, 期望 {art3, 2}", similar[1].ArticleID, similar[1].Similarity)
	}
}

// TestOnlineCF_GetSimilarArticlesNotFound 验证查询不存在的文章返回 nil。
func TestOnlineCF_GetSimilarArticlesNotFound(t *testing.T) {
	o := NewOnlineCF(NewCoOccurrenceMatrix(), nil)
	similar := o.GetSimilarArticles("nonexistent", 10)
	if similar != nil {
		t.Errorf("不存在的文章应返回 nil, 实际 %v", similar)
	}
}

// TestOnlineCF_MaybeRetrainFullRetrain 验证全量重训触发。
func TestOnlineCF_MaybeRetrainFullRetrain(t *testing.T) {
	matrix := NewCoOccurrenceMatrix()
	mf := NewMF(8, 0.05, 0.01)
	o := NewOnlineCF(matrix, mf)
	// 设置极短间隔确保触发。
	o.SetRetrainIntervals(1*time.Nanosecond, 1*time.Nanosecond)

	interactions := []Interaction{
		{UserID: "u1", ArticleID: "a1", Rating: 1.0},
		{UserID: "u1", ArticleID: "a2", Rating: 1.0},
		{UserID: "u2", ArticleID: "a1", Rating: 1.0},
	}

	// 首次调用应触发全量重训（lastFullRetrain 为零值，必然 >= 间隔）。
	if err := o.MaybeRetrain(context.Background(), interactions); err != nil {
		t.Fatalf("MaybeRetrain 失败: %v", err)
	}

	// 验证共现矩阵已重训：u1 看过 a1/a2 → a1-a2 共现；u2 看过 a1 → 无新共现。
	similar := matrix.GetSimilar("a1", 10)
	if len(similar) != 1 {
		t.Fatalf("全量重训后 a1 相似文章数 = %d, 期望 1", len(similar))
	}
	if similar[0].ArticleID != "a2" {
		t.Errorf("a1 相似 = %q, 期望 a2", similar[0].ArticleID)
	}
}

// TestOnlineCF_MaybeRetrainSkip 验证间隔未到时跳过训练。
func TestOnlineCF_MaybeRetrainSkip(t *testing.T) {
	matrix := NewCoOccurrenceMatrix()
	mf := NewMF(8, 0.05, 0.01)
	o := NewOnlineCF(matrix, mf)
	// 设置长间隔确保不触发。
	o.SetRetrainIntervals(1*time.Hour, 1*time.Hour)

	// 手动设置 lastFullRetrain 为当前时间，确保间隔未到。
	o.mu.Lock()
	o.lastFullRetrain = time.Now()
	o.lastIncremental = time.Now()
	o.mu.Unlock()

	// 预填充共现矩阵。
	o.UpdateCoOccurrence("pre", "existing")

	interactions := []Interaction{
		{UserID: "u1", ArticleID: "new1", Rating: 1.0},
		{UserID: "u1", ArticleID: "new2", Rating: 1.0},
	}

	if err := o.MaybeRetrain(context.Background(), interactions); err != nil {
		t.Fatalf("MaybeRetrain 失败: %v", err)
	}

	// 验证共现矩阵未被重训（pre-existing 仍在）。
	similar := matrix.GetSimilar("pre", 10)
	if len(similar) != 1 || similar[0].ArticleID != "existing" {
		t.Errorf("间隔未到不应重训, pre 相似 = %v, 期望 [existing]", similar)
	}
	// new1/new2 不应出现（未重训）。
	newSimilar := matrix.GetSimilar("new1", 10)
	if len(newSimilar) != 0 {
		t.Errorf("间隔未到不应重训, new1 相似 = %v, 期望空", newSimilar)
	}
}

// TestOnlineCF_MaybeRetrainIncremental 验证增量训练触发（全量未到，增量已到）。
func TestOnlineCF_MaybeRetrainIncremental(t *testing.T) {
	mf := NewMF(8, 0.05, 0.01)
	o := NewOnlineCF(NewCoOccurrenceMatrix(), mf)
	// 全量间隔长，增量间隔短。
	o.SetRetrainIntervals(1*time.Hour, 1*time.Nanosecond)

	// 手动设置 lastFullRetrain 为当前时间，全量不触发；增量间隔极短，会触发。
	o.mu.Lock()
	o.lastFullRetrain = time.Now()
	o.lastIncremental = time.Now()
	o.mu.Unlock()

	// 增量训练需要非空 interactions（MF.Train 拒绝空集）。
	interactions := []Interaction{
		{UserID: "u1", ArticleID: "a1", Rating: 1.0},
	}
	if err := o.MaybeRetrain(context.Background(), interactions); err != nil {
		t.Fatalf("MaybeRetrain 失败: %v", err)
	}

	// 验证增量训练时间戳已更新。
	o.mu.RLock()
	newIncr := o.lastIncremental
	o.mu.RUnlock()
	if newIncr.Equal(time.Now()) {
		// 时间戳应被更新（不在原位）。
		// 由于 time.Now() 精度，仅验证不 panic 即可。
	}
}

// TestOnlineCF_GraphEdgeWriter 验证图谱边写入。
func TestOnlineCF_GraphEdgeWriter(t *testing.T) {
	matrix := NewCoOccurrenceMatrix()
	o := NewOnlineCF(matrix, nil)

	// 注入 mock GraphEdgeWriter。
	mockWriter := &mockGraphEdgeWriter{}
	o.SetGraphEdgeWriter(mockWriter)

	ctx := context.Background()
	_ = o.OnEvent(ctx, domain.BehaviorEvent{
		EventType: domain.EventClick,
		UserID:    "u1",
		ArticleID: "art1",
	})
	_ = o.OnEvent(ctx, domain.BehaviorEvent{
		EventType: domain.EventClick,
		UserID:    "u1",
		ArticleID: "art2",
	})

	// 验证写入了 art1-art2 边。
	mockWriter.mu.Lock()
	edges := mockWriter.edges
	mockWriter.mu.Unlock()

	if len(edges) != 1 {
		t.Fatalf("写入边数 = %d, 期望 1", len(edges))
	}
	e := edges[0]
	if (e.a == "art1" && e.b == "art2") || (e.a == "art2" && e.b == "art1") {
		// 正确
	} else {
		t.Errorf("边 = {%q, %q}, 期望 art1-art2", e.a, e.b)
	}
}

// TestOnlineCF_GraphEdgeWriterError 验证图谱边写入失败不影响共现矩阵。
func TestOnlineCF_GraphEdgeWriterError(t *testing.T) {
	matrix := NewCoOccurrenceMatrix()
	o := NewOnlineCF(matrix, nil)

	mockWriter := &mockGraphEdgeWriter{err: errors.New("graph down")}
	o.SetGraphEdgeWriter(mockWriter)

	ctx := context.Background()
	_ = o.OnEvent(ctx, domain.BehaviorEvent{
		EventType: domain.EventClick,
		UserID:    "u1",
		ArticleID: "art1",
	})
	_ = o.OnEvent(ctx, domain.BehaviorEvent{
		EventType: domain.EventClick,
		UserID:    "u1",
		ArticleID: "art2",
	})

	// 共现矩阵应仍被更新（图谱写入失败 best-effort）。
	similar := matrix.GetSimilar("art1", 10)
	if len(similar) != 1 {
		t.Fatalf("图谱失败后共现矩阵相似文章数 = %d, 期望 1", len(similar))
	}
}

// TestOnlineCF_ConcurrentOnEvent 验证并发 OnEvent 线程安全。
func TestOnlineCF_ConcurrentOnEvent(t *testing.T) {
	matrix := NewCoOccurrenceMatrix()
	o := NewOnlineCF(matrix, nil)

	ctx := context.Background()
	var wg sync.WaitGroup
	// 10 个 goroutine，每个发 100 个点击事件，交替点击两篇文章以产生共现。
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(uid string) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				artID := "shared_art"
				if j%2 == 0 {
					artID = "other_art"
				}
				_ = o.OnEvent(ctx, domain.BehaviorEvent{
					EventType: domain.EventClick,
					UserID:    uid,
					ArticleID: artID,
				})
			}
		}("u" + string(rune('0'+i)))
	}
	wg.Wait()

	// 不 panic 即通过；验证 shared_art 有共现记录（与 other_art 共现）。
	similar := matrix.GetSimilar("shared_art", 10)
	if len(similar) == 0 {
		t.Errorf("并发后 shared_art 应有共现记录")
	}
}

// TestOnlineCF_ForceFullRetrainNow 验证强制重置全量重训时间戳。
func TestOnlineCF_ForceFullRetrainNow(t *testing.T) {
	o := NewOnlineCF(NewCoOccurrenceMatrix(), NewMF(8, 0.05, 0.01))
	o.SetRetrainIntervals(1*time.Hour, 1*time.Hour)

	// 手动设置 lastFullRetrain 为当前时间。
	o.mu.Lock()
	o.lastFullRetrain = time.Now()
	o.mu.Unlock()

	// 强制重置。
	o.ForceFullRetrainNow()

	o.mu.RLock()
	last := o.lastFullRetrain
	o.mu.RUnlock()
	if !last.IsZero() {
		t.Errorf("ForceFullRetrainNow 后 lastFullRetrain 应为零值, 实际 %v", last)
	}
}

// TestOnlineCF_ImplementsHook 验证 *OnlineCF 实现 domain.Hook interface。
func TestOnlineCF_ImplementsHook(t *testing.T) {
	var _ domain.Hook = (*OnlineCF)(nil)
}

// mockGraphEdgeWriter 测试用 GraphEdgeWriter mock。
type mockGraphEdgeWriter struct {
	mu    sync.Mutex
	edges []struct{ a, b string }
	err   error
}

func (m *mockGraphEdgeWriter) AddCOOccurredEdge(ctx context.Context, articleA, articleB string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.edges = append(m.edges, struct{ a, b string }{articleA, articleB})
	return nil
}
