// Package agent rerank_test.go — RerankAgent 单元测试（Task 11.5）。
//
// 该文件用 stdlib 手写 stub（不引入 testify）覆盖 RerankAgent 的：
//   - 正常路径（self_rerank + llm rerank + ab_test 三工具调用）
//   - 空候选输入
//   - 自研 rerank 失败降级加权排序
//   - tools 为 nil 完全降级加权排序
//   - LLM 未注入时跳过 LLM rerank
//   - A/B 测试失败仍返回重排结果
//   - 候选顺序符合加权降序
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// stub 实现
// ----------------------------------------------------------------------------

// stubLLMForRerank 测试用 LLMClient stub（RerankAgent 专用）。
type stubLLMForRerank struct {
	completeOut string
	completeErr error
	calls       int
}

func (s *stubLLMForRerank) Complete(_ context.Context, _ string, _ LLMOptions) (string, error) {
	s.calls++
	return s.completeOut, s.completeErr
}

func (s *stubLLMForRerank) CompleteWithStructuredOutput(_ context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
	return nil, errors.New("not used")
}

func (s *stubLLMForRerank) CompleteWithLogprobs(_ context.Context, _ string, _ int) (string, [][]LogprobEntry, error) {
	return "", nil, errors.New("not used")
}

// stubToolsForRerank 测试用 ToolExecutor stub（RerankAgent 专用）。
// 通过 handler map 按工具名分发不同响应。
type stubToolsForRerank struct {
	handlers map[string]json.RawMessage
	errs     map[string]error
	calls    map[string]int
	order    []string
}

func newStubToolsForRerank() *stubToolsForRerank {
	return &stubToolsForRerank{
		handlers: make(map[string]json.RawMessage),
		errs:     make(map[string]error),
		calls:    make(map[string]int),
	}
}

func (s *stubToolsForRerank) Invoke(_ context.Context, name string, _ json.RawMessage) (json.RawMessage, error) {
	s.calls[name]++
	s.order = append(s.order, name)
	if err, ok := s.errs[name]; ok {
		return nil, err
	}
	if out, ok := s.handlers[name]; ok {
		return out, nil
	}
	return json.RawMessage("null"), nil
}

func (s *stubToolsForRerank) List(_ context.Context) ([]string, error) {
	return []string{"rerank.self_rerank", "rerank.llm", "rerank.ab_test"}, nil
}

// wrapCandidates 包装 Candidate 列表为 {"candidates":[...]} JSON。
func wrapCandidates(cs []domain.Candidate) json.RawMessage {
	out, _ := json.Marshal(map[string]any{"candidates": cs})
	return out
}

// makeCandidates 构造测试候选列表。
func makeCandidates() []domain.Candidate {
	return []domain.Candidate{
		{ArticleID: "a1", Score: 0.5, Scores: map[string]float64{"cf": 0.3, "graph": 0.2}},
		{ArticleID: "a2", Score: 0.9, Scores: map[string]float64{"cf": 0.6, "graph": 0.4}},
		{ArticleID: "a3", Score: 0.7, Scores: map[string]float64{"cf": 0.5, "graph": 0.1}},
	}
}

// ----------------------------------------------------------------------------
// 测试用例
// ----------------------------------------------------------------------------

// TestRerankAgent_Name 验证 Name 返回 "rerank"。
func TestRerankAgent_Name(t *testing.T) {
	a := NewRerankAgent(nil, nil, domain.AgentOptions{})
	if a.Name() != "rerank" {
		t.Errorf("Name = %q, 期望 rerank", a.Name())
	}
}

