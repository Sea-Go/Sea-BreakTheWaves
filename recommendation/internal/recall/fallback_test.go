package recall

import (
	"context"
	"errors"
	"testing"

	"sea/internal/domain"
)

// ============================================================================
// 该文件测试 Fallback 兜底策略，覆盖：
//   - 候选数 >= threshold 时直接返回
//   - 候选数 < threshold 时从频道默认池补足
//   - 频道默认池不足时从热门补足
//   - 按 ArticleID 去重
//   - 候选已达 topK 时不再补足
//   - 错误传播
// ============================================================================

// mockHotRepo 模拟 HotRepo。
type mockHotRepo struct {
	hot       []domain.Candidate
	err       error
	callCount int
	lastTopK  int
}

func (m *mockHotRepo) ListHot(ctx context.Context, topK int) ([]domain.Candidate, error) {
	m.callCount++
	m.lastTopK = topK
	if m.err != nil {
		return nil, m.err
	}
	// 返回全量，由 Fallback.Ensure 自身按 topK 截断（去重场景需返回多于 need 的项）
	return m.hot, nil
}

// mockChannelDefaultPool 模拟 ChannelDefaultPool。
type mockChannelDefaultPool struct {
	defaults     []domain.Candidate
	err          error
	callCount    int
	lastChannel  string
	lastTopK     int
}

func (m *mockChannelDefaultPool) GetDefault(ctx context.Context, channel string, topK int) ([]domain.Candidate, error) {
	m.callCount++
	m.lastChannel = channel
	m.lastTopK = topK
	if m.err != nil {
		return nil, m.err
	}
	// 返回全量，由 Fallback.Ensure 自身按 topK 截断（去重场景需返回多于 need 的项）
	return m.defaults, nil
}

// TestFallback_AboveThreshold 验证候选数 >= threshold 时直接返回。
func TestFallback_AboveThreshold(t *testing.T) {
	hot := &mockHotRepo{}
	pool := &mockChannelDefaultPool{}
	fb := NewFallback(hot, pool, 10)

	cands := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
		{ArticleID: "a3"},
		{ArticleID: "a4"},
		{ArticleID: "a5"},
		{ArticleID: "a6"},
		{ArticleID: "a7"},
		{ArticleID: "a8"},
		{ArticleID: "a9"},
		{ArticleID: "a10"},
	}
	res, err := fb.Ensure(context.Background(), cands, "tech", 20)
	if err != nil {
		t.Fatalf("Ensure 返回错误: %v", err)
	}
	if len(res) != 10 {
		t.Errorf("返回 %d 个, 期望 10（直接返回）", len(res))
	}
	if hot.callCount != 0 {
		t.Errorf("不应调用 HotRepo, 实际调用 %d 次", hot.callCount)
	}
	if pool.callCount != 0 {
		t.Errorf("不应调用 ChannelDefaultPool, 实际调用 %d 次", pool.callCount)
	}
}

// TestFallback_BelowThreshold_ChannelPool 验证从频道默认池补足。
func TestFallback_BelowThreshold_ChannelPool(t *testing.T) {
	hot := &mockHotRepo{}
	pool := &mockChannelDefaultPool{
		defaults: []domain.Candidate{
			{ArticleID: "d1"},
			{ArticleID: "d2"},
			{ArticleID: "d3"},
		},
	}
	fb := NewFallback(hot, pool, 10)

	cands := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
	}
	// threshold=10, 候选=2 < 10, topK=5, 需补 3 个
	res, err := fb.Ensure(context.Background(), cands, "tech", 5)
	if err != nil {
		t.Fatalf("Ensure 返回错误: %v", err)
	}
	if len(res) != 5 {
		t.Fatalf("返回 %d 个, 期望 5", len(res))
	}
	if pool.callCount != 1 {
		t.Errorf("ChannelDefaultPool 调用 %d 次, 期望 1", pool.callCount)
	}
	if pool.lastChannel != "tech" {
		t.Errorf("ChannelDefaultPool channel = %q, 期望 tech", pool.lastChannel)
	}
	if hot.callCount != 0 {
		t.Errorf("频道默认池已补足, 不应调用 HotRepo")
	}
}

// TestFallback_BelowThreshold_HotFallback 验证频道默认池不足时从热门补足。
func TestFallback_BelowThreshold_HotFallback(t *testing.T) {
	hot := &mockHotRepo{
		hot: []domain.Candidate{
			{ArticleID: "h1"},
			{ArticleID: "h2"},
			{ArticleID: "h3"},
		},
	}
	pool := &mockChannelDefaultPool{
		defaults: []domain.Candidate{
			{ArticleID: "d1"},
		},
	}
	fb := NewFallback(hot, pool, 10)

	cands := []domain.Candidate{
		{ArticleID: "a1"},
	}
	// threshold=10, 候选=1 < 10, topK=5
	// 频道默认池补 1 个 (d1), 还需 3 个从热门补
	res, err := fb.Ensure(context.Background(), cands, "tech", 5)
	if err != nil {
		t.Fatalf("Ensure 返回错误: %v", err)
	}
	if len(res) != 5 {
		t.Fatalf("返回 %d 个, 期望 5", len(res))
	}
	if pool.callCount != 1 {
		t.Errorf("ChannelDefaultPool 调用 %d 次, 期望 1", pool.callCount)
	}
	if hot.callCount != 1 {
		t.Errorf("HotRepo 调用 %d 次, 期望 1", hot.callCount)
	}
}

