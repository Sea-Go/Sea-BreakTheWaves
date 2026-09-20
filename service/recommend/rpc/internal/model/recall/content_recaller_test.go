package recall

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件使用 mock VectorRepo/BM25Repo/Embedder 测试 ContentRecaller，覆盖：
//   - 双路成功 + RRF 融合分数正确性
//   - 单路失败降级（仅向量 / 仅 BM25）
//   - 双路失败返回错误
//   - topK 截断
//   - 无 Embedder 时降级到 BM25
//   - fuseRRF 单元测试（去重 / 排序 / 单列表）
//   - Name()
// ============================================================================

// mockVectorRepo 模拟 VectorRepo。
type mockVectorRepo struct {
	fn       func(ctx context.Context, query []float32, topK int) ([]domain.Candidate, error)
	calls    int
	lastVec  []float32
	lastTopK int
}

func (m *mockVectorRepo) Search(ctx context.Context, query []float32, topK int) ([]domain.Candidate, error) {
	m.calls++
	m.lastVec = query
	m.lastTopK = topK
	if m.fn != nil {
		return m.fn(ctx, query, topK)
	}
	return nil, nil
}

// mockBM25Repo 模拟 BM25Repo。
type mockBM25Repo struct {
	fn        func(ctx context.Context, query string, topK int) ([]domain.Candidate, error)
	calls     int
	lastQuery string
	lastTopK  int
}

func (m *mockBM25Repo) Search(ctx context.Context, query string, topK int) ([]domain.Candidate, error) {
	m.calls++
	m.lastQuery = query
	m.lastTopK = topK
	if m.fn != nil {
		return m.fn(ctx, query, topK)
	}
	return nil, nil
}

// mockEmbedder 模拟 Embedder，返回固定向量并记录输入文本。
type mockEmbedder struct {
	fn       func(ctx context.Context, text string) ([]float32, error)
	calls    int
	lastText string
}

func (m *mockEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	m.calls++
	m.lastText = text
	if m.fn != nil {
		return m.fn(ctx, text)
	}
	return []float32{0.1, 0.2, 0.3}, nil
}

// floatEq 比较两个浮点数是否在容差内相等。
func floatEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// TestContentRecaller_Name 验证召回器名称。
func TestContentRecaller_Name(t *testing.T) {
	r := NewContentRecaller(&mockVectorRepo{}, &mockBM25Repo{}, &mockEmbedder{}, 10)
	if got := r.Name(); got != "content" {
		t.Errorf("Name() = %q, 期望 content", got)
	}
}

