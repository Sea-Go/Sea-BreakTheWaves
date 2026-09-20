// Package agent quality_test.go — QualityAgent 单元测试（Task 11.6）。
//
// 该文件用 stdlib 手写 stub（不引入 testify）覆盖 QualityAgent 的：
//   - 正常路径（quality.score + quality.judge + best_of_n 三步）
//   - 阈值过滤（高分通过、低分过滤）
//   - 空候选输入
//   - tools 为 nil 降级 LLM logprobs 评分
//   - quality.score 失败兜底用候选原 Score
//   - 从 config 读取阈值（RecommendConfig / map 两种形态）
//   - Best-of-N 选择提升 top 候选分数
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// stub 实现
// ----------------------------------------------------------------------------

// stubLLMForQuality 测试用 LLMClient stub（QualityAgent 专用）。
type stubLLMForQuality struct {
	logprobsOut string
	logprobs    [][]LogprobEntry
	logprobsErr error
	calls       int
}

func (s *stubLLMForQuality) Complete(_ context.Context, _ string, _ LLMOptions) (string, error) {
	return "", nil
}

func (s *stubLLMForQuality) CompleteWithStructuredOutput(_ context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
	return nil, errors.New("not used")
}

func (s *stubLLMForQuality) CompleteWithLogprobs(_ context.Context, _ string, _ int) (string, [][]LogprobEntry, error) {
	s.calls++
	return s.logprobsOut, s.logprobs, s.logprobsErr
}

// stubToolsForQuality 测试用 ToolExecutor stub（QualityAgent 专用）。
type stubToolsForQuality struct {
	handlers map[string]json.RawMessage
	errs     map[string]error
	calls    map[string]int
}

func newStubToolsForQuality() *stubToolsForQuality {
	return &stubToolsForQuality{
		handlers: make(map[string]json.RawMessage),
		errs:     make(map[string]error),
		calls:    make(map[string]int),
	}
}

func (s *stubToolsForQuality) Invoke(_ context.Context, name string, _ json.RawMessage) (json.RawMessage, error) {
	s.calls[name]++
	if err, ok := s.errs[name]; ok {
		return nil, err
	}
	if out, ok := s.handlers[name]; ok {
		return out, nil
	}
	return json.RawMessage("null"), nil
}

func (s *stubToolsForQuality) List(_ context.Context) ([]string, error) {
	return []string{"quality.score", "quality.judge"}, nil
}

// wrapScores 包装分数 map 为 {"scores": {...}} JSON。
func wrapScores(scores map[string]float64) json.RawMessage {
	out, _ := json.Marshal(map[string]any{"scores": scores})
	return out
}

// makeRerankItems 构造测试重排候选列表。
func makeRerankItems() []domain.Candidate {
	return []domain.Candidate{
		{ArticleID: "a1", Score: 0.9},
		{ArticleID: "a2", Score: 0.4},
		{ArticleID: "a3", Score: 0.7},
	}
}

// ----------------------------------------------------------------------------
// 测试用例
// ----------------------------------------------------------------------------

// TestQualityAgent_Name 验证 Name 返回 "quality"。
func TestQualityAgent_Name(t *testing.T) {
	a := NewQualityAgent(nil, nil, domain.AgentOptions{})
	if a.Name() != "quality" {
		t.Errorf("Name = %q, 期望 quality", a.Name())
	}
}

