package search

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 HybridSearcher，覆盖：
//   - RRF 融合正确性（分数计算 / 去重 / source 标记）
//   - 三路并行检索 + 弹性降级（单路失败不阻断 / 全部失败返回错误）
//   - TopK 截断 / RerankField 二次打分 / 并行执行
//   - 空后端 / Name() / 编译期断言
// ============================================================================

// 编译期断言：*HybridSearcher 实现 domain.Searcher interface。
var _ domain.Searcher = (*HybridSearcher)(nil)

// stubVectorRepo 模拟 VectorRepo。
type stubVectorRepo struct {
	denseFn  func(ctx context.Context, query string, topK int, filter domain.SearchFilter) ([]domain.SearchHit, error)
	sparseFn func(ctx context.Context, query string, topK int, filter domain.SearchFilter) ([]domain.SearchHit, error)
	mu       sync.Mutex
	denseN   int
	sparseN  int
}

func (s *stubVectorRepo) SearchDense(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
	s.mu.Lock()
	s.denseN++
	s.mu.Unlock()
	if s.denseFn != nil {
		return s.denseFn(ctx, q, topK, f)
	}
	return nil, nil
}

func (s *stubVectorRepo) SearchSparse(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
	s.mu.Lock()
	s.sparseN++
	s.mu.Unlock()
	if s.sparseFn != nil {
		return s.sparseFn(ctx, q, topK, f)
	}
	return nil, nil
}

// stubBM25Repo 模拟 BM25Repo。
type stubBM25Repo struct {
	fn func(ctx context.Context, query string, topK int, filter domain.SearchFilter) ([]domain.SearchHit, error)
	n  int
	mu sync.Mutex
}

func (s *stubBM25Repo) Search(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	if s.fn != nil {
		return s.fn(ctx, q, topK, f)
	}
	return nil, nil
}

// TestRRFFuse_Basic 验证 RRF 融合分数、去重与 source 标记。
func TestRRFFuse_Basic(t *testing.T) {
	list0 := []domain.SearchHit{
		{ArticleID: "a1", Score: 0.9, Source: "vector"},
		{ArticleID: "a2", Score: 0.8, Source: "vector"},
	}
	list1 := []domain.SearchHit{
		{ArticleID: "a2", Score: 0.7, Source: "bm25"},
		{ArticleID: "a3", Score: 0.6, Source: "bm25"},
	}
	out := rrfFuse([][]domain.SearchHit{list0, list1}, []float64{1, 1}, 60)
	if len(out) != 3 {
		t.Fatalf("融合后命中数 = %d, 期望 3", len(out))
	}
	// 期望分数
	expA1 := 1.0 / 61
	expA2 := 1.0/62 + 1.0/61
	expA3 := 1.0 / 62
	// 顺序应为 a2 > a1 > a3
	if out[0].ArticleID != "a2" {
		t.Errorf("首位 = %q, 期望 a2", out[0].ArticleID)
	}
	if !floatEq(out[0].Score, expA2) {
		t.Errorf("a2 score = %v, 期望 %v", out[0].Score, expA2)
	}
	if out[1].ArticleID != "a1" {
		t.Errorf("次位 = %q, 期望 a1", out[1].ArticleID)
	}
	if !floatEq(out[1].Score, expA1) {
		t.Errorf("a1 score = %v, 期望 %v", out[1].Score, expA1)
	}
	if out[2].ArticleID != "a3" {
		t.Errorf("末位 = %q, 期望 a3", out[2].ArticleID)
	}
	if !floatEq(out[2].Score, expA3) {
		t.Errorf("a3 score = %v, 期望 %v", out[2].Score, expA3)
	}
	// a2 出现在两路 → source = "hybrid"
	if out[0].Source != "hybrid" {
		t.Errorf("a2 source = %q, 期望 hybrid", out[0].Source)
	}
	// a1 仅 vector → 保留 vector
	if out[1].Source != "vector" {
		t.Errorf("a1 source = %q, 期望 vector", out[1].Source)
	}
}

// TestRRFFuse_Weights 验证权重对融合分数的影响。
func TestRRFFuse_Weights(t *testing.T) {
	list0 := []domain.SearchHit{{ArticleID: "a1", Source: "vector"}}
	list1 := []domain.SearchHit{{ArticleID: "a1", Source: "bm25"}}
	// list0 权重 2，list1 权重 1
	out := rrfFuse([][]domain.SearchHit{list0, list1}, []float64{2, 1}, 60)
	if len(out) != 1 {
		t.Fatalf("命中数 = %d, 期望 1", len(out))
	}
	exp := 2.0/61 + 1.0/61
	if !floatEq(out[0].Score, exp) {
		t.Errorf("加权 score = %v, 期望 %v", out[0].Score, exp)
	}
}