// TestRerankAgent_Run_Normal 验证正常路径：self_rerank + llm + ab_test 三工具调用。
func TestRerankAgent_Run_Normal(t *testing.T) {
	llm := &stubLLMForRerank{}
	tools := newStubToolsForRerank()
	// self_rerank 返回重排后的列表。
	tools.handlers["rerank.self_rerank"] = wrapCandidates([]domain.Candidate{
		{ArticleID: "a2", Score: 0.9},
		{ArticleID: "a3", Score: 0.7},
		{ArticleID: "a1", Score: 0.5},
	})
	// llm rerank 返回稍调后的列表。
	tools.handlers["rerank.llm"] = wrapCandidates([]domain.Candidate{
		{ArticleID: "a3", Score: 0.95},
		{ArticleID: "a2", Score: 0.85},
		{ArticleID: "a1", Score: 0.45},
	})
	// ab_test 返回分桶状态。
	tools.handlers["rerank.ab_test"] = mustMarshal(map[string]any{"bucket": "B", "model": "llm"})

	a := NewRerankAgent(llm, tools, domain.AgentOptions{})
	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"candidates": makeCandidates()},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if tools.calls["rerank.self_rerank"] != 1 {
		t.Errorf("期望 self_rerank 调用 1 次, 实际 %d", tools.calls["rerank.self_rerank"])
	}
	if tools.calls["rerank.llm"] != 1 {
		t.Errorf("期望 rerank.llm 调用 1 次, 实际 %d", tools.calls["rerank.llm"])
	}
	if tools.calls["rerank.ab_test"] != 1 {
		t.Errorf("期望 rerank.ab_test 调用 1 次, 实际 %d", tools.calls["rerank.ab_test"])
	}
	// 验证最终结果来自 LLM rerank（a3 在首位）。
	items, ok := got.Result.([]domain.Candidate)
	if !ok {
		t.Fatalf("Result 类型断言失败: %T", got.Result)
	}
	if len(items) != 3 {
		t.Fatalf("期望 3 个候选, 实际 %d", len(items))
	}
	if items[0].ArticleID != "a3" {
		t.Errorf("首位 ArticleID = %q, 期望 a3（来自 LLM rerank）", items[0].ArticleID)
	}
	// Trace 应包含三步。
	if len(got.Trace) != 3 {
		t.Errorf("期望 3 步 trace, 实际 %d", len(got.Trace))
	}
	if got.Trace[0] != "rerank.self" || got.Trace[1] != "rerank.llm" || got.Trace[2] != "rerank.ab_test" {
		t.Errorf("Trace = %v", got.Trace)
	}
	// State 字段。
	if _, ok := got.State["rerank_items"]; !ok {
		t.Errorf("State 缺少 rerank_items")
	}
}

// TestRerankAgent_Run_EmptyCandidates 验证空候选输入正常返回空结果。
func TestRerankAgent_Run_EmptyCandidates(t *testing.T) {
	llm := &stubLLMForRerank{}
	tools := newStubToolsForRerank()
	tools.handlers["rerank.self_rerank"] = wrapCandidates(nil)
	a := NewRerankAgent(llm, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"candidates": []domain.Candidate{}},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	items, ok := got.Result.([]domain.Candidate)
	if !ok {
		t.Fatalf("Result 类型断言失败: %T", got.Result)
	}
	if len(items) != 0 {
		t.Errorf("期望 0 个候选, 实际 %d", len(items))
	}
}

// TestRerankAgent_Run_SelfFailFallback 验证自研 rerank 失败降级加权排序。
func TestRerankAgent_Run_SelfFailFallback(t *testing.T) {
	llm := &stubLLMForRerank{}
	tools := newStubToolsForRerank()
	tools.errs["rerank.self_rerank"] = errors.New("self rerank down")
	// llm rerank 不被调用（self 失败时降级，但 LLM 仍调用）。
	tools.handlers["rerank.llm"] = wrapCandidates(nil)
	tools.handlers["rerank.ab_test"] = mustMarshal(map[string]any{"bucket": "A"})

	a := NewRerankAgent(llm, tools, domain.AgentOptions{})
	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"candidates": makeCandidates()},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	items, ok := got.Result.([]domain.Candidate)
	if !ok {
		t.Fatalf("Result 类型断言失败: %T", got.Result)
	}
	// llm rerank 返回空 → 保留加权排序结果。
	// 加权求和：a1=0.5, a2=1.0, a3=0.6 → 期望顺序 a2 > a3 > a1。
	if len(items) != 3 {
		t.Fatalf("期望 3 个候选, 实际 %d", len(items))
	}
	if items[0].ArticleID != "a2" {
		t.Errorf("首位 ArticleID = %q, 期望 a2（加权最高）", items[0].ArticleID)
	}
}

