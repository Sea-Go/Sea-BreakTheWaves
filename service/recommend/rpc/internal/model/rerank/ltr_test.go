package rerank

import (
	"context"
	"math"
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// TestLambdaMART_Rerank_Stub 验证 model 为 nil 时走加权特征 stub 并正确排序。
func TestLambdaMART_Rerank_Stub(t *testing.T) {
	fe := NewFeatureExtractor()
	lm := NewLambdaMART(nil, fe)
	candidates := []domain.Candidate{
		{
			ArticleID: "a1",
			Extra:     map[string]any{"title": "AI", "tags": []string{"AI"}},
			Scores:    map[string]float64{"click_rate": 0.5, "quality_authority": 0.5},
		},
		{
			ArticleID: "a2",
			Extra:     map[string]any{"title": "美食", "tags": []string{"美食"}},
			Scores:    map[string]float64{"click_rate": 0.3, "quality_authority": 0.3},
		},
		{
			ArticleID: "a3",
			Extra:     map[string]any{"title": "AI", "tags": []string{"AI"}},
			Scores:    map[string]float64{"click_rate": 0.9, "quality_authority": 0.9},
		},
	}
	profile := domain.UserProfile{
		Key:    domain.UserKey{UserID: "u1"},
		Static: &domain.StaticProfile{Interests: []string{"AI"}},
	}
	temporal := domain.TemporalProfile{PeriodicPattern: domain.NewPeriodicPattern()}

	out, err := lm.Rerank(context.Background(), candidates, profile, temporal, 2)
	if err != nil {
		t.Fatalf("Rerank 错误: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("Rerank 返回数量期望 2, 实际 %d", len(out))
	}
	if out[0].Score < out[1].Score {
		t.Errorf("应按分数降序, out[0]=%v < out[1]=%v", out[0].Score, out[1].Score)
	}
	// a3（高行为+高质量+兴趣匹配）应排在首位
	if out[0].ArticleID != "a3" {
		t.Errorf("首位期望 a3, 实际 %s", out[0].ArticleID)
	}
}

// TestLambdaMART_Rerank_WithModel 验证模型加载后走 Predict 路径。
func TestLambdaMART_Rerank_WithModel(t *testing.T) {
	m := &stubModel{predict: func(input []float64) (float64, error) {
		// 返回输入向量长度作为分数（特征向量维度）
		return float64(len(input)), nil
	}}
	lm := NewLambdaMART(m, nil) // fe 为 nil，自动创建
	candidates := []domain.Candidate{
		{ArticleID: "a1", Extra: map[string]any{"title": "AI", "tags": []string{"AI"}}},
		{ArticleID: "a2", Extra: map[string]any{"title": "美食", "tags": []string{"美食"}}},
	}
	profile := domain.UserProfile{Key: domain.UserKey{UserID: "u1"}}
	temporal := domain.TemporalProfile{PeriodicPattern: domain.NewPeriodicPattern()}

	out, err := lm.Rerank(context.Background(), candidates, profile, temporal, 0)
	if err != nil {
		t.Fatalf("Rerank 错误: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("topK=0 不截断, 期望 2, 实际 %d", len(out))
	}
	if m.calls != 2 {
		t.Errorf("Predict 调用次数期望 2, 实际 %d", m.calls)
	}
	// 输入特征维度应与 featureKeys 长度一致
	expectedDim := len(featureKeys())
	if out[0].Score != float64(expectedDim) {
		t.Errorf("Predict 输入维度期望 %d, 实际 %v", expectedDim, out[0].Score)
	}
}

// TestDCG 验证 DCG 计算。
func TestDCG(t *testing.T) {
	rels := []float64{3, 2, 3, 0, 1, 2}
	dcg := DCG(rels)
	// 手算: 3/log2(2) + 2/log2(3) + 3/log2(4) + 0 + 1/log2(6) + 2/log2(7)
	expected := 3.0/math.Log2(2) + 2.0/math.Log2(3) + 3.0/math.Log2(4) +
		0.0 + 1.0/math.Log2(6) + 2.0/math.Log2(7)
	if math.Abs(dcg-expected) > 1e-9 {
		t.Errorf("DCG 期望 %v, 实际 %v", expected, dcg)
	}
	if DCG(nil) != 0 {
		t.Errorf("DCG(nil) 期望 0, 实际 %v", DCG(nil))
	}
}

// TestIDCG 验证 IDCG（二值理想排序）。
func TestIDCG(t *testing.T) {
	n := 5
	idcg := IDCG(n)
	ideal := make([]float64, n)
	for i := range ideal {
		ideal[i] = 1.0
	}
	expected := DCG(ideal)
	if math.Abs(idcg-expected) > 1e-9 {
		t.Errorf("IDCG(%d) 期望 %v, 实际 %v", n, expected, idcg)
	}
	if IDCG(0) != 0 {
		t.Errorf("IDCG(0) 期望 0, 实际 %v", IDCG(0))
	}
	if IDCG(-1) != 0 {
		t.Errorf("IDCG(-1) 期望 0, 实际 %v", IDCG(-1))
	}
}

// TestNDCGAtK_PerfectRanking 验证完美排序 NDCG=1.0。
func TestNDCGAtK_PerfectRanking(t *testing.T) {
	ranked := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
		{ArticleID: "a3"},
	}
	labels := map[string]float64{
		"a1": 3.0,
		"a2": 2.0,
		"a3": 1.0,
	}
	ndcg := NDCGAtK(ranked, labels, 3)
	if math.Abs(ndcg-1.0) > 1e-9 {
		t.Errorf("完美排序 NDCG 期望 1.0, 实际 %v", ndcg)
	}
}

// TestNDCGAtK_WorstRanking 验证非完美排序 NDCG<1.0 且>0。
func TestNDCGAtK_WorstRanking(t *testing.T) {
	// relevance 升序（最差排序）
	ranked := []domain.Candidate{
		{ArticleID: "a1"}, // rel=1
		{ArticleID: "a2"}, // rel=2
		{ArticleID: "a3"}, // rel=3
	}
	labels := map[string]float64{
		"a1": 1.0,
		"a2": 2.0,
		"a3": 3.0,
	}
	ndcg := NDCGAtK(ranked, labels, 3)
	if ndcg >= 1.0 {
		t.Errorf("最差排序 NDCG 应 < 1.0, 实际 %v", ndcg)
	}
	if ndcg <= 0 {
		t.Errorf("最差排序 NDCG 应 > 0, 实际 %v", ndcg)
	}
}

// TestNDCGAtK_KLargerThanRanked 验证 k 超过 ranked 长度时截断。
func TestNDCGAtK_KLargerThanRanked(t *testing.T) {
	ranked := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
	}
	labels := map[string]float64{"a1": 2.0, "a2": 1.0}
	ndcg := NDCGAtK(ranked, labels, 5)
	if math.Abs(ndcg-1.0) > 1e-9 {
		t.Errorf("完美排序（k>len）NDCG 期望 1.0, 实际 %v", ndcg)
	}
}

// TestNDCGAtK_Empty 验证空输入与 k=0 返回 0。
func TestNDCGAtK_Empty(t *testing.T) {
	if NDCGAtK(nil, nil, 3) != 0 {
		t.Errorf("空输入 NDCG 期望 0")
	}
	if NDCGAtK([]domain.Candidate{{ArticleID: "a1"}}, map[string]float64{"a1": 1}, 0) != 0 {
		t.Errorf("k=0 NDCG 期望 0")
	}
}

// TestNDCGAtK_BinaryConsistency 验证二值场景与 IDCG 的一致性。
func TestNDCGAtK_BinaryConsistency(t *testing.T) {
	// 两个相关项排在第 1、3 位
	ranked := []domain.Candidate{
		{ArticleID: "a1"}, // rel=1
		{ArticleID: "a2"}, // rel=0
		{ArticleID: "a3"}, // rel=1
	}
	labels := map[string]float64{"a1": 1.0, "a2": 0.0, "a3": 1.0}
	ndcg := NDCGAtK(ranked, labels, 3)
	// actual DCG = 1/log2(2) + 0 + 1/log2(4) = 1 + 0.5 = 1.5
	// ideal DCG (2 个相关) = 1/log2(2) + 1/log2(3) = 1 + 0.6309...
	actualDCG := 1.0/math.Log2(2) + 1.0/math.Log2(4)
	idealDCG := 1.0/math.Log2(2) + 1.0/math.Log2(3)
	expected := actualDCG / idealDCG
	if math.Abs(ndcg-expected) > 1e-9 {
		t.Errorf("二值场景 NDCG 期望 %v, 实际 %v", expected, ndcg)
	}
}
