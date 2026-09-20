// internal/eval/metrics_test.go — 评估指标单测（Task 15.2）。
//
// 覆盖：
//   - Recall@K / Precision@K / NDCG@K 边界（空/单元素/完全匹配/完全不匹配/K 截断）
//   - CTR / CVR 边界（除零/正常/超额）
//   - MRR 边界（命中/未命中/多命中取首个）
//   - MAP 边界（多 query/空/不匹配长度）
//   - CalculateAll 返回结构正确
//   - LLM Judge stub + RubricJudge 权重调整
//
// 使用 stdlib 手写 stub，不依赖 testify。
package eval

import (
	"context"
	"errors"
	"math"
	"testing"
)

// ============================================================================
// stub 实现
// ============================================================================

// stubLLMJudge 测试用 LLMJudge，可配置返回分数与错误。
type stubLLMJudge struct {
	score float64
	err   error
	calls int
}

func (s *stubLLMJudge) Judge(_ context.Context, _ string, _ []string) (float64, error) {
	s.calls++
	return s.score, s.err
}

// floatEq 浮点近似比较。
func floatEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// ============================================================================
// 测试用例 — Recall@K
// ============================================================================

// TestRecallAtK_Boundaries 验证 Recall@K 各边界。
func TestRecallAtK_Boundaries(t *testing.T) {
	calc := NewMetricsCalculator()
	cases := []struct {
		name     string
		ranked   []string
		expected []string
		k        int
		want     float64
	}{
		{"empty ranked", nil, []string{"a"}, 3, 0},
		{"empty expected", []string{"a"}, nil, 3, 0},
		{"k=0", []string{"a"}, []string{"a"}, 0, 0},
		{"perfect match", []string{"a", "b"}, []string{"a", "b"}, 2, 1.0},
		{"partial 1 of 2", []string{"a", "x"}, []string{"a", "b"}, 2, 0.5},
		{"no match", []string{"x", "y"}, []string{"a", "b"}, 2, 0.0},
		{"k truncation", []string{"x", "y", "a"}, []string{"a"}, 2, 0.0},
		{"k larger than ranked", []string{"a"}, []string{"a", "b"}, 5, 0.5},
	}
	for _, c := range cases {
		got := calc.RecallAtK(c.ranked, c.expected, c.k)
		if !floatEq(got, c.want) {
			t.Errorf("%s: Recall@%d = %v, 期望 %v", c.name, c.k, got, c.want)
		}
	}
}

// ============================================================================
// 测试用例 — Precision@K
// ============================================================================

// TestPrecisionAtK_Boundaries 验证 Precision@K 各边界。
func TestPrecisionAtK_Boundaries(t *testing.T) {
	calc := NewMetricsCalculator()
	cases := []struct {
		name     string
		ranked   []string
		expected []string
		k        int
		want     float64
	}{
		{"empty ranked", nil, []string{"a"}, 3, 0.0},
		{"k=0", []string{"a"}, []string{"a"}, 0, 0.0},
		{"2 hits in 4", []string{"a", "b", "c", "d"}, []string{"a", "b"}, 4, 0.5},
		{"1 hit in 3", []string{"a", "x", "y"}, []string{"a"}, 3, 1.0 / 3.0},
		{"0 hits", []string{"x", "y"}, []string{"a"}, 2, 0.0},
		{"k truncation", []string{"a", "b", "c"}, []string{"c"}, 2, 0.0},
		{"k larger than ranked", []string{"a"}, []string{"a"}, 5, 0.2}, // 1 hit / 5
	}
	for _, c := range cases {
		got := calc.PrecisionAtK(c.ranked, c.expected, c.k)
		if !floatEq(got, c.want) {
			t.Errorf("%s: Precision@%d = %v, 期望 %v", c.name, c.k, got, c.want)
		}
	}
}

// ============================================================================
// 测试用例 — NDCG@K
// ============================================================================

// TestNDCGAtK_Boundaries 验证 NDCG@K 各边界。
func TestNDCGAtK_Boundaries(t *testing.T) {
	calc := NewMetricsCalculator()
	cases := []struct {
		name     string
		ranked   []string
		expected []string
		k        int
		want     float64
	}{
		{"empty", nil, nil, 3, 0},
		{"empty expected", []string{"a"}, nil, 3, 0},
		{"k=0", []string{"a"}, []string{"a"}, 0, 0},
		{"perfect single", []string{"a", "b", "c"}, []string{"a"}, 3, 1.0},
		{"perfect multi", []string{"a", "b", "c"}, []string{"a", "b"}, 3, 1.0},
		{"no hit", []string{"x", "y", "z"}, []string{"a"}, 3, 0.0},
	}
	for _, c := range cases {
		got := calc.NDCGAtK(c.ranked, c.expected, c.k)
		if !floatEq(got, c.want) {
			t.Errorf("%s: NDCG@%d = %v, 期望 %v", c.name, c.k, got, c.want)
		}
	}
}