// TestContentRecaller_RRFFusion 验证双路成功时 RRF 融合分数与排序。
func TestContentRecaller_RRFFusion(t *testing.T) {
	vecRepo := &mockVectorRepo{
		fn: func(ctx context.Context, query []float32, topK int) ([]domain.Candidate, error) {
			return []domain.Candidate{
				{ArticleID: "a1", Score: 0.9},
				{ArticleID: "a2", Score: 0.8},
			}, nil
		},
	}
	bmRepo := &mockBM25Repo{
		fn: func(ctx context.Context, query string, topK int) ([]domain.Candidate, error) {
			return []domain.Candidate{
				{ArticleID: "a2", Score: 0.7},
				{ArticleID: "a3", Score: 0.6},
			}, nil
		},
	}
	r := NewContentRecaller(vecRepo, bmRepo, &mockEmbedder{}, 10)
	res, err := r.Recall(context.Background(), domain.RecallRequest{
		Intent: &domain.Intent{
			Entities: []domain.Entity{{Name: "AI"}},
		},
		TopK: 10,
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if res.Source != "content" {
		t.Errorf("Source = %q, 期望 content", res.Source)
	}
	if len(res.Candidates) != 3 {
		t.Fatalf("候选数 = %d, 期望 3（去重后）", len(res.Candidates))
	}
	// RRF 期望：a2 同时出现在向量(rank2)与 BM25(rank1)，分数最高。
	// a2 = 1/(60+2) + 1/(60+1) = 1/62 + 1/61
	// a1 = 1/(60+1) = 1/61（仅向量 rank1）
	// a3 = 1/(60+2) = 1/62（仅 BM25 rank2）
	wantA2 := 1.0/62 + 1.0/61
	wantA1 := 1.0 / 61
	wantA3 := 1.0 / 62
	if res.Candidates[0].ArticleID != "a2" {
		t.Errorf("首位应为 a2（RRF 分数最高）, 实际 %q", res.Candidates[0].ArticleID)
	}
	if !floatEq(res.Candidates[0].Score, wantA2) {
		t.Errorf("a2 分数 = %v, 期望 %v", res.Candidates[0].Score, wantA2)
	}
	// a1 应排在 a3 前（1/61 > 1/62）。
	if res.Candidates[1].ArticleID != "a1" {
		t.Errorf("第二位应为 a1, 实际 %q", res.Candidates[1].ArticleID)
	}
	if !floatEq(res.Candidates[1].Score, wantA1) {
		t.Errorf("a1 分数 = %v, 期望 %v", res.Candidates[1].Score, wantA1)
	}
	if res.Candidates[2].ArticleID != "a3" {
		t.Errorf("第三位应为 a3, 实际 %q", res.Candidates[2].ArticleID)
	}
	if !floatEq(res.Candidates[2].Score, wantA3) {
		t.Errorf("a3 分数 = %v, 期望 %v", res.Candidates[2].Score, wantA3)
	}
	// 融合后 Source 标记为 content。
	for _, c := range res.Candidates {
		if c.Source != "content" {
			t.Errorf("候选 %q Source = %q, 期望 content", c.ArticleID, c.Source)
		}
	}
}

// TestContentRecaller_DeriveQueryFromEntities 验证查询文本从 Intent 实体派生并传递给 Embedder/BM25。
func TestContentRecaller_DeriveQueryFromEntities(t *testing.T) {
	emb := &mockEmbedder{}
	bmRepo := &mockBM25Repo{
		fn: func(ctx context.Context, query string, topK int) ([]domain.Candidate, error) {
			return nil, nil
		},
	}
	r := NewContentRecaller(&mockVectorRepo{}, bmRepo, emb, 10)
	_, _ = r.Recall(context.Background(), domain.RecallRequest{
		Intent: &domain.Intent{
			Entities: []domain.Entity{{Name: "AI"}, {Name: "Agent"}},
		},
	})
	if emb.lastText != "AI Agent" {
		t.Errorf("Embedder 收到文本 = %q, 期望 'AI Agent'", emb.lastText)
	}
	if bmRepo.lastQuery != "AI Agent" {
		t.Errorf("BM25 收到查询 = %q, 期望 'AI Agent'", bmRepo.lastQuery)
	}
}

// TestContentRecaller_VectorOnlyFallback 验证 BM25 失败时降级到向量结果。
func TestContentRecaller_VectorOnlyFallback(t *testing.T) {
	vecRepo := &mockVectorRepo{
		fn: func(ctx context.Context, query []float32, topK int) ([]domain.Candidate, error) {
			return []domain.Candidate{{ArticleID: "v1"}, {ArticleID: "v2"}}, nil
		},
	}
	bmRepo := &mockBM25Repo{
		fn: func(ctx context.Context, query string, topK int) ([]domain.Candidate, error) {
			return nil, errSentinel
		},
	}
	r := NewContentRecaller(vecRepo, bmRepo, &mockEmbedder{}, 10)
	res, err := r.Recall(context.Background(), domain.RecallRequest{})
	if err != nil {
		t.Fatalf("BM25 单路失败不应返回错误, 实际 %v", err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("候选数 = %d, 期望 2（仅向量）", len(res.Candidates))
	}
}

// TestContentRecaller_BM25OnlyFallback 验证向量失败时降级到 BM25 结果。
func TestContentRecaller_BM25OnlyFallback(t *testing.T) {
	vecRepo := &mockVectorRepo{
		fn: func(ctx context.Context, query []float32, topK int) ([]domain.Candidate, error) {
			return nil, errSentinel
		},
	}
	bmRepo := &mockBM25Repo{
		fn: func(ctx context.Context, query string, topK int) ([]domain.Candidate, error) {
			return []domain.Candidate{{ArticleID: "b1"}}, nil
		},
	}
	r := NewContentRecaller(vecRepo, bmRepo, &mockEmbedder{}, 10)
	res, err := r.Recall(context.Background(), domain.RecallRequest{})
	if err != nil {
		t.Fatalf("向量单路失败不应返回错误, 实际 %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("候选数 = %d, 期望 1（仅 BM25）", len(res.Candidates))
	}
	if res.Candidates[0].ArticleID != "b1" {
		t.Errorf("候选 = %q, 期望 b1", res.Candidates[0].ArticleID)
	}
}

// TestContentRecaller_BothFail 验证双路失败返回错误。
func TestContentRecaller_BothFail(t *testing.T) {
	vecRepo := &mockVectorRepo{
		fn: func(ctx context.Context, query []float32, topK int) ([]domain.Candidate, error) {
			return nil, errSentinel
		},
	}
	bmRepo := &mockBM25Repo{
		fn: func(ctx context.Context, query string, topK int) ([]domain.Candidate, error) {
			return nil, errors.New("bm25 down")
		},
	}
	r := NewContentRecaller(vecRepo, bmRepo, &mockEmbedder{}, 10)
	_, err := r.Recall(context.Background(), domain.RecallRequest{})
	if err == nil {
		t.Fatalf("双路失败应返回错误")
	}
}

// TestContentRecaller_NoEmbedder 验证无 Embedder 时向量不可用、降级到 BM25。
func TestContentRecaller_NoEmbedder(t *testing.T) {
	vecRepo := &mockVectorRepo{}
	bmRepo := &mockBM25Repo{
		fn: func(ctx context.Context, query string, topK int) ([]domain.Candidate, error) {
			return []domain.Candidate{{ArticleID: "b1"}}, nil
		},
	}
	// embedder 传 nil。
	r := NewContentRecaller(vecRepo, bmRepo, nil, 10)
	res, err := r.Recall(context.Background(), domain.RecallRequest{})
	if err != nil {
		t.Fatalf("无 Embedder 应降级到 BM25, 不应返回错误, 实际 %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("候选数 = %d, 期望 1", len(res.Candidates))
	}
	if vecRepo.calls != 0 {
		t.Errorf("无 Embedder 不应调用 VectorRepo.Search, 实际调用 %d 次", vecRepo.calls)
	}
	if bmRepo.calls != 1 {
		t.Errorf("应调用 BM25Repo.Search 1 次, 实际 %d 次", bmRepo.calls)
	}
}

// TestContentRecaller_TopKCap 验证融合结果截断到 topK。
func TestContentRecaller_TopKCap(t *testing.T) {
	vecRepo := &mockVectorRepo{
		fn: func(ctx context.Context, query []float32, topK int) ([]domain.Candidate, error) {
			// 使用不同 ArticleID，避免被 fuseRRF 按 ArticleID 去重。
			cands := make([]domain.Candidate, 0, 5)
			for i := 0; i < 5; i++ {
				cands = append(cands, domain.Candidate{ArticleID: fmt.Sprintf("v%d", i)})
			}
			return cands, nil
		},
	}
	bmRepo := &mockBM25Repo{
		fn: func(ctx context.Context, query string, topK int) ([]domain.Candidate, error) {
			return nil, nil
		},
	}
	r := NewContentRecaller(vecRepo, bmRepo, &mockEmbedder{}, 3)
	res, err := r.Recall(context.Background(), domain.RecallRequest{})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Errorf("候选数 = %d, 期望截断到 3", len(res.Candidates))
	}
}

// TestContentRecaller_TopKFromRequest 验证 req.TopK 优先于默认 topK。
func TestContentRecaller_TopKFromRequest(t *testing.T) {
	vecRepo := &mockVectorRepo{}
	bmRepo := &mockBM25Repo{}
	r := NewContentRecaller(vecRepo, bmRepo, &mockEmbedder{}, 50)
	_, _ = r.Recall(context.Background(), domain.RecallRequest{TopK: 7})
	if vecRepo.lastTopK != 7 {
		t.Errorf("VectorRepo topK = %d, 期望 7", vecRepo.lastTopK)
	}
	if bmRepo.lastTopK != 7 {
		t.Errorf("BM25Repo topK = %d, 期望 7", bmRepo.lastTopK)
	}
}

// TestFuseRRF_DedupAndSort 验证 RRF 融合去重与排序。
func TestFuseRRF_DedupAndSort(t *testing.T) {
	lists := [][]domain.Candidate{
		{{ArticleID: "a", Score: 0.9}, {ArticleID: "b", Score: 0.8}},
		{{ArticleID: "b", Score: 0.7}, {ArticleID: "c", Score: 0.6}},
	}
	got := fuseRRF(lists, 60)
	if len(got) != 3 {
		t.Fatalf("融合后候选数 = %d, 期望 3", len(got))
	}
	// b 出现在两路（向量 rank2 + BM25 rank1），分数最高。
	if got[0].ArticleID != "b" {
		t.Errorf("首位应为 b, 实际 %q", got[0].ArticleID)
	}
	// 验证 b 的分数 = 1/62 + 1/61。
	wantB := 1.0/62 + 1.0/61
	if !floatEq(got[0].Score, wantB) {
		t.Errorf("b 分数 = %v, 期望 %v", got[0].Score, wantB)
	}
}

// TestFuseRRF_SingleList 验证单列表融合保持原顺序与分数。
func TestFuseRRF_SingleList(t *testing.T) {
	list := []domain.Candidate{
		{ArticleID: "a", Score: 0.9},
		{ArticleID: "b", Score: 0.8},
	}
	got := fuseRRF([][]domain.Candidate{list}, 60)
	if len(got) != 2 {
		t.Fatalf("候选数 = %d, 期望 2", len(got))
	}
	if got[0].ArticleID != "a" {
		t.Errorf("首位应为 a, 实际 %q", got[0].ArticleID)
	}
	// a rank1 → 1/61。
	if !floatEq(got[0].Score, 1.0/61) {
		t.Errorf("a 分数 = %v, 期望 %v", got[0].Score, 1.0/61)
	}
}

// TestFuseRRF_Empty 验证空列表输入返回空结果。
func TestFuseRRF_Empty(t *testing.T) {
	got := fuseRRF(nil, 60)
	if len(got) != 0 {
		t.Errorf("空输入应返回空结果, 实际 %d", len(got))
	}
	got = fuseRRF([][]domain.Candidate{{}}, 60)
	if len(got) != 0 {
		t.Errorf("空列表应返回空结果, 实际 %d", len(got))
	}
}
