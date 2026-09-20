// Package agent eval_test.go — EvalAgent 单测（Task 11.11）。
//
// 覆盖：
//   - Name/Run 基本流程
//   - 评估执行（tools.Invoke eval.run）
//   - NDCG@K / Precision@K / Recall@K 计算正确性
//   - LLM Judge（CompleteWithLogprobs）
//   - 行为漂移检测（good/bad metrics 方向正确）
//   - tools 为 nil 时不 panic
//   - 无用例时返回空报告
//   - SampleSize 采样
//
// 使用 stdlib 手写 stub（stubTool 复用自 channel_test.go），不依赖 testify。
package agent

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// stub 实现
// ============================================================================

// stubEvalLLM 测试用 LLMClient，可配置 logprobs 响应。
type stubEvalLLM struct {
	mu              sync.Mutex
	logprobsCalls   int
	structuredCalls int
	resp            string
	logprobs        [][]LogprobEntry
	err             error
}

func (s *stubEvalLLM) Complete(_ context.Context, _ string, _ LLMOptions) (string, error) {
	return s.resp, s.err
}

func (s *stubEvalLLM) CompleteWithStructuredOutput(_ context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.structuredCalls++
	return json.RawMessage(`{}`), s.err
}

func (s *stubEvalLLM) CompleteWithLogprobs(_ context.Context, _ string, _ int) (string, [][]LogprobEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logprobsCalls++
	return s.resp, s.logprobs, s.err
}

func (s *stubEvalLLM) logprobsCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logprobsCalls
}

// makeEvalRunResult 构造 eval.run 工具返回的 JSON。
func makeEvalRunResult(ranked []string, latencyMS int, cost, ctr float64) json.RawMessage {
	r := evalRunResult{
		Ranked:    ranked,
		LatencyMS: latencyMS,
		Cost:      cost,
		CTR:       ctr,
	}
	data, _ := json.Marshal(r)
	return data
}

// makeEvalCases 构造 n 个 EvalCase。
func makeEvalCases(n int) []EvalCase {
	cases := make([]EvalCase, n)
	for i := range cases {
		cases[i] = EvalCase{
			ID:       "case-" + itoa(i),
			Query:    "query-" + itoa(i),
			Expected: []string{"art-" + itoa(i), "art-common"},
			Surface:  "default",
		}
	}
	return cases
}

// ============================================================================
// 测试用例 — Name 与接口
// ============================================================================

// TestEvalAgent_Name 验证 Name 返回 "eval"。
func TestEvalAgent_Name(t *testing.T) {
	a := NewEvalAgent(nil, nil, domain.AgentOptions{})
	if a.Name() != "eval" {
		t.Errorf("Name = %q, 期望 eval", a.Name())
	}
}

// TestEvalAgent_ImplementsDomainAgent 验证 EvalAgent 实现 domain.Agent 接口。
func TestEvalAgent_ImplementsDomainAgent(t *testing.T) {
	var _ domain.Agent = (*EvalAgent)(nil)
	var _ domain.Agent = NewEvalAgent(nil, nil, domain.AgentOptions{})
}

// ============================================================================
// 测试用例 — 评估执行
// ============================================================================