// TestQualityAgent_Run_Normal 验证正常路径：score + judge + best_of_n 三步。
func TestQualityAgent_Run_Normal(t *testing.T) {
	llm := &stubLLMForQuality{
		logprobs: [][]LogprobEntry{
			{{Token: "yes", Logprob: -0.1}},
		},
	}
	tools := newStubToolsForQuality()
	tools.handlers["quality.score"] = wrapScores(map[string]float64{
		"a1": 0.9, "a2": 0.4, "a3": 0.7,
	})
	tools.handlers["quality.judge"] = wrapScores(map[string]float64{
		"a1": 0.95, "a2": 0.45, "a3": 0.75,
	})
	a := NewQualityAgent(llm, tools, domain.AgentOptions{MaxToolCalls: 3})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"rerank_items": makeRerankItems()},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if tools.calls["quality.score"] != 1 {
		t.Errorf("期望 quality.score 调用 1 次, 实际 %d", tools.calls["quality.score"])
	}
	if tools.calls["quality.judge"] != 1 {
		t.Errorf("期望 quality.judge 调用 1 次, 实际 %d", tools.calls["quality.judge"])
	}
	// Trace 应包含三步。
	if len(got.Trace) != 3 {
		t.Errorf("期望 3 步 trace, 实际 %d", len(got.Trace))
	}
	if got.Trace[0] != "quality.score" || got.Trace[1] != "quality.judge" || got.Trace[2] != "quality.best_of_n" {
		t.Errorf("Trace = %v", got.Trace)
	}
	// 默认阈值 0.6 → 应过滤掉 a2（0.45）。
	filtered, ok := got.Result.([]domain.Candidate)
	if !ok {
		t.Fatalf("Result 类型断言失败: %T", got.Result)
	}
	if len(filtered) != 2 {
		t.Errorf("期望 2 个通过阈值候选, 实际 %d", len(filtered))
	}
	for _, c := range filtered {
		if c.ArticleID == "a2" {
			t.Errorf("a2 应被过滤（分数 0.45 < 0.6）")
		}
	}
	// State 字段。
	if _, ok := got.State["quality_scores"]; !ok {
		t.Errorf("State 缺少 quality_scores")
	}
	if _, ok := got.State["quality_filtered"]; !ok {
		t.Errorf("State 缺少 quality_filtered")
	}
}