// TestRRFFuse_EmptyArticleID 验证空 ArticleID 被跳过。
func TestRRFFuse_EmptyArticleID(t *testing.T) {
	list0 := []domain.SearchHit{
		{ArticleID: "", Source: "vector"},
		{ArticleID: "a1", Source: "vector"},
	}
	out := rrfFuse([][]domain.SearchHit{list0}, []float64{1}, 60)
	if len(out) != 1 {
		t.Fatalf("命中数 = %d, 期望 1（空 ID 跳过）", len(out))
	}
	if out[0].ArticleID != "a1" {
		t.Errorf("命中 = %q, 期望 a1", out[0].ArticleID)
	}
}

// TestRRFFuse_EmptyInput 验证空输入返回空。
func TestRRFFuse_EmptyInput(t *testing.T) {
	out := rrfFuse(nil, nil, 60)
	if len(out) != 0 {
		t.Errorf("空输入命中数 = %d, 期望 0", len(out))
	}
}

// TestHybridSearcher_Name 验证名称。
func TestHybridSearcher_Name(t *testing.T) {
	h := NewHybridSearcher(nil, nil)
	if got := h.Name(); got != "hybrid" {
		t.Errorf("Name() = %q, 期望 hybrid", got)
	}
}

// TestHybridSearcher_Search_EmptyBackends 验证无后端时返回空结果且无错误。
func TestHybridSearcher_Search_EmptyBackends(t *testing.T) {
	h := NewHybridSearcher(nil, nil)
	res, err := h.Search(context.Background(), domain.SearchQuery{Query: "q", TopK: 5})
	if err != nil {
		t.Fatalf("Search 返回错误: %v", err)
	}
	if len(res.Hits) != 0 {
		t.Errorf("命中数 = %d, 期望 0", len(res.Hits))
	}
}

// TestHybridSearcher_Search_FuseAndDedup 验证三路检索后融合去重。
func TestHybridSearcher_Search_FuseAndDedup(t *testing.T) {
	v := &stubVectorRepo{
		denseFn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return []domain.SearchHit{
				{ArticleID: "a1", Score: 0.9, Source: "vector"},
				{ArticleID: "a2", Score: 0.8, Source: "vector"},
			}, nil
		},
		sparseFn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return []domain.SearchHit{
				{ArticleID: "a2", Score: 0.7, Source: "sparse"},
			}, nil
		},
	}
	b := &stubBM25Repo{
		fn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return []domain.SearchHit{
				{ArticleID: "a3", Score: 0.6, Source: "bm25"},
			}, nil
		},
	}
	h := NewHybridSearcher(v, b)
	res, err := h.Search(context.Background(), domain.SearchQuery{Query: "q", TopK: 10})
	if err != nil {
		t.Fatalf("Search 返回错误: %v", err)
	}
	if len(res.Hits) != 3 {
		t.Fatalf("命中数 = %d, 期望 3（去重后）", len(res.Hits))
	}
	// a2 在 dense+sparse 两路 → source=hybrid，分数最高
	if res.Hits[0].ArticleID != "a2" {
		t.Errorf("首位 = %q, 期望 a2", res.Hits[0].ArticleID)
	}
	if res.Hits[0].Source != "hybrid" {
		t.Errorf("a2 source = %q, 期望 hybrid", res.Hits[0].Source)
	}
	// 验证降序
	for i := 1; i < len(res.Hits); i++ {
		if res.Hits[i].Score > res.Hits[i-1].Score {
			t.Errorf("未按分数降序: [%d]=%v > [%d]=%v", i-1, res.Hits[i-1].Score, i, res.Hits[i].Score)
		}
	}
}

// TestHybridSearcher_Search_TopK 验证截断到 TopK。
func TestHybridSearcher_Search_TopK(t *testing.T) {
	v := &stubVectorRepo{
		denseFn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return []domain.SearchHit{
				{ArticleID: "a1", Score: 0.9, Source: "vector"},
				{ArticleID: "a2", Score: 0.8, Source: "vector"},
				{ArticleID: "a3", Score: 0.7, Source: "vector"},
				{ArticleID: "a4", Score: 0.6, Source: "vector"},
			}, nil
		},
	}
	h := NewHybridSearcher(v, nil)
	res, err := h.Search(context.Background(), domain.SearchQuery{Query: "q", TopK: 2})
	if err != nil {
		t.Fatalf("Search 返回错误: %v", err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("命中数 = %d, 期望 2（截断）", len(res.Hits))
	}
	if res.Hits[0].ArticleID != "a1" {
		t.Errorf("首位 = %q, 期望 a1", res.Hits[0].ArticleID)
	}
}

// TestHybridSearcher_Search_RerankField 验证二次打分。
func TestHybridSearcher_Search_RerankField(t *testing.T) {
	v := &stubVectorRepo{
		denseFn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return []domain.SearchHit{
				{ArticleID: "a1", Score: 0.9, Source: "vector"},
				{ArticleID: "a2", Score: 0.8, Source: "vector"},
			}, nil
		},
	}
	// 对 a2 加 1.0 二次分，使其超过 a1
	h := NewHybridSearcher(v, nil, WithRerankField(func(hit domain.SearchHit) float64 {
		if hit.ArticleID == "a2" {
			return 1.0
		}
		return 0
	}))
	res, err := h.Search(context.Background(), domain.SearchQuery{Query: "q", TopK: 10})
	if err != nil {
		t.Fatalf("Search 返回错误: %v", err)
	}
	if res.Hits[0].ArticleID != "a2" {
		t.Errorf("二次打分后首位 = %q, 期望 a2", res.Hits[0].ArticleID)
	}
}