// TestRerankAgent_Run_NilTools 验证 tools 为 nil 时完全降级加权排序。
func TestRerankAgent_Run_NilTools(t *testing.T) {
	a := NewRerankAgent(nil, nil, domain.AgentOptions{})
	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"candidates": makeCandidates()},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	items, ok := got.Result.([]domain.Candidate)
	if !ok {
		t.Fatalf("Result 类型断言失败: %T", got.Result)
	}
	// 加权求和：a1=0.5, a2=1.0, a3=0.6 → 期望顺序 a2 > a3 > a1。
	if len(items) != 3 {
		t.Fatalf("期望 3 个候选, 实际 %d", len(items))
	}
	if items[0].ArticleID != "a2" || items[1].ArticleID != "a3" || items[2].ArticleID != "a1" {
		t.Errorf("加权排序顺序错误: %v", items)
	}
	// Trace 仅含 rerank.self（降级路径只走一步）。
	if len(got.Trace) != 1 || got.Trace[0] != "rerank.self" {
		t.Errorf("Trace = %v, 期望 [rerank.self]", got.Trace)
	}
}

// TestRerankAgent_Run_NilLLM 验证 LLM 未注入时跳过 LLM rerank。
func TestRerankAgent_Run_NilLLM(t *testing.T) {
	tools := newStubToolsForRerank()
	tools.handlers["rerank.self_rerank"] = wrapCandidates([]domain.Candidate{
		{ArticleID: "a2", Score: 0.9},
		{ArticleID: "a1", Score: 0.5},
	})
	tools.handlers["rerank.ab_test"] = mustMarshal(map[string]any{"bucket": "A"})

	a := NewRerankAgent(nil, tools, domain.AgentOptions{})
	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"candidates": makeCandidates()},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	// 不应调用 rerank.llm。
	if tools.calls["rerank.llm"] != 0 {
		t.Errorf("LLM 未注入时不应调 rerank.llm, 实际 %d 次", tools.calls["rerank.llm"])
	}
	// Trace 应只有 self + ab_test（无 llm）。
	if len(got.Trace) != 2 {
		t.Errorf("期望 2 步 trace, 实际 %d", len(got.Trace))
	}
	if got.Trace[0] != "rerank.self" || got.Trace[1] != "rerank.ab_test" {
		t.Errorf("Trace = %v", got.Trace)
	}
	items, _ := got.Result.([]domain.Candidate)
	if len(items) != 2 || items[0].ArticleID != "a2" {
		t.Errorf("结果应来自 self_rerank, 实际: %v", items)
	}
}

// TestRerankAgent_Run_ABTestFail 验证 A/B 测试失败仍返回重排结果。
func TestRerankAgent_Run_ABTestFail(t *testing.T) {
	llm := &stubLLMForRerank{}
	tools := newStubToolsForRerank()
	tools.handlers["rerank.self_rerank"] = wrapCandidates([]domain.Candidate{
		{ArticleID: "a2", Score: 0.9},
	})
	tools.handlers["rerank.llm"] = wrapCandidates([]domain.Candidate{
		{ArticleID: "a2", Score: 0.95},
	})
	tools.errs["rerank.ab_test"] = errors.New("ab_test down")

	a := NewRerankAgent(llm, tools, domain.AgentOptions{})
	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"candidates": makeCandidates()},
	})
	if err != nil {
		t.Fatalf("A/B 测试失败不应返回错误, 实际 %v", err)
	}
	items, _ := got.Result.([]domain.Candidate)
	if len(items) != 1 || items[0].ArticleID != "a2" {
		t.Errorf("结果应来自 llm rerank, 实际: %v", items)
	}
	// ab_test 失败时 trace 不含 rerank.ab_test。
	for _, tr := range got.Trace {
		if tr == "rerank.ab_test" {
			t.Errorf("ab_test 失败时 trace 不应包含 rerank.ab_test")
		}
	}
}

// TestRerankAgent_Run_NoCandidatesState 验证 State 无 candidates 键时正常返回空。
func TestRerankAgent_Run_NoCandidatesState(t *testing.T) {
	a := NewRerankAgent(nil, nil, domain.AgentOptions{})
	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	items, ok := got.Result.([]domain.Candidate)
	if !ok {
		t.Fatalf("Result 类型断言失败: %T", got.Result)
	}
	if len(items) != 0 {
		t.Errorf("期望 0 个候选, 实际 %d", len(items))
	}
}