// TestQualityAgent_Run_ThresholdFilter 验证阈值过滤：高分通过、低分过滤。
func TestQualityAgent_Run_ThresholdFilter(t *testing.T) {
	llm := &stubLLMForQuality{}
	tools := newStubToolsForQuality()
	tools.handlers["quality.score"] = wrapScores(map[string]float64{
		"a1": 0.9, "a2": 0.3, "a3": 0.6,
	})
	a := NewQualityAgent(llm, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{
			"rerank_items": makeRerankItems(),
			"config": domain.RecommendConfig{
				QualityThreshold: 0.6,
			},
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	filtered := got.Result.([]domain.Candidate)
	// 0.9 通过, 0.3 过滤, 0.6 通过（>=）。
	if len(filtered) != 2 {
		t.Errorf("期望 2 个通过阈值候选, 实际 %d", len(filtered))
	}
	ids := map[string]bool{}
	for _, c := range filtered {
		ids[c.ArticleID] = true
	}
	if !ids["a1"] || !ids["a3"] || ids["a2"] {
		t.Errorf("过滤结果错误: %v", ids)
	}
}

// TestQualityAgent_Run_EmptyCandidates 验证空候选输入正常返回空结果。
func TestQualityAgent_Run_EmptyCandidates(t *testing.T) {
	llm := &stubLLMForQuality{}
	tools := newStubToolsForQuality()
	a := NewQualityAgent(llm, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"rerank_items": []domain.Candidate{}},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	filtered, ok := got.Result.([]domain.Candidate)
	if !ok {
		t.Fatalf("Result 类型断言失败: %T", got.Result)
	}
	if len(filtered) != 0 {
		t.Errorf("期望 0 个候选, 实际 %d", len(filtered))
	}
	// quality.score 不应被调用（空候选直接返回空 map）。
	if tools.calls["quality.score"] != 0 {
		t.Errorf("空候选不应调 quality.score, 实际 %d 次", tools.calls["quality.score"])
	}
}

// TestQualityAgent_Run_NilTools 验证 tools 为 nil 时降级 LLM logprobs 评分。
func TestQualityAgent_Run_NilTools(t *testing.T) {
	llm := &stubLLMForQuality{
		logprobs: [][]LogprobEntry{
			{{Token: "yes", Logprob: -0.2}},
		},
	}
	a := NewQualityAgent(llm, nil, domain.AgentOptions{MaxToolCalls: 2})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"rerank_items": makeRerankItems()},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	// LLM 应被调用：3 候选 × 1 次/候选 = 3 次（scoreWithLLM）+ 2 次（bestOfN top-2）= 5 次。
	if llm.calls == 0 {
		t.Errorf("tools 为 nil 时应调用 LLM logprobs, 实际 %d 次", llm.calls)
	}
	// Trace 应包含 quality.score + quality.best_of_n（无 quality.judge，因 tools 为 nil）。
	if len(got.Trace) != 2 {
		t.Errorf("期望 2 步 trace, 实际 %d", len(got.Trace))
	}
	if got.Trace[0] != "quality.score" || got.Trace[1] != "quality.best_of_n" {
		t.Errorf("Trace = %v", got.Trace)
	}
}

// TestQualityAgent_Run_ScoreFailFallback 验证 quality.score 失败兜底用候选原 Score。
func TestQualityAgent_Run_ScoreFailFallback(t *testing.T) {
	llm := &stubLLMForQuality{
		logprobs: [][]LogprobEntry{{{Token: "yes", Logprob: -0.1}}},
	}
	tools := newStubToolsForQuality()
	tools.errs["quality.score"] = errors.New("score down")
	a := NewQualityAgent(llm, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"rerank_items": makeRerankItems()},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	scores := got.State["quality_scores"].(map[string]float64)
	// 兜底应使用候选原 Score：a1=0.9, a2=0.4, a3=0.7。
	if scores["a1"] != 0.9 {
		t.Errorf("兜底分数 a1 = %v, 期望 0.9", scores["a1"])
	}
	if scores["a2"] != 0.4 {
		t.Errorf("兜底分数 a2 = %v, 期望 0.4", scores["a2"])
	}
}

// TestQualityAgent_Run_ConfigMapThreshold 验证从 map 形态 config 读取阈值。
func TestQualityAgent_Run_ConfigMapThreshold(t *testing.T) {
	llm := &stubLLMForQuality{}
	tools := newStubToolsForQuality()
	tools.handlers["quality.score"] = wrapScores(map[string]float64{
		"a1": 0.9, "a2": 0.4, "a3": 0.7,
	})
	a := NewQualityAgent(llm, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{
			"rerank_items": makeRerankItems(),
			"config": map[string]any{
				"QualityThreshold": 0.8,
			},
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	filtered := got.Result.([]domain.Candidate)
	// 阈值 0.8 → 仅 a1（0.9）通过。
	if len(filtered) != 1 || filtered[0].ArticleID != "a1" {
		t.Errorf("阈值 0.8 应仅 a1 通过, 实际: %v", filtered)
	}
}

// TestQualityAgent_Run_DefaultThreshold 验证无 config 时使用默认阈值 0.6。
func TestQualityAgent_Run_DefaultThreshold(t *testing.T) {
	llm := &stubLLMForQuality{}
	tools := newStubToolsForQuality()
	tools.handlers["quality.score"] = wrapScores(map[string]float64{
		"a1": 0.9, "a2": 0.5, "a3": 0.7,
	})
	a := NewQualityAgent(llm, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"rerank_items": makeRerankItems()},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	filtered := got.Result.([]domain.Candidate)
	// 默认阈值 0.6 → a1(0.9) + a3(0.7) 通过, a2(0.5) 过滤。
	if len(filtered) != 2 {
		t.Errorf("期望 2 个通过阈值候选, 实际 %d", len(filtered))
	}
}

// TestQualityAgent_Run_BestOfN 验证 Best-of-N 选择提升 top 候选分数。
func TestQualityAgent_Run_BestOfN(t *testing.T) {
	// LLM 对首位候选返回 "yes"（高概率），其余返回 "no"。
	llm := &stubLLMForQuality{
		logprobs: [][]LogprobEntry{
			{{Token: "yes", Logprob: -0.05}},
		},
	}
	tools := newStubToolsForQuality()
	// 故意给 a2 低分（< 0.6），但 Best-of-N 应选中 a1（top-1 by score）。
	tools.handlers["quality.score"] = wrapScores(map[string]float64{
		"a1": 0.9, "a2": 0.4, "a3": 0.7,
	})
	a := NewQualityAgent(llm, tools, domain.AgentOptions{MaxToolCalls: 3})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"rerank_items": makeRerankItems()},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	scores := got.State["quality_scores"].(map[string]float64)
	// a1 应被 Best-of-N 选中并提升到至少阈值 0.6。
	if scores["a1"] < 0.6 {
		t.Errorf("Best-of-N 应提升 a1 分数到 ≥0.6, 实际 %v", scores["a1"])
	}
}

// TestQualityAgent_Run_NoLLM 验证 LLM 未注入时跳过 best_of_n 与 judge。
func TestQualityAgent_Run_NoLLM(t *testing.T) {
	tools := newStubToolsForQuality()
	tools.handlers["quality.score"] = wrapScores(map[string]float64{
		"a1": 0.9, "a2": 0.4, "a3": 0.7,
	})
	a := NewQualityAgent(nil, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"rerank_items": makeRerankItems()},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	// LLM 未注入 → 不调 quality.judge（需 LLM）, 不调 best_of_n（需 LLM）。
	if tools.calls["quality.judge"] != 0 {
		t.Errorf("LLM 未注入时不应调 quality.judge, 实际 %d 次", tools.calls["quality.judge"])
	}
	// Trace 仅含 quality.score。
	if len(got.Trace) != 1 || got.Trace[0] != "quality.score" {
		t.Errorf("Trace = %v, 期望 [quality.score]", got.Trace)
	}
}

// TestQualityAgent_Run_JudgeFail 验证 quality.judge 失败仍返回评分结果。
func TestQualityAgent_Run_JudgeFail(t *testing.T) {
	llm := &stubLLMForQuality{
		logprobs: [][]LogprobEntry{{{Token: "yes", Logprob: -0.1}}},
	}
	tools := newStubToolsForQuality()
	tools.handlers["quality.score"] = wrapScores(map[string]float64{
		"a1": 0.9, "a2": 0.4, "a3": 0.7,
	})
	tools.errs["quality.judge"] = errors.New("judge down")
	a := NewQualityAgent(llm, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"rerank_items": makeRerankItems()},
	})
	if err != nil {
		t.Fatalf("judge 失败不应返回错误, 实际 %v", err)
	}
	scores := got.State["quality_scores"].(map[string]float64)
	// judge 失败 → 保留 score 结果。
	if scores["a1"] != 0.9 {
		t.Errorf("judge 失败应保留 score 结果 a1=0.9, 实际 %v", scores["a1"])
	}
}

