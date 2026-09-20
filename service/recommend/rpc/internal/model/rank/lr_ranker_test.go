package rank

import (
	"context"
	"math"
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 LRRanker 的逻辑回归排序逻辑，覆盖：
//   - sigmoid 计算正确性（score = sigmoid(sum(w*x) + bias)）
//   - 降序排列
//   - bias 单独生效（Scores 为 nil 时 score = sigmoid(bias)）
//   - 空候选列表
//   - Name() 返回 "lr"
// ============================================================================

// TestLRRanker_Name 验证排序器名称。
func TestLRRanker_Name(t *testing.T) {
	r := NewLRRanker(nil, 0)
	if got := r.Name(); got != "lr" {
		t.Errorf("Name() = %q, 期望 lr", got)
	}
}

// TestLRRanker_SigmoidCalc 验证 sigmoid 计算正确性。
// 权重 {"a": 1.0}，bias = 0，候选 a=1.0 → z=1.0 → sigmoid(1.0) ≈ 0.731
func TestLRRanker_SigmoidCalc(t *testing.T) {
	r := NewLRRanker(map[string]float64{"a": 1.0}, 0)
	cands := []domain.Candidate{
		{ArticleID: "x", Scores: map[string]float64{"a": 1.0}},
	}
	res, err := r.Rank(context.Background(), domain.RankContext{Candidates: cands})
	if err != nil {
		t.Fatalf("Rank 返回错误: %v", err)
	}
	expected := 1.0 / (1.0 + math.Exp(-1.0))
	if math.Abs(res.Candidates[0].Score-expected) > 1e-9 {
		t.Errorf("Score = %v, 期望 sigmoid(1.0) = %v", res.Candidates[0].Score, expected)
	}
}

// TestLRRanker_BiasOnly 验证 Scores 为 nil 时 score = sigmoid(bias)。
func TestLRRanker_BiasOnly(t *testing.T) {
	r := NewLRRanker(map[string]float64{"a": 1.0}, 2.0)
	cands := []domain.Candidate{
		{ArticleID: "x", Scores: nil},
	}
	res, err := r.Rank(context.Background(), domain.RankContext{Candidates: cands})
	if err != nil {
		t.Fatalf("Rank 返回错误: %v", err)
	}
	expected := 1.0 / (1.0 + math.Exp(-2.0))
	if math.Abs(res.Candidates[0].Score-expected) > 1e-9 {
		t.Errorf("Score = %v, 期望 sigmoid(2.0) = %v", res.Candidates[0].Score, expected)
	}
}

// TestLRRanker_DescendingOrder 验证降序排列。
// 候选 z 值越大 sigmoid 越大，应排前面。
func TestLRRanker_DescendingOrder(t *testing.T) {
	r := NewLRRanker(map[string]float64{"a": 1.0}, 0)
	cands := []domain.Candidate{
		{ArticleID: "low", Scores: map[string]float64{"a": -2.0}},
		{ArticleID: "high", Scores: map[string]float64{"a": 3.0}},
		{ArticleID: "mid", Scores: map[string]float64{"a": 0.0}},
	}
	res, err := r.Rank(context.Background(), domain.RankContext{Candidates: cands})
	if err != nil {
		t.Fatalf("Rank 返回错误: %v", err)
	}
	if res.Candidates[0].ArticleID != "high" {
		t.Errorf("首位 ArticleID = %q, 期望 high", res.Candidates[0].ArticleID)
	}
	if res.Candidates[1].ArticleID != "mid" {
		t.Errorf("第二位 ArticleID = %q, 期望 mid", res.Candidates[1].ArticleID)
	}
	if res.Candidates[2].ArticleID != "low" {
		t.Errorf("第三位 ArticleID = %q, 期望 low", res.Candidates[2].ArticleID)
	}
	// 所有分数应在 (0, 1) 区间
	for _, c := range res.Candidates {
		if c.Score <= 0 || c.Score >= 1 {
			t.Errorf("Score = %v 不在 (0,1) 区间", c.Score)
		}
	}
}

// TestLRRanker_Empty 验证空候选列表返回空结果。
func TestLRRanker_Empty(t *testing.T) {
	r := NewLRRanker(map[string]float64{"a": 1.0}, 0)
	res, err := r.Rank(context.Background(), domain.RankContext{Candidates: nil})
	if err != nil {
		t.Fatalf("Rank 返回错误: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("空候选返回 %d 个, 期望 0", len(res.Candidates))
	}
}

// TestSigmoid 辅助测试 sigmoid 函数边界值。
func TestSigmoid(t *testing.T) {
	cases := []struct {
		z        float64
		expected float64
	}{
		{0, 0.5},
		{100, 1.0}, // 极大值趋近 1
	}
	for _, c := range cases {
		got := sigmoid(c.z)
		if math.Abs(got-c.expected) > 1e-9 {
			t.Errorf("sigmoid(%v) = %v, 期望 %v", c.z, got, c.expected)
		}
	}
	// 极小值趋近 0
	if got := sigmoid(-100); got > 1e-9 {
		t.Errorf("sigmoid(-100) = %v, 期望趋近 0", got)
	}
}