// TestEvalAgent_Run_Basic 验证基本评估流程：tools.Invoke eval.run + metrics 聚合。
func TestEvalAgent_Run_Basic(t *testing.T) {
	tool := &stubTool{
		resp: makeEvalRunResult([]string{"art-0", "art-common", "other"}, 50, 0.001, 0.1),
	}
	llm := &stubEvalLLM{}
	a := NewEvalAgent(llm, tool, domain.AgentOptions{})

	cases := makeEvalCases(2)
	cfg := EvalConfig{
		Metrics:        []string{"ndcg@10", "precision@10"},
		Rubric:         "", // 不启用 LLM Judge
		DriftThreshold: 0.1,
	}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State: map[string]any{
			"eval_cases":  cases,
			"eval_config": cfg,
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}

	report, ok := out.Result.(EvalReport)
	if !ok {
		t.Fatalf("Result 类型不是 EvalReport: %T", out.Result)
	}
	if report.Cases != 2 {
		t.Errorf("Cases = %d, 期望 2", report.Cases)
	}
	if report.Passed != 2 {
		t.Errorf("Passed = %d, 期望 2（NDCG>0）", report.Passed)
	}
	// tools.Invoke 应被调用 2 次（每 case 1 次）。
	if tool.invokeCount() != 2 {
		t.Errorf("期望 tools.Invoke 调用 2 次, 实际 %d 次", tool.invokeCount())
	}
	// 验证 metrics 存在。
	if _, ok := report.Metrics["ndcg@10"]; !ok {
		t.Errorf("metrics 缺少 ndcg@10, 实际 %v", report.Metrics)
	}
	if _, ok := report.Metrics["latency"]; !ok {
		t.Errorf("metrics 缺少 latency, 实际 %v", report.Metrics)
	}
	// trace 应包含 eval.run 与 eval.drift（未启用 LLM Judge 所以无 eval.judge）。
	hasRun := false
	hasDrift := false
	for _, tr := range out.Trace {
		if tr == "eval.run" {
			hasRun = true
		}
		if tr == "eval.drift" {
			hasDrift = true
		}
	}
	if !hasRun {
		t.Errorf("trace 应包含 eval.run, 实际 %v", out.Trace)
	}
	if !hasDrift {
		t.Errorf("trace 应包含 eval.drift, 实际 %v", out.Trace)
	}
}