// TestExtractQualityThreshold 验证阈值提取支持多种 config 形态。
func TestExtractQualityThreshold(t *testing.T) {
	// 形态 1：domain.RecommendConfig
	t1 := extractQualityThreshold(domain.AgentInput{
		State: map[string]any{
			"config": domain.RecommendConfig{QualityThreshold: 0.8},
		},
	})
	if t1 != 0.8 {
		t.Errorf("形态 1 阈值 = %v, 期望 0.8", t1)
	}

	// 形态 2：map[string]any{"QualityThreshold": 0.7}
	t2 := extractQualityThreshold(domain.AgentInput{
		State: map[string]any{
			"config": map[string]any{"QualityThreshold": 0.7},
		},
	})
	if t2 != 0.7 {
		t.Errorf("形态 2 阈值 = %v, 期望 0.7", t2)
	}

	// 形态 3：map[string]any{"quality_threshold": 0.5}
	t3 := extractQualityThreshold(domain.AgentInput{
		State: map[string]any{
			"config": map[string]any{"quality_threshold": 0.5},
		},
	})
	if t3 != 0.5 {
		t.Errorf("形态 3 阈值 = %v, 期望 0.5", t3)
	}

	// 形态 4：无 config → 默认 0.6
	t4 := extractQualityThreshold(domain.AgentInput{State: map[string]any{}})
	if t4 != defaultQualityThreshold {
		t.Errorf("形态 4 阈值 = %v, 期望 %v", t4, defaultQualityThreshold)
	}

	// 形态 5：State 为 nil → 默认 0.6
	t5 := extractQualityThreshold(domain.AgentInput{})
	if t5 != defaultQualityThreshold {
		t.Errorf("形态 5 阈值 = %v, 期望 %v", t5, defaultQualityThreshold)
	}
}