// TestNDCGAtK_PartialMatch 验证部分命中 NDCG 数值正确。
func TestNDCGAtK_PartialMatch(t *testing.T) {
	calc := NewMetricsCalculator()
	// ranked=[b, a, c], expected=[a]
	// DCG = 1/log2(3) ≈ 0.6309
	// IDCG = 1/log2(2) = 1.0
	ranked := []string{"b", "a", "c"}
	expected := []string{"a"}
	got := calc.NDCGAtK(ranked, expected, 3)
	want := 1.0 / math.Log2(3)
	if !floatEq(got, want) {
		t.Errorf("NDCG = %v, 期望 %v", got, want)
	}
}

// TestNDCGAtK_KTruncation 验证 K 截断。
func TestNDCGAtK_KTruncation(t *testing.T) {
	calc := NewMetricsCalculator()
	// a 在位置 2（i=2）>= k=2 → 不计入 DCG → 0
	ranked := []string{"b", "c", "a"}
	expected := []string{"a"}
	if got := calc.NDCGAtK(ranked, expected, 2); got != 0 {
		t.Errorf("K 截断 NDCG = %v, 期望 0", got)
	}
}

// ============================================================================
// 测试用例 — CTR / CVR
// ============================================================================

// TestCTR 验证 CTR 计算。
func TestCTR(t *testing.T) {
	calc := NewMetricsCalculator()
	cases := []struct {
		name        string
		impressions int
		clicks      int
		want        float64
	}{
		{"zero impressions", 0, 5, 0},
		{"negative impressions", -1, 5, 0},
		{"zero clicks", 100, 0, 0},
		{"half", 100, 50, 0.5},
		{"all", 100, 100, 1.0},
		{"over 1", 50, 100, 2.0},
	}
	for _, c := range cases {
		got := calc.CTR(c.impressions, c.clicks)
		if !floatEq(got, c.want) {
			t.Errorf("%s: CTR = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

// TestCVR 验证 CVR 计算。
func TestCVR(t *testing.T) {
	calc := NewMetricsCalculator()
	cases := []struct {
		name        string
		clicks      int
		conversions int
		want        float64
	}{
		{"zero clicks", 0, 5, 0},
		{"negative clicks", -1, 5, 0},
		{"zero conversions", 100, 0, 0},
		{"half", 100, 50, 0.5},
		{"all", 100, 100, 1.0},
	}
	for _, c := range cases {
		got := calc.CVR(c.clicks, c.conversions)
		if !floatEq(got, c.want) {
			t.Errorf("%s: CVR = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

// ============================================================================
// 测试用例 — MRR
// ============================================================================

// TestMRR_Boundaries 验证 MRR 各边界。
func TestMRR_Boundaries(t *testing.T) {
	calc := NewMetricsCalculator()
	cases := []struct {
		name     string
		ranked   []string
		expected []string
		want     float64
	}{
		{"empty ranked", nil, []string{"a"}, 0},
		{"empty expected", []string{"a"}, nil, 0},
		{"hit at rank 1", []string{"a", "b"}, []string{"a"}, 1.0},
		{"hit at rank 2", []string{"b", "a"}, []string{"a"}, 0.5},
		{"hit at rank 3", []string{"b", "c", "a"}, []string{"a"}, 1.0 / 3.0},
		{"no hit", []string{"x", "y"}, []string{"a"}, 0},
		{"multi expected first hit at rank 2", []string{"b", "a", "c"}, []string{"a", "c"}, 0.5},
	}
	for _, c := range cases {
		got := calc.MRR(c.ranked, c.expected)
		if !floatEq(got, c.want) {
			t.Errorf("%s: MRR = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

// ============================================================================
// 测试用例 — MAP
// ============================================================================

// TestMAP 验证 MAP 计算。
func TestMAP(t *testing.T) {
	calc := NewMetricsCalculator()
	// 单 query: ranked=[a, b, c], expected=[a, c]
	// AP: i=0 a 命中 hits=1 precision=1/1=1.0
	//     i=2 c 命中 hits=2 precision=2/3≈0.6667
	// AP = (1.0 + 0.6667) / 2 = 0.8333
	queries := [][]string{{"a", "b", "c"}}
	expected := [][]string{{"a", "c"}}
	got := calc.MAP(queries, expected)
	want := (1.0 + 2.0/3.0) / 2.0
	if !floatEq(got, want) {
		t.Errorf("MAP = %v, 期望 %v", got, want)
	}
}

// TestMAP_MultiQueries 验证多 query MAP。
func TestMAP_MultiQueries(t *testing.T) {
	calc := NewMetricsCalculator()
	// query1: ranked=[a, b, c], expected=[a, c] → AP = (1 + 2/3) / 2 = 0.8333
	// query2: ranked=[x, y], expected=[z] → AP = 0
	// MAP = (0.8333 + 0) / 2 = 0.4167
	queries := [][]string{{"a", "b", "c"}, {"x", "y"}}
	expected := [][]string{{"a", "c"}, {"z"}}
	got := calc.MAP(queries, expected)
	want := ((1.0 + 2.0/3.0) / 2.0) / 2.0
	if !floatEq(got, want) {
		t.Errorf("MAP = %v, 期望 %v", got, want)
	}
}

// TestMAP_Empty 验证空 MAP。
func TestMAP_Empty(t *testing.T) {
	calc := NewMetricsCalculator()
	if got := calc.MAP(nil, nil); got != 0 {
		t.Errorf("空 MAP = %v, 期望 0", got)
	}
	// 长度不匹配返回 0。
	if got := calc.MAP([][]string{{"a"}}, [][]string{{"a"}, {"b"}}); got != 0 {
		t.Errorf("长度不匹配 MAP = %v, 期望 0", got)
	}
}

// ============================================================================
// 测试用例 — CalculateAll
// ============================================================================

// TestCalculateAll 验证 CalculateAll 返回所有指标。
func TestCalculateAll(t *testing.T) {
	calc := NewMetricsCalculator()
	results := calc.CalculateAll([]string{"a", "b"}, []string{"a"}, 2)
	if len(results) != 4 {
		t.Fatalf("期望 4 个指标, 实际 %d", len(results))
	}
	names := make(map[string]bool, len(results))
	for _, r := range results {
		names[r.Name] = true
		if r.Sample != 1 {
			t.Errorf("Sample = %d, 期望 1", r.Sample)
		}
	}
	for _, want := range []string{"recall@2", "precision@2", "ndcg@2", "mrr"} {
		if !names[want] {
			t.Errorf("缺少指标 %s, 实际 %v", want, names)
		}
	}
}

// ============================================================================
// 测试用例 — LLM Judge
// ============================================================================

// TestLLMJudge_Stub 验证 stub LLMJudge 调用。
func TestLLMJudge_Stub(t *testing.T) {
	stub := &stubLLMJudge{score: 0.85}
	got, err := stub.Judge(context.Background(), "test query", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Judge 错误: %v", err)
	}
	if !floatEq(got, 0.85) {
		t.Errorf("Judge = %v, 期望 0.85", got)
	}
	if stub.calls != 1 {
		t.Errorf("期望调用 1 次, 实际 %d 次", stub.calls)
	}
}

// TestRubricJudge_NoRubrics 验证无 rubric 时直接返回 LLM 分数。
func TestRubricJudge_NoRubrics(t *testing.T) {
	stub := &stubLLMJudge{score: 0.7}
	judge := NewRubricJudge(stub, nil)
	got, err := judge.Judge(context.Background(), "q", []string{"a"})
	if err != nil {
		t.Fatalf("Judge 错误: %v", err)
	}
	if !floatEq(got, 0.7) {
		t.Errorf("无 rubric 时应返回原始分数 0.7, 实际 %v", got)
	}
}

// TestRubricJudge_Weighted 验证 rubric 权重调整。
func TestRubricJudge_Weighted(t *testing.T) {
	stub := &stubLLMJudge{score: 0.6}
	// 权重之和 = 1.0 → 维持原值。
	judge := NewRubricJudge(stub, map[string]float64{"relevance": 0.5, "diversity": 0.5})
	got, _ := judge.Judge(context.Background(), "q", []string{"a"})
	if !floatEq(got, 0.6) {
		t.Errorf("权重和=1 时应返回 0.6, 实际 %v", got)
	}
}

// TestRubricJudge_CappedAtOne 验证分数不超过 1。
func TestRubricJudge_CappedAtOne(t *testing.T) {
	stub := &stubLLMJudge{score: 0.8}
	// 权重之和 = 2.0 → 0.8 * 2 = 1.6 → 截断为 1.0。
	judge := NewRubricJudge(stub, map[string]float64{"a": 1.0, "b": 1.0})
	got, _ := judge.Judge(context.Background(), "q", []string{"a"})
	if got != 1.0 {
		t.Errorf("分数应截断为 1.0, 实际 %v", got)
	}
}

// TestRubricJudge_NilLLM 验证未注入 LLM 报错。
func TestRubricJudge_NilLLM(t *testing.T) {
	judge := NewRubricJudge(nil, map[string]float64{"a": 1.0})
	_, err := judge.Judge(context.Background(), "q", []string{"a"})
	if err == nil {
		t.Errorf("未注入 LLM 应报错")
	}
}

// TestRubricJudge_LLMError 验证 LLM 错误传播。
func TestRubricJudge_LLMError(t *testing.T) {
	stub := &stubLLMJudge{err: errors.New("llm error")}
	judge := NewRubricJudge(stub, nil)
	_, err := judge.Judge(context.Background(), "q", []string{"a"})
	if err == nil {
		t.Errorf("LLM 错误应传播")
	}
}