// TestEvalAgent_Run_LLMJudge 验证 LLM Judge 触发 CompleteWithLogprobs。
func TestEvalAgent_Run_LLMJudge(t *testing.T) {
	tool := &stubTool{
		resp: makeEvalRunResult([]string{"art-0"}, 50, 0.001, 0.1),
	}
	llm := &stubEvalLLM{resp: "0.85"}
	a := NewEvalAgent(llm, tool, domain.AgentOptions{})

	cases := makeEvalCases(1)
	cfg := EvalConfig{
		Rubric:         "quality rubric",
		DriftThreshold: 0.1,
	}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State: map[string]any{
			"eval_cases":  cases,
			"eval_config": cfg,
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	report := out.Result.(EvalReport)
	if report.LLMJudgeScore != 0.85 {
		t.Errorf("LLMJudgeScore = %v, 期望 0.85", report.LLMJudgeScore)
	}
	if llm.logprobsCallCount() != 1 {
		t.Errorf("期望 CompleteWithLogprobs 调用 1 次, 实际 %d 次", llm.logprobsCallCount())
	}
	// trace 应包含 eval.judge。
	hasJudge := false
	for _, tr := range out.Trace {
		if tr == "eval.judge" {
			hasJudge = true
		}
	}
	if !hasJudge {
		t.Errorf("trace 应包含 eval.judge, 实际 %v", out.Trace)
	}
}

// TestEvalAgent_Run_NilTools 验证 tools 为 nil 时不 panic。
func TestEvalAgent_Run_NilTools(t *testing.T) {
	a := NewEvalAgent(nil, nil, domain.AgentOptions{})

	cases := makeEvalCases(2)
	cfg := EvalConfig{DriftThreshold: 0.1}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State: map[string]any{
			"eval_cases":  cases,
			"eval_config": cfg,
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	report := out.Result.(EvalReport)
	if report.Cases != 2 {
		t.Errorf("Cases = %d, 期望 2", report.Cases)
	}
	if report.Passed != 0 {
		t.Errorf("nil tools 时 Passed 应为 0, 实际 %d", report.Passed)
	}
}

// TestEvalAgent_Run_NoCases 验证无用例时返回空报告。
func TestEvalAgent_Run_NoCases(t *testing.T) {
	tool := &stubTool{}
	a := NewEvalAgent(nil, tool, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State: map[string]any{
			"eval_cases":  []EvalCase{},
			"eval_config": EvalConfig{},
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	report := out.Result.(EvalReport)
	if report.Cases != 0 {
		t.Errorf("Cases = %d, 期望 0", report.Cases)
	}
	if tool.invokeCount() != 0 {
		t.Errorf("无用例时不应调用 tools, 实际 %d 次", tool.invokeCount())
	}
}

// TestEvalAgent_Run_SampleSize 验证 SampleSize 采样。
func TestEvalAgent_Run_SampleSize(t *testing.T) {
	tool := &stubTool{
		resp: makeEvalRunResult([]string{"art-0"}, 50, 0.001, 0.1),
	}
	a := NewEvalAgent(nil, tool, domain.AgentOptions{})

	cases := makeEvalCases(10)
	cfg := EvalConfig{
		SampleSize:     3,
		DriftThreshold: 0.1,
	}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State: map[string]any{
			"eval_cases":  cases,
			"eval_config": cfg,
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	report := out.Result.(EvalReport)
	if report.Cases != 3 {
		t.Errorf("SampleSize=3 时 Cases = %d, 期望 3", report.Cases)
	}
	if tool.invokeCount() != 3 {
		t.Errorf("SampleSize=3 时应调用 tools 3 次, 实际 %d 次", tool.invokeCount())
	}
}

// ============================================================================
// 测试用例 — 漂移检测
// ============================================================================

// TestEvalAgent_Run_DriftDetected 验证漂移检测：metrics 下降 > 阈值标记 drift。
func TestEvalAgent_Run_DriftDetected(t *testing.T) {
	tool := &stubTool{
		resp: makeEvalRunResult([]string{"art-0"}, 120, 0.001, 0.05), // latency 较高, ctr 较低
	}
	a := NewEvalAgent(nil, tool, domain.AgentOptions{})

	cases := makeEvalCases(1)
	cfg := EvalConfig{DriftThreshold: 0.1}
	// 基线：ndcg@10=0.9, latency=50（当前 latency=120 上升 140% → drift）。
	baseline := map[string]float64{
		"ndcg@10": 1.0, // 当前 ~1.0, 下降 < 10% → 不 drift
		"latency": 50,  // 当前 120, 上升 140% → drift
	}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State: map[string]any{
			"eval_cases":    cases,
			"eval_config":   cfg,
			"eval_baseline": baseline,
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	report := out.Result.(EvalReport)
	if !report.DriftDetected {
		t.Errorf("latency 上升 140%% 应检测到漂移")
	}
	// driftMetrics 应包含 latency。
	found := false
	for _, m := range report.DriftMetrics {
		if m == "latency" {
			found = true
		}
	}
	if !found {
		t.Errorf("DriftMetrics 应包含 latency, 实际 %v", report.DriftMetrics)
	}
}

// TestDetectDrift_GoodMetricDecrease 验证高值为优指标下降超阈值 → drift。
func TestDetectDrift_GoodMetricDecrease(t *testing.T) {
	current := map[string]float64{"ndcg@10": 0.7}
	baseline := map[string]float64{"ndcg@10": 0.9}
	drifted, metrics := detectDrift(current, baseline, 0.1)
	if !drifted {
		t.Errorf("ndcg 下降 22%% 应检测到漂移")
	}
	if len(metrics) != 1 || metrics[0] != "ndcg@10" {
		t.Errorf("DriftMetrics = %v, 期望 [ndcg@10]", metrics)
	}
}

// TestDetectDrift_GoodMetricNoDrift 验证高值为优指标下降未超阈值 → 不 drift。
func TestDetectDrift_GoodMetricNoDrift(t *testing.T) {
	current := map[string]float64{"ndcg@10": 0.85}
	baseline := map[string]float64{"ndcg@10": 0.9}
	// 下降 5.6% < 10% → 不 drift。
	drifted, _ := detectDrift(current, baseline, 0.1)
	if drifted {
		t.Errorf("ndcg 下降 5.6%% 不应检测到漂移")
	}
}

// TestDetectDrift_BadMetricIncrease 验证低值为优指标上升超阈值 → drift。
func TestDetectDrift_BadMetricIncrease(t *testing.T) {
	current := map[string]float64{"latency": 150}
	baseline := map[string]float64{"latency": 100}
	drifted, metrics := detectDrift(current, baseline, 0.1)
	if !drifted {
		t.Errorf("latency 上升 50%% 应检测到漂移")
	}
	if len(metrics) != 1 || metrics[0] != "latency" {
		t.Errorf("DriftMetrics = %v, 期望 [latency]", metrics)
	}
}

// TestDetectDrift_NoBaseline 验证无基线时不检测漂移。
func TestDetectDrift_NoBaseline(t *testing.T) {
	current := map[string]float64{"ndcg@10": 0.5}
	drifted, metrics := detectDrift(current, nil, 0.1)
	if drifted {
		t.Errorf("无基线时不应检测到漂移")
	}
	if metrics != nil {
		t.Errorf("无基线时 DriftMetrics 应为 nil, 实际 %v", metrics)
	}
}

// TestDetectDrift_ZeroBaseline 验证基线为 0 时跳过该指标。
func TestDetectDrift_ZeroBaseline(t *testing.T) {
	current := map[string]float64{"ndcg@10": 0.5}
	baseline := map[string]float64{"ndcg@10": 0}
	drifted, _ := detectDrift(current, baseline, 0.1)
	if drifted {
		t.Errorf("基线为 0 时应跳过该指标, 不检测漂移")
	}
}

// ============================================================================
// 测试用例 — NDCG / Precision / Recall 计算
// ============================================================================

// TestNDCGAtK_Perfect 验证完美排序 NDCG=1.0。
func TestNDCGAtK_Perfect(t *testing.T) {
	ranked := []string{"a", "b", "c"}
	expected := []string{"a"}
	got := NDCGAtK(ranked, expected, 3)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("NDCG = %v, 期望 1.0", got)
	}
}

// TestNDCGAtK_Partial 验证部分命中 NDCG 正确。
func TestNDCGAtK_Partial(t *testing.T) {
	// ranked=[b, a, c], expected=[a]
	// DCG = 1/log2(3) ≈ 0.6309
	// IDCG = 1/log2(2) = 1.0
	// NDCG ≈ 0.6309
	ranked := []string{"b", "a", "c"}
	expected := []string{"a"}
	got := NDCGAtK(ranked, expected, 3)
	want := 1.0 / math.Log2(3)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("NDCG = %v, 期望 %v", got, want)
	}
}

// TestNDCGAtK_NoHit 验证无命中 NDCG=0。
func TestNDCGAtK_NoHit(t *testing.T) {
	ranked := []string{"b", "c", "d"}
	expected := []string{"a"}
	got := NDCGAtK(ranked, expected, 3)
	if got != 0 {
		t.Errorf("NDCG = %v, 期望 0", got)
	}
}

// TestNDCGAtK_MultiExpected 验证多期望命中。
func TestNDCGAtK_MultiExpected(t *testing.T) {
	// ranked=[a, b, c], expected=[a, b]
	// DCG = 1/log2(2) + 1/log2(3) = 1.0 + 0.6309 = 1.6309
	// IDCG = 1/log2(2) + 1/log2(3) = 1.6309
	// NDCG = 1.0
	ranked := []string{"a", "b", "c"}
	expected := []string{"a", "b"}
	got := NDCGAtK(ranked, expected, 3)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("NDCG = %v, 期望 1.0", got)
	}
}

// TestNDCGAtK_KTruncation 验证 K 截断。
func TestNDCGAtK_KTruncation(t *testing.T) {
	// ranked=[b, c, a], expected=[a], k=2
	// a 在位置 2（i=2）>= k=2 → 不计入 DCG
	// DCG = 0, NDCG = 0
	ranked := []string{"b", "c", "a"}
	expected := []string{"a"}
	got := NDCGAtK(ranked, expected, 2)
	if got != 0 {
		t.Errorf("NDCG = %v, 期望 0（K 截断）", got)
	}
}

// TestNDCGAtK_Empty 验证空输入 NDCG=0。
func TestNDCGAtK_Empty(t *testing.T) {
	if got := NDCGAtK(nil, nil, 3); got != 0 {
		t.Errorf("空输入 NDCG = %v, 期望 0", got)
	}
	if got := NDCGAtK([]string{"a"}, nil, 3); got != 0 {
		t.Errorf("expected 为空 NDCG = %v, 期望 0", got)
	}
	if got := NDCGAtK([]string{"a"}, []string{"a"}, 0); got != 0 {
		t.Errorf("k=0 NDCG = %v, 期望 0", got)
	}
}

// TestNDCGAtK_MoreExpectedThanK 验证 expected 数量 > K。
func TestNDCGAtK_MoreExpectedThanK(t *testing.T) {
	// ranked=[a, b], expected=[a, b, c, d], k=2
	// DCG = 1/log2(2) + 1/log2(3) = 1.6309
	// IDCG (n=min(4,2)=2) = 1/log2(2) + 1/log2(3) = 1.6309
	// NDCG = 1.0
	ranked := []string{"a", "b"}
	expected := []string{"a", "b", "c", "d"}
	got := NDCGAtK(ranked, expected, 2)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("NDCG = %v, 期望 1.0", got)
	}
}

// TestPrecisionAtK 验证 Precision@K 计算。
func TestPrecisionAtK(t *testing.T) {
	cases := []struct {
		name     string
		ranked   []string
		expected []string
		k        int
		want     float64
	}{
		{"2 hits in 4", []string{"a", "b", "c", "d"}, []string{"a", "b"}, 4, 0.5},
		{"1 hit in 3", []string{"a", "x", "y"}, []string{"a"}, 3, 1.0 / 3.0},
		{"0 hits", []string{"x", "y"}, []string{"a"}, 2, 0.0},
		{"k=0", []string{"a"}, []string{"a"}, 0, 0.0},
		{"k truncation", []string{"a", "b", "c"}, []string{"c"}, 2, 0.0},
	}
	for _, c := range cases {
		got := PrecisionAtK(c.ranked, c.expected, c.k)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: Precision = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

// TestRecallAtK 验证 Recall@K 计算。
func TestRecallAtK(t *testing.T) {
	cases := []struct {
		name     string
		ranked   []string
		expected []string
		k        int
		want     float64
	}{
		{"1 of 2", []string{"a", "x"}, []string{"a", "b"}, 2, 0.5},
		{"2 of 2", []string{"a", "b"}, []string{"a", "b"}, 2, 1.0},
		{"0 of 1", []string{"x", "y"}, []string{"a"}, 2, 0.0},
		{"k=0", []string{"a"}, []string{"a"}, 0, 0.0},
		{"k truncation", []string{"a", "b", "c"}, []string{"c"}, 2, 0.0},
		{"empty expected", []string{"a"}, nil, 2, 0.0},
	}
	for _, c := range cases {
		got := RecallAtK(c.ranked, c.expected, c.k)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: Recall = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

// ============================================================================
// 测试用例 — 辅助函数
// ============================================================================

// TestParseScoreFromText 验证从文本解析分数。
func TestParseScoreFromText(t *testing.T) {
	cases := []struct {
		text string
		want float64
	}{
		{"0.85", 0.85},
		{"0.5", 0.5},
		{"1", 1.0},
		{"score: 0.75", 0.75},
		{"85", 0.85}, // 0-100 分制
		{"", -1},
		{"no number here", -1},
	}
	for _, c := range cases {
		got := parseScoreFromText(c.text)
		if c.want < 0 {
			if got >= 0 {
				t.Errorf("parseScoreFromText(%q) = %v, 期望 < 0", c.text, got)
			}
			continue
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("parseScoreFromText(%q) = %v, 期望 %v", c.text, got, c.want)
		}
	}
}

// TestAvgLogprobConfidence 验证 logprobs 置信度计算。
func TestAvgLogprobConfidence(t *testing.T) {
	// logprob=0 → exp(0)=1.0
	logprobs := [][]LogprobEntry{
		{{Token: "a", Logprob: 0}},
		{{Token: "b", Logprob: 0}},
	}
	got := avgLogprobConfidence(logprobs)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("avgLogprobConfidence = %v, 期望 1.0", got)
	}

	// 空 logprobs → 0。
	if got := avgLogprobConfidence(nil); got != 0 {
		t.Errorf("nil logprobs 应返回 0, 实际 %v", got)
	}

	// logprob=-0.6931 → exp≈0.5
	lp := math.Log(0.5)
	logprobs2 := [][]LogprobEntry{
		{{Token: "a", Logprob: lp}, {Token: "b", Logprob: -1.0}},
	}
	got = avgLogprobConfidence(logprobs2)
	if math.Abs(got-0.5) > 1e-3 {
		t.Errorf("avgLogprobConfidence = %v, 期望 ~0.5", got)
	}
}

// TestExtractEvalCases 验证从 []any 提取 EvalCase。
func TestExtractEvalCases(t *testing.T) {
	// []EvalCase 直接传入。
	cases := []EvalCase{{ID: "c1", Query: "q1", Expected: []string{"a"}}}
	got := extractEvalCases(cases)
	if len(got) != 1 || got[0].ID != "c1" {
		t.Errorf("extractEvalCases([]EvalCase) = %+v, 不匹配", got)
	}

	// []any 传入（每项为 map[string]any）。
	anyCases := []any{
		map[string]any{
			"id":       "c2",
			"query":    "q2",
			"expected": []any{"a", "b"},
			"surface":  "travel",
		},
	}
	got = extractEvalCases(anyCases)
	if len(got) != 1 || got[0].ID != "c2" {
		t.Errorf("extractEvalCases([]any) = %+v, 不匹配", got)
	}
	if len(got[0].Expected) != 2 || got[0].Expected[0] != "a" {
		t.Errorf("Expected = %v, 期望 [a b]", got[0].Expected)
	}

	// nil 传入。
	if got := extractEvalCases(nil); got != nil {
		t.Errorf("extractEvalCases(nil) 应返回 nil, 实际 %+v", got)
	}
}

// TestExtractEvalConfig 验证从 map 提取 EvalConfig。
func TestExtractEvalConfig(t *testing.T) {
	// EvalConfig 直接传入。
	cfg := EvalConfig{Rubric: "test", DriftThreshold: 0.2, SampleSize: 5}
	got := extractEvalConfig(cfg)
	if got.Rubric != "test" || got.DriftThreshold != 0.2 || got.SampleSize != 5 {
		t.Errorf("extractEvalConfig(EvalConfig) = %+v, 不匹配", got)
	}

	// map[string]any 传入。
	m := map[string]any{
		"rubric":          "map rubric",
		"drift_threshold": 0.15,
		"sample_size":     10,
		"metrics":         []any{"ndcg@10", "ctr"},
	}
	got = extractEvalConfig(m)
	if got.Rubric != "map rubric" {
		t.Errorf("Rubric = %s, 期望 map rubric", got.Rubric)
	}
	if got.DriftThreshold != 0.15 {
		t.Errorf("DriftThreshold = %v, 期望 0.15", got.DriftThreshold)
	}
	if got.SampleSize != 10 {
		t.Errorf("SampleSize = %d, 期望 10", got.SampleSize)
	}
	if len(got.Metrics) != 2 {
		t.Errorf("Metrics 长度 = %d, 期望 2", len(got.Metrics))
	}

	// nil 传入。
	got = extractEvalConfig(nil)
	if got.DriftThreshold != 0 {
		t.Errorf("nil 时 DriftThreshold 应为 0, 实际 %v", got.DriftThreshold)
	}
}

// TestExtractBaseline 验证从 map 提取基线。
func TestExtractBaseline(t *testing.T) {
	// map[string]float64 直接传入。
	m1 := map[string]float64{"ndcg@10": 0.8}
	got := extractBaseline(m1)
	if got["ndcg@10"] != 0.8 {
		t.Errorf("extractBaseline(map[string]float64) = %v, 不匹配", got)
	}

	// map[string]any 传入。
	m2 := map[string]any{"latency": 100.0}
	got = extractBaseline(m2)
	if got["latency"] != 100.0 {
		t.Errorf("extractBaseline(map[string]any) = %v, 不匹配", got)
	}

	// nil 传入。
	if got := extractBaseline(nil); got != nil {
		t.Errorf("extractBaseline(nil) 应返回 nil, 实际 %v", got)
	}
}