// TestFallback_Dedup 验证按 ArticleID 去重。
func TestFallback_Dedup(t *testing.T) {
	hot := &mockHotRepo{
		hot: []domain.Candidate{
			{ArticleID: "a1"}, // 与已有重复
			{ArticleID: "d1"}, // 与频道默认池重复
			{ArticleID: "h1"},
		},
	}
	pool := &mockChannelDefaultPool{
		defaults: []domain.Candidate{
			{ArticleID: "a2"}, // 与已有重复
			{ArticleID: "d1"},
		},
	}
	fb := NewFallback(hot, pool, 10)

	cands := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
	}
	// threshold=10, 候选=2 < 10, topK=5
	// 频道默认池: a2(重复跳过), d1(加入) → 3 个
	// 热门: a1(重复跳过), d1(重复跳过), h1(加入) → 4 个
	// 源耗尽, 最终 4 个
	res, err := fb.Ensure(context.Background(), cands, "tech", 5)
	if err != nil {
		t.Fatalf("Ensure 返回错误: %v", err)
	}
	// 验证无重复
	seen := make(map[string]struct{})
	for _, c := range res {
		if _, ok := seen[c.ArticleID]; ok {
			t.Errorf("结果含重复 ArticleID %q", c.ArticleID)
		}
		seen[c.ArticleID] = struct{}{}
	}
	if len(res) != 4 {
		t.Errorf("返回 %d 个, 期望 4（源耗尽后去重）", len(res))
	}
}

// TestFallback_AlreadyTopK 验证候选已达 topK 时不再补足（即使低于 threshold）。
func TestFallback_AlreadyTopK(t *testing.T) {
	hot := &mockHotRepo{}
	pool := &mockChannelDefaultPool{}
	fb := NewFallback(hot, pool, 20)

	cands := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
		{ArticleID: "a3"},
		{ArticleID: "a4"},
		{ArticleID: "a5"},
	}
	// threshold=20, 候选=5 < 20, 但 topK=5, 已达 topK
	res, err := fb.Ensure(context.Background(), cands, "tech", 5)
	if err != nil {
		t.Fatalf("Ensure 返回错误: %v", err)
	}
	if len(res) != 5 {
		t.Errorf("返回 %d 个, 期望 5（已达 topK）", len(res))
	}
	if hot.callCount != 0 || pool.callCount != 0 {
		t.Errorf("已达 topK 不应调用兜底源")
	}
}

// TestFallback_ErrorPropagation 验证错误传播。
func TestFallback_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("hot repo down")
	hot := &mockHotRepo{err: sentinel}
	pool := &mockChannelDefaultPool{
		defaults: []domain.Candidate{{ArticleID: "d1"}},
	}
	fb := NewFallback(hot, pool, 10)

	cands := []domain.Candidate{{ArticleID: "a1"}}
	// threshold=10, 候选=1 < 10, topK=5
	// 频道默认池补 1 个, 还需 3, 调用 HotRepo 返回错误
	_, err := fb.Ensure(context.Background(), cands, "tech", 5)
	if !errors.Is(err, sentinel) {
		t.Errorf("期望错误 %v 传播, 实际 %v", sentinel, err)
	}
}

// TestFallback_ChannelPoolError 验证频道默认池错误传播。
func TestFallback_ChannelPoolError(t *testing.T) {
	sentinel := errors.New("channel pool down")
	hot := &mockHotRepo{}
	pool := &mockChannelDefaultPool{err: sentinel}
	fb := NewFallback(hot, pool, 10)

	_, err := fb.Ensure(context.Background(), []domain.Candidate{{ArticleID: "a1"}}, "tech", 5)
	if !errors.Is(err, sentinel) {
		t.Errorf("期望错误 %v 传播, 实际 %v", sentinel, err)
	}
}

// TestFallback_SourcesExhausted 验证兜底源耗尽时返回所有可用候选。
func TestFallback_SourcesExhausted(t *testing.T) {
	hot := &mockHotRepo{
		hot: []domain.Candidate{{ArticleID: "h1"}},
	}
	pool := &mockChannelDefaultPool{
		defaults: []domain.Candidate{{ArticleID: "d1"}},
	}
	fb := NewFallback(hot, pool, 10)

	cands := []domain.Candidate{{ArticleID: "a1"}}
	// threshold=10, 候选=1 < 10, topK=100
	// 频道默认池补 1, 热门补 1, 源耗尽, 最终 3 个
	res, err := fb.Ensure(context.Background(), cands, "tech", 100)
	if err != nil {
		t.Fatalf("Ensure 返回错误: %v", err)
	}
	if len(res) != 3 {
		t.Errorf("返回 %d 个, 期望 3（源耗尽）", len(res))
	}
}