// TestFilterByThreshold 验证阈值过滤保持原顺序。
func TestFilterByThreshold(t *testing.T) {
	cs := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
		{ArticleID: "a3"},
	}
	scores := map[string]float64{"a1": 0.9, "a2": 0.3, "a3": 0.7}
	out := filterByThreshold(cs, scores, 0.6)
	if len(out) != 2 {
		t.Fatalf("期望 2 个通过, 实际 %d", len(out))
	}
	if out[0].ArticleID != "a1" || out[1].ArticleID != "a3" {
		t.Errorf("过滤顺序错误: %v", out)
	}
}

// TestTopNByScore 验证按分数降序取 top-N。
func TestTopNByScore(t *testing.T) {
	cs := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
		{ArticleID: "a3"},
	}
	scores := map[string]float64{"a1": 0.5, "a2": 0.9, "a3": 0.7}
	top := topNByScore(cs, scores, 2)
	if len(top) != 2 {
		t.Fatalf("期望 2 个 top, 实际 %d", len(top))
	}
	if top[0].ArticleID != "a2" || top[1].ArticleID != "a3" {
		t.Errorf("top-N 顺序错误: %v", top)
	}
}

// TestNormalizeToken 验证 token 归一化。
func TestNormalizeToken(t *testing.T) {
	cases := []struct {
		in, out string
	}{
		{"yes", "yes"},
		{"Yes", "yes"},
		{"YES", "yes"},
		{" yes ", "yes"},
		{"Pass", "pass"},
		{"  Good  ", "good"},
	}
	for _, c := range cases {
		if got := normalizeToken(c.in); got != c.out {
			t.Errorf("normalizeToken(%q) = %q, 期望 %q", c.in, got, c.out)
		}
	}
}

// TestExpApprox 验证 expApprox 近似 exp(x)。
func TestExpApprox(t *testing.T) {
	// exp(0) = 1
	if r := expApprox(0); r != 1 {
		t.Errorf("expApprox(0) = %v, 期望 1", r)
	}
	// exp(-1) ≈ 0.368
	r := expApprox(-1)
	if r < 0.35 || r > 0.38 {
		t.Errorf("expApprox(-1) = %v, 期望 ≈0.368", r)
	}
	// exp(-10) ≈ 0.0000454
	r = expApprox(-10)
	if r < 0 || r > 0.001 {
		t.Errorf("expApprox(-10) = %v, 期望 ≈0", r)
	}
	// x >= 0 → 1
	if r := expApprox(0.5); r != 1 {
		t.Errorf("expApprox(0.5) = %v, 期望 1", r)
	}
}

// TestParseQualityScores 验证分数解析支持两种格式。
func TestParseQualityScores(t *testing.T) {
	// 格式 1：{"scores": {...}}
	s1, err := parseQualityScores(wrapScores(map[string]float64{"a1": 0.9}))
	if err != nil {
		t.Fatalf("格式 1 解析失败: %v", err)
	}
	if s1["a1"] != 0.9 {
		t.Errorf("格式 1 a1 = %v, 期望 0.9", s1["a1"])
	}

	// 格式 2：直接 {"a1": 0.9}
	raw := mustMarshal(map[string]float64{"a1": 0.9})
	s2, err := parseQualityScores(raw)
	if err != nil {
		t.Fatalf("格式 2 解析失败: %v", err)
	}
	if s2["a1"] != 0.9 {
		t.Errorf("格式 2 a1 = %v, 期望 0.9", s2["a1"])
	}

	// 格式 3：空输入 → 空 map
	s3, err := parseQualityScores(nil)
	if err != nil {
		t.Fatalf("格式 3 解析失败: %v", err)
	}
	if len(s3) != 0 {
		t.Errorf("格式 3 应返回空 map, 实际 %v", s3)
	}
}
