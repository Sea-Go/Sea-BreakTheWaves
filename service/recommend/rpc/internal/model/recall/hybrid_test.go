package recall

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件使用 mock recallers 测试 HybridRecaller，覆盖：
//   - 并行召回：多个召回器被并行调用（验证并发执行）
//   - 去重：相同 ArticleID 的候选按分数 max 合并
//   - topK 截断：融合后按分数降序截断到 topK
//   - 分数降序：输出按 Score 降序
//   - 错误传播：任一召回器出错则整体返回错误
//   - 空召回器列表
//   - Name() 返回 "hybrid"
//   - 编译期断言 *HybridRecaller 实现 domain.Recaller
// ============================================================================

// 编译期断言：*HybridRecaller 实现 domain.Recaller interface。
var _ domain.Recaller = (*HybridRecaller)(nil)

// mockRecaller 模拟 domain.Recaller，记录调用并返回固定结果。
type mockRecaller struct {
	name  string
	fn    func(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error)
	calls int
	mu    sync.Mutex
}

func (m *mockRecaller) Recall(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	if m.fn != nil {
		return m.fn(ctx, req)
	}
	return domain.RecallResult{}, nil
}

func (m *mockRecaller) Name() string { return m.name }

// TestHybridRecaller_Name 验证召回器名称。
func TestHybridRecaller_Name(t *testing.T) {
	h, err := NewHybridRecaller(nil, 4, 10)
	if err != nil {
		t.Fatalf("NewHybridRecaller 返回错误: %v", err)
	}
	if got := h.Name(); got != "hybrid" {
		t.Errorf("Name() = %q, 期望 hybrid", got)
	}
}

// TestHybridRecaller_Recall_EmptyRecallers 验证空召回器列表返回空结果。
func TestHybridRecaller_Recall_EmptyRecallers(t *testing.T) {
	h, err := NewHybridRecaller(nil, 4, 10)
	if err != nil {
		t.Fatalf("NewHybridRecaller 返回错误: %v", err)
	}
	res, err := h.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if res.Source != "hybrid" {
		t.Errorf("Source = %q, 期望 hybrid", res.Source)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("候选数 = %d, 期望 0", len(res.Candidates))
	}
}

// TestHybridRecaller_Recall_ParallelAndDedup 验证并行召回 + 去重 + 分数降序。
func TestHybridRecaller_Recall_ParallelAndDedup(t *testing.T) {
	r1 := &mockRecaller{
		name: "r1",
		fn: func(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
			return domain.RecallResult{
				Candidates: []domain.Candidate{
					{ArticleID: "a1", Score: 0.9, Source: "rule"},
					{ArticleID: "a2", Score: 0.8, Source: "rule"},
					{ArticleID: "a3", Score: 0.7, Source: "rule"},
				},
				Source: "rule",
			}, nil
		},
	}
	r2 := &mockRecaller{
		name: "r2",
		fn: func(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
			return domain.RecallResult{
				Candidates: []domain.Candidate{
					{ArticleID: "a2", Score: 0.95, Source: "cf"}, // 与 r1 的 a2 重复, 分数更高
					{ArticleID: "a4", Score: 0.6, Source: "cf"},
				},
				Source: "cf",
			}, nil
		},
	}
	h, err := NewHybridRecaller([]domain.Recaller{r1, r2}, 4, 10)
	if err != nil {
		t.Fatalf("NewHybridRecaller 返回错误: %v", err)
	}
	res, err := h.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if res.Source != "hybrid" {
		t.Errorf("Source = %q, 期望 hybrid", res.Source)
	}
	// 去重后应 4 个候选（a1/a2/a3/a4）
	if len(res.Candidates) != 4 {
		t.Fatalf("候选数 = %d, 期望 4（去重后）", len(res.Candidates))
	}
	// a2 分数取 max(0.8, 0.95) = 0.95, 应排第一
	if res.Candidates[0].ArticleID != "a2" {
		t.Errorf("首个候选 = %q, 期望 a2", res.Candidates[0].ArticleID)
	}
	if res.Candidates[0].Score != 0.95 {
		t.Errorf("a2 Score = %v, 期望 0.95（取 max）", res.Candidates[0].Score)
	}
	// 验证降序
	for i := 1; i < len(res.Candidates); i++ {
		if res.Candidates[i].Score > res.Candidates[i-1].Score {
			t.Errorf("候选未按分数降序: [%d]=%v > [%d]=%v", i-1, res.Candidates[i-1].Score, i, res.Candidates[i].Score)
		}
	}
	// 两个召回器各被调用一次
	if r1.calls != 1 || r2.calls != 1 {
		t.Errorf("调用次数 r1=%d r2=%d, 期望各 1", r1.calls, r2.calls)
	}
}