// TestRerankAgent_Run_LLMRerankFail 验证 LLM rerank 失败时保留 self_rerank 结果。
func TestRerankAgent_Run_LLMRerankFail(t *testing.T) {
	llm := &stubLLMForRerank{}
	tools := newStubToolsForRerank()
	tools.handlers["rerank.self_rerank"] = wrapCandidates([]domain.Candidate{
		{ArticleID: "a2", Score: 0.9},
	})
	tools.errs["rerank.llm"] = errors.New("llm rerank down")
	tools.handlers["rerank.ab_test"] = mustMarshal(map[string]any{"bucket": "A"})

	a := NewRerankAgent(llm, tools, domain.AgentOptions{})
	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"candidates": makeCandidates()},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	items, _ := got.Result.([]domain.Candidate)
	if len(items) != 1 || items[0].ArticleID != "a2" {
		t.Errorf("LLM rerank 失败应保留 self 结果, 实际: %v", items)
	}
}

// TestWeightedFallback 验证加权降级排序顺序。
func TestWeightedFallback(t *testing.T) {
	cs := []domain.Candidate{
		{ArticleID: "a1", Scores: map[string]float64{"x": 0.1}},
		{ArticleID: "a2", Scores: map[string]float64{"x": 0.9, "y": 0.8}},
		{ArticleID: "a3", Scores: map[string]float64{"x": 0.5}},
	}
	out := weightedFallback(cs)
	if len(out) != 3 {
		t.Fatalf("期望 3 个候选, 实际 %d", len(out))
	}
	if out[0].ArticleID != "a2" {
		t.Errorf("首位应为 a2（加权 1.7）, 实际 %s", out[0].ArticleID)
	}
	if out[1].ArticleID != "a3" {
		t.Errorf("次位应为 a3（加权 0.5）, 实际 %s", out[1].ArticleID)
	}
	if out[2].ArticleID != "a1" {
		t.Errorf("末位应为 a1（加权 0.1）, 实际 %s", out[2].ArticleID)
	}
}

// TestWeightedFallback_Empty 验证空输入返回 nil。
func TestWeightedFallback_Empty(t *testing.T) {
	if out := weightedFallback(nil); out != nil {
		t.Errorf("空输入应返回 nil, 实际 %v", out)
	}
}

// TestWeightedFallback_FallbackToScore 验证 Scores 为空时回退到 Score 字段。
func TestWeightedFallback_FallbackToScore(t *testing.T) {
	cs := []domain.Candidate{
		{ArticleID: "a1", Score: 0.3},
		{ArticleID: "a2", Score: 0.9},
	}
	out := weightedFallback(cs)
	if out[0].ArticleID != "a2" {
		t.Errorf("首位应为 a2（Score 0.9）, 实际 %s", out[0].ArticleID)
	}
}

// TestExtractCandidates 验证从 State 提取 candidates 支持多种形态。
func TestExtractCandidates(t *testing.T) {
	// 形态 1：直接 []domain.Candidate
	cs1 := []domain.Candidate{{ArticleID: "a1"}}
	got1 := extractCandidates(domain.AgentInput{State: map[string]any{"candidates": cs1}})
	if len(got1) != 1 || got1[0].ArticleID != "a1" {
		t.Errorf("形态 1 提取失败: %v", got1)
	}

	// 形态 2：[]any（JSON 反序列化场景，字段名与 Go struct 一致）
	cs2 := []any{
		map[string]any{"ArticleID": "a2", "Score": 0.5},
	}
	got2 := extractCandidates(domain.AgentInput{State: map[string]any{"candidates": cs2}})
	if len(got2) != 1 || got2[0].ArticleID != "a2" {
		t.Errorf("形态 2 提取失败: %v", got2)
	}

	// 形态 3：无 candidates 键
	got3 := extractCandidates(domain.AgentInput{State: map[string]any{}})
	if got3 != nil {
		t.Errorf("形态 3 应返回 nil, 实际 %v", got3)
	}

	// 形态 4：State 为 nil
	got4 := extractCandidates(domain.AgentInput{})
	if got4 != nil {
		t.Errorf("形态 4 应返回 nil, 实际 %v", got4)
	}
}
