package rank

import (
	"context"
	"math"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 WeightedRanker 的加权排序逻辑，覆盖：
//   - 加权分数计算正确性（分数 = sum(weight_i * feature_i)）
//   - 降序排列
//   - 空候选列表
//   - Candidate.Scores 为 nil 时分数为 0
//   - 不修改入参候选列表
//   - Name() 返回 "weighted"
// ============================================================================

// TestWeightedRanker_Name 验证排序器名称。
func TestWeightedRanker_Name(t *testing.T) {
	r := NewWeightedRanker(nil)
	if got := r.Name(); got != "weighted" {
		t.Errorf("Name() = %q, 期望 weighted", got)
	}
}

// TestWeightedRanker_Basic 验证加权分数计算与降序排列。
// 权重 {"a": 0.5, "b": 0.5}，三个候选：
//   - c1: a=1.0, b=1.0 → 1.0
//   - c2: a=0.5, b=0.5 → 0.5
//   - c3: a=0.8, b=0.2 → 0.5
//
// 期望顺序：c1 > c2 == c3（稳定排序保持原序）
func TestWeightedRanker_Basic(t *testing.T) {
	r := NewWeightedRanker(map[string]float64{
		"a": 0.5,
		"b": 0.5,
	})
	cands := []domain.Candidate{
		{ArticleID: "c1", Scores: map[string]float64{"a": 1.0, "b": 1.0}},
		{ArticleID: "c2", Scores: map[string]float64{"a": 0.5, "b": 0.5}},
		{ArticleID: "c3", Scores: map[string]float64{"a": 0.8, "b": 0.2}},
	}
	res, err := r.Rank(context.Background(), domain.RankContext{Candidates: cands})
	if err != nil {
		t.Fatalf("Rank 返回错误: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Fatalf("候选数 = %d, 期望 3", len(res.Candidates))
	}
	if res.Candidates[0].ArticleID != "c1" {
		t.Errorf("首位 ArticleID = %q, 期望 c1", res.Candidates[0].ArticleID)
	}
	if res.Candidates[0].Score != 1.0 {
		t.Errorf("首位 Score = %v, 期望 1.0", res.Candidates[0].Score)
	}
	// c2 与 c3 分数均为 0.5，稳定排序保持原序
	if res.Candidates[1].ArticleID != "c2" {
		t.Errorf("第二位 ArticleID = %q, 期望 c2（稳定排序）", res.Candidates[1].ArticleID)
	}
	if res.Candidates[2].ArticleID != "c3" {
		t.Errorf("第三位 ArticleID = %q, 期望 c3（稳定排序）", res.Candidates[2].ArticleID)
	}
}

// TestWeightedRanker_NilScores 验证 Candidate.Scores 为 nil 时分数为 0。
func TestWeightedRanker_NilScores(t *testing.T) {
	r := NewWeightedRanker(map[string]float64{"a": 1.0})
	cands := []domain.Candidate{
		{ArticleID: "x", Scores: nil},
		{ArticleID: "y", Scores: map[string]float64{"a": 0.5}},
	}
	res, err := r.Rank(context.Background(), domain.RankContext{Candidates: cands})
	if err != nil {
		t.Fatalf("Rank 返回错误: %v", err)
	}
	// y 分数 0.5 > x 分数 0.0
	if res.Candidates[0].ArticleID != "y" {
		t.Errorf("首位 ArticleID = %q, 期望 y", res.Candidates[0].ArticleID)
	}
	if res.Candidates[1].Score != 0.0 {
		t.Errorf("nil Scores 候选 Score = %v, 期望 0", res.Candidates[1].Score)
	}
}

// TestWeightedRanker_Empty 验证空候选列表返回空结果。
func TestWeightedRanker_Empty(t *testing.T) {
	r := NewWeightedRanker(map[string]float64{"a": 1.0})
	res, err := r.Rank(context.Background(), domain.RankContext{Candidates: nil})
	if err != nil {
		t.Fatalf("Rank 返回错误: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("空候选返回 %d 个, 期望 0", len(res.Candidates))
	}
}

// TestWeightedRanker_DoesNotMutateInput 验证不修改入参候选列表。
func TestWeightedRanker_DoesNotMutateInput(t *testing.T) {
	r := NewWeightedRanker(map[string]float64{"a": 1.0})
	cands := []domain.Candidate{
		{ArticleID: "x", Score: 99.0, Scores: map[string]float64{"a": 0.5}},
	}
	_, _ = r.Rank(context.Background(), domain.RankContext{Candidates: cands})
	if cands[0].Score != 99.0 {
		t.Errorf("入参 Score 被修改为 %v, 期望保持 99.0", cands[0].Score)
	}
}

// TestWeightedRanker_WeightedScoreExact 验证加权分数精确值。
func TestWeightedRanker_WeightedScoreExact(t *testing.T) {
	r := NewWeightedRanker(map[string]float64{
		"cf":    0.3,
		"graph": 0.2,
		"fresh": 0.5,
	})
	cands := []domain.Candidate{
		{ArticleID: "x", Scores: map[string]float64{"cf": 0.8, "graph": 0.6, "fresh": 0.4}},
	}
	res, err := r.Rank(context.Background(), domain.RankContext{Candidates: cands})
	if err != nil {
		t.Fatalf("Rank 返回错误: %v", err)
	}
	expected := 0.3*0.8 + 0.2*0.6 + 0.5*0.4
	if math.Abs(res.Candidates[0].Score-expected) > 1e-9 {
		t.Errorf("Score = %v, 期望 %v", res.Candidates[0].Score, expected)
	}
}