// TestHybridRecaller_Recall_TopKTruncation 验证融合后截断到 topK。
func TestHybridRecaller_Recall_TopKTruncation(t *testing.T) {
	r1 := &mockRecaller{
		name: "r1",
		fn: func(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
			return domain.RecallResult{
				Candidates: []domain.Candidate{
					{ArticleID: "a1", Score: 0.9},
					{ArticleID: "a2", Score: 0.8},
					{ArticleID: "a3", Score: 0.7},
					{ArticleID: "a4", Score: 0.6},
					{ArticleID: "a5", Score: 0.5},
				},
			}, nil
		},
	}
	h, err := NewHybridRecaller([]domain.Recaller{r1}, 4, 10)
	if err != nil {
		t.Fatalf("NewHybridRecaller 返回错误: %v", err)
	}
	// req.TopK=3 覆盖 recaller 默认 10
	res, err := h.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		TopK:    3,
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Fatalf("候选数 = %d, 期望被截断到 3", len(res.Candidates))
	}
	// 验证保留分数最高的 3 个
	if res.Candidates[0].ArticleID != "a1" {
		t.Errorf("首个候选 = %q, 期望 a1", res.Candidates[0].ArticleID)
	}
	if res.Candidates[2].ArticleID != "a3" {
		t.Errorf("末个候选 = %q, 期望 a3", res.Candidates[2].ArticleID)
	}
}

// TestHybridRecaller_Recall_DefaultTopKFromRecaller 验证 req.TopK<=0 时使用 recaller 默认 topK。
func TestHybridRecaller_Recall_DefaultTopKFromRecaller(t *testing.T) {
	r1 := &mockRecaller{
		name: "r1",
		fn: func(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
			// 注意：使用不同 ArticleID，避免被 mergeAndDedup 去重
			cands := make([]domain.Candidate, 0, 8)
			for i := 0; i < 8; i++ {
				cands = append(cands, domain.Candidate{
					ArticleID: "a" + string(rune('1'+i)),
					Score:     0.5,
				})
			}
			return domain.RecallResult{Candidates: cands}, nil
		},
	}
	// recaller 默认 topK=2
	h, err := NewHybridRecaller([]domain.Recaller{r1}, 4, 2)
	if err != nil {
		t.Fatalf("NewHybridRecaller 返回错误: %v", err)
	}
	res, err := h.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		// TopK 为 0, 回退到 recaller 默认 2
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if len(res.Candidates) != 2 {
		t.Errorf("候选数 = %d, 期望回退到 recaller 默认 2", len(res.Candidates))
	}
}

// TestHybridRecaller_Recall_ErrorPropagation 验证任一召回器出错则整体返回错误。
func TestHybridRecaller_Recall_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("recall backend down")
	r1 := &mockRecaller{
		name: "r1",
		fn: func(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
			return domain.RecallResult{Candidates: []domain.Candidate{{ArticleID: "a1"}}}, nil
		},
	}
	r2 := &mockRecaller{
		name: "r2",
		fn: func(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
			return domain.RecallResult{}, sentinel
		},
	}
	h, err := NewHybridRecaller([]domain.Recaller{r1, r2}, 4, 10)
	if err != nil {
		t.Fatalf("NewHybridRecaller 返回错误: %v", err)
	}
	_, err = h.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("期望错误 %v 传播, 实际 %v", sentinel, err)
	}
}