// TestHybridSearcher_Search_PartialFail 验证单路失败不阻断（弹性降级）。
func TestHybridSearcher_Search_PartialFail(t *testing.T) {
	v := &stubVectorRepo{
		denseFn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return nil, errors.New("dense down")
		},
		sparseFn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return []domain.SearchHit{{ArticleID: "a1", Score: 0.9, Source: "sparse"}}, nil
		},
	}
	b := &stubBM25Repo{
		fn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return []domain.SearchHit{{ArticleID: "a2", Score: 0.8, Source: "bm25"}}, nil
		},
	}
	h := NewHybridSearcher(v, b)
	res, err := h.Search(context.Background(), domain.SearchQuery{Query: "q", TopK: 10})
	if err != nil {
		t.Fatalf("单路失败不应返回错误: %v", err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("命中数 = %d, 期望 2（dense 失败但 sparse/bm25 正常）", len(res.Hits))
	}
}

// TestHybridSearcher_Search_AllFail 验证全部失败返回错误。
func TestHybridSearcher_Search_AllFail(t *testing.T) {
	v := &stubVectorRepo{
		denseFn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return nil, errors.New("dense down")
		},
		sparseFn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return nil, errors.New("sparse down")
		},
	}
	b := &stubBM25Repo{
		fn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return nil, errors.New("bm25 down")
		},
	}
	h := NewHybridSearcher(v, b)
	_, err := h.Search(context.Background(), domain.SearchQuery{Query: "q", TopK: 10})
	if err == nil {
		t.Fatalf("全部失败应返回错误")
	}
}

// TestHybridSearcher_Search_FilterPassed 验证元数据过滤透传给底层 repo。
func TestHybridSearcher_Search_FilterPassed(t *testing.T) {
	var gotFilter domain.SearchFilter
	v := &stubVectorRepo{
		denseFn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			gotFilter = f
			return nil, nil
		},
	}
	b := &stubBM25Repo{
		fn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return nil, nil
		},
	}
	h := NewHybridSearcher(v, b)
	want := domain.SearchFilter{Tags: []string{"tech"}, Channel: "ai"}
	_, _ = h.Search(context.Background(), domain.SearchQuery{Query: "q", TopK: 5, Filter: want})
	if gotFilter.Channel != "ai" {
		t.Errorf("Filter 未透传: Channel = %q, 期望 ai", gotFilter.Channel)
	}
	if len(gotFilter.Tags) != 1 || gotFilter.Tags[0] != "tech" {
		t.Errorf("Filter 未透传: Tags = %v, 期望 [tech]", gotFilter.Tags)
	}
}

// TestHybridSearcher_Search_ParallelExecution 验证三路并行执行（耗时 ~ max 而非 sum）。
func TestHybridSearcher_Search_ParallelExecution(t *testing.T) {
	const delay = 100 * time.Millisecond
	slow := func() ([]domain.SearchHit, error) {
		time.Sleep(delay)
		return []domain.SearchHit{{ArticleID: "a1", Score: 0.5, Source: "x"}}, nil
	}
	v := &stubVectorRepo{
		denseFn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return slow()
		},
		sparseFn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return slow()
		},
	}
	b := &stubBM25Repo{
		fn: func(ctx context.Context, q string, topK int, f domain.SearchFilter) ([]domain.SearchHit, error) {
			return slow()
		},
	}
	h := NewHybridSearcher(v, b, WithPoolSize(8))
	start := time.Now()
	_, err := h.Search(context.Background(), domain.SearchQuery{Query: "q", TopK: 10})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Search 返回错误: %v", err)
	}
	// 并行执行总耗时应接近 1*delay；留 2x 容差
	if elapsed >= 2*delay {
		t.Errorf("并行耗时 = %v, 期望接近 %v", elapsed, delay)
	}
}

// TestJoinErrors 验证错误聚合。
func TestJoinErrors(t *testing.T) {
	if err := joinErrors([]error{nil, nil}); err != nil {
		t.Errorf("全 nil 应返回 nil, 实际 %v", err)
	}
	errs := []error{nil, errors.New("a"), errors.New("b")}
	joined := joinErrors(errs)
	if joined == nil {
		t.Fatalf("应返回聚合错误")
	}
	if !errors.Is(joined, errors.New("a")) {
		// errors.Join 后 errors.Is 仍可匹配原始错误
		if joined.Error() == "" {
			t.Errorf("聚合错误为空")
		}
	}
}

// floatEq 浮点近似比较。
func floatEq(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}