// TestHybridRecaller_Recall_ParallelExecution 验证多个召回器被并行调用。
// 通过让每个召回器 sleep 一段时间, 若串行则总耗时 >= sum, 若并行则总耗时 ~ max。
func TestHybridRecaller_Recall_ParallelExecution(t *testing.T) {
	const delay = 100 * time.Millisecond
	mkSlowRecaller := func(name string) *mockRecaller {
		return &mockRecaller{
			name: name,
			fn: func(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
				time.Sleep(delay)
				return domain.RecallResult{
					Candidates: []domain.Candidate{{ArticleID: name, Score: 0.5}},
				}, nil
			},
		}
	}
	// 3 个召回器, poolSize=4（足够并行）
	recallers := []domain.Recaller{mkSlowRecaller("a1"), mkSlowRecaller("a2"), mkSlowRecaller("a3")}
	h, err := NewHybridRecaller(recallers, 4, 10)
	if err != nil {
		t.Fatalf("NewHybridRecaller 返回错误: %v", err)
	}
	start := time.Now()
	res, err := h.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Fatalf("候选数 = %d, 期望 3", len(res.Candidates))
	}
	// 并行执行总耗时应接近 1*delay 而非 3*delay；留 2x 容差应对调度抖动
	if elapsed >= 2*delay {
		t.Errorf("并行召回耗时 = %v, 期望接近 %v（串行应 ~%v）", elapsed, delay, 3*delay)
	}
}

// TestHybridRecaller_Recall_PoolConcurrencyLimit 验证 ants Pool 限制并发度。
// poolSize=1 时, 3 个召回器串行执行, 总耗时 >= 3*delay。
func TestHybridRecaller_Recall_PoolConcurrencyLimit(t *testing.T) {
	const delay = 80 * time.Millisecond
	mkSlowRecaller := func(name string) *mockRecaller {
		return &mockRecaller{
			name: name,
			fn: func(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
				time.Sleep(delay)
				return domain.RecallResult{
					Candidates: []domain.Candidate{{ArticleID: name, Score: 0.5}},
				}, nil
			},
		}
	}
	recallers := []domain.Recaller{mkSlowRecaller("a1"), mkSlowRecaller("a2"), mkSlowRecaller("a3")}
	// poolSize=1, 强制串行
	h, err := NewHybridRecaller(recallers, 1, 10)
	if err != nil {
		t.Fatalf("NewHybridRecaller 返回错误: %v", err)
	}
	start := time.Now()
	_, err = h.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	// 串行执行总耗时应 >= 3*delay
	if elapsed < 3*delay {
		t.Errorf("poolSize=1 时耗时 = %v, 期望 >= %v（串行）", elapsed, 3*delay)
	}
}

// TestMergeAndDedup 验证融合去重逻辑（单元测试 mergeAndDedup 函数）。
func TestMergeAndDedup(t *testing.T) {
	results := []domain.RecallResult{
		{
			Candidates: []domain.Candidate{
				{ArticleID: "a1", Score: 0.9, Source: "rule"},
				{ArticleID: "a2", Score: 0.5, Source: "rule"},
			},
		},
		{
			Candidates: []domain.Candidate{
				{ArticleID: "a2", Score: 0.8, Source: "cf"}, // 重复, 分数更高
				{ArticleID: "a3", Score: 0.7, Source: "cf"},
			},
		},
		{
			Candidates: nil, // 空结果应被跳过
		},
	}
	out := mergeAndDedup(results)
	if len(out) != 3 {
		t.Fatalf("去重后候选数 = %d, 期望 3", len(out))
	}
	// 验证 a2 取 max(0.5, 0.8) = 0.8
	var a2 *domain.Candidate
	for i := range out {
		if out[i].ArticleID == "a2" {
			a2 = &out[i]
			break
		}
	}
	if a2 == nil {
		t.Fatalf("未找到 a2")
	}
	if a2.Score != 0.8 {
		t.Errorf("a2 Score = %v, 期望 0.8（取 max）", a2.Score)
	}
	// 验证降序
	if out[0].Score < out[1].Score || out[1].Score < out[2].Score {
		t.Errorf("候选未按分数降序: %v %v %v", out[0].Score, out[1].Score, out[2].Score)
	}
}

// TestMergeAndDedup_EmptyArticleID 验证无 ArticleID 的候选不参与去重，全部保留。
func TestMergeAndDedup_EmptyArticleID(t *testing.T) {
	results := []domain.RecallResult{
		{
			Candidates: []domain.Candidate{
				{ArticleID: "", Score: 0.9, Source: "rule"},
				{ArticleID: "", Score: 0.8, Source: "rule"},
				{ArticleID: "a1", Score: 0.7, Source: "rule"},
			},
		},
	}
	out := mergeAndDedup(results)
	// 2 个无 ID 候选 + 1 个有 ID 候选 = 3
	if len(out) != 3 {
		t.Fatalf("候选数 = %d, 期望 3（无 ID 不去重）", len(out))
	}
}
