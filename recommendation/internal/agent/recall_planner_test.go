// Package agent recall_planner_test.go — RecallPlannerAgent 测试。
//
// 该文件测试 internal/agent/recall_planner.go 的 RecallPlannerAgent，覆盖：
//   - LLM 成功规划 / LLM 失败默认等权重 / LLM 失败 complex intent 强化 graph+cf
//   - nil LLM 规则兜底 / 无效 JSON 规则兜底 / State 保留与 recall_plan 写入
//   - ruleBasedPlan 默认与 complex 分支 / readIntent 各场景 / JSON 解析
//
// 共享 stub（stubFALLMClient 等）定义于 factory_test.go。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"sea/internal/domain"
)

// TestRecallPlannerAgent_Name 验证 Agent 名称。
func TestRecallPlannerAgent_Name(t *testing.T) {
	a := NewRecallPlannerAgent(&stubFALLMClient{}, &stubFAToolExecutor{}, domain.AgentOptions{})
	if a.Name() != "recall_planner" {
		t.Errorf("Name = %q, 期望 recall_planner", a.Name())
	}
}

// TestRecallPlannerAgent_Run_LLMSuccess 验证 LLM 成功时使用 LLM 结果。
func TestRecallPlannerAgent_Run_LLMSuccess(t *testing.T) {
	resp := `{"sources":["content","graph"],"weights":{"content":0.6,"graph":0.4}}`
	llm := &stubFALLMClient{structuredResp: json.RawMessage(resp)}
	a := NewRecallPlannerAgent(llm, &stubFAToolExecutor{}, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{Message: "AI"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	plan, ok := out.Result.(RecallPlan)
	if !ok {
		t.Fatalf("Result 类型断言失败")
	}
	if len(plan.Sources) != 2 {
		t.Errorf("Sources 长度 = %d, 期望 2", len(plan.Sources))
	}
	if plan.Sources[0] != SourceContent {
		t.Errorf("Sources[0] = %q, 期望 content", plan.Sources[0])
	}
	if plan.Weights["content"] != 0.6 {
		t.Errorf("Weights[content] = %f, 期望 0.6", plan.Weights["content"])
	}
	if plan.Weights["graph"] != 0.4 {
		t.Errorf("Weights[graph] = %f, 期望 0.4", plan.Weights["graph"])
	}
	if _, ok := out.State["recall_plan"]; !ok {
		t.Errorf("State 应包含 recall_plan")
	}
	if len(out.Trace) != 1 || out.Trace[0] != "recall.plan" {
		t.Errorf("Trace = %v, 期望 [recall.plan]", out.Trace)
	}
}

// TestRecallPlannerAgent_Run_LLMFail_DefaultEqualWeights 验证 LLM 失败时默认 5 路等权重。
func TestRecallPlannerAgent_Run_LLMFail_DefaultEqualWeights(t *testing.T) {
	llm := &stubFALLMClient{structuredErr: errors.New("llm down")}
	a := NewRecallPlannerAgent(llm, &stubFAToolExecutor{}, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{Message: "test"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	plan := out.Result.(RecallPlan)
	if len(plan.Sources) != 5 {
		t.Errorf("Sources 长度 = %d, 期望 5", len(plan.Sources))
	}
	for _, src := range []string{string(SourceContent), string(SourceCF), string(SourceGraph), string(SourceChannel), string(SourceRule)} {
		if plan.Weights[src] != 0.2 {
			t.Errorf("Weights[%s] = %f, 期望 0.2", src, plan.Weights[src])
		}
	}
}

// TestRecallPlannerAgent_Run_LLMFail_ComplexIntent_StrengthenGraphCF 验证 complex intent 强化 graph+cf。
func TestRecallPlannerAgent_Run_LLMFail_ComplexIntent_StrengthenGraphCF(t *testing.T) {
	llm := &stubFALLMClient{structuredErr: errors.New("down")}
	a := NewRecallPlannerAgent(llm, &stubFAToolExecutor{}, domain.AgentOptions{})

	intent := Intent{Complexity: 0.8}
	out, err := a.Run(context.Background(), domain.AgentInput{
		Message: "test",
		State:   map[string]any{"intent": intent},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	plan := out.Result.(RecallPlan)
	if plan.Weights[string(SourceGraph)] != 0.3 {
		t.Errorf("Weights[graph] = %f, 期望 0.3", plan.Weights[string(SourceGraph)])
	}
	if plan.Weights[string(SourceCF)] != 0.25 {
		t.Errorf("Weights[cf] = %f, 期望 0.25", plan.Weights[string(SourceCF)])
	}
	if plan.Weights[string(SourceContent)] != 0.2 {
		t.Errorf("Weights[content] = %f, 期望 0.2", plan.Weights[string(SourceContent)])
	}
	if plan.Weights[string(SourceChannel)] != 0.15 {
		t.Errorf("Weights[channel] = %f, 期望 0.15", plan.Weights[string(SourceChannel)])
	}
	if plan.Weights[string(SourceRule)] != 0.1 {
		t.Errorf("Weights[rule] = %f, 期望 0.1", plan.Weights[string(SourceRule)])
	}
}

// TestRecallPlannerAgent_Run_NilLLM_RuleFallback 验证 nil LLM 时规则兜底。
func TestRecallPlannerAgent_Run_NilLLM_RuleFallback(t *testing.T) {
	a := NewRecallPlannerAgent(nil, &stubFAToolExecutor{}, domain.AgentOptions{})
	out, err := a.Run(context.Background(), domain.AgentInput{Message: "test"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	plan := out.Result.(RecallPlan)
	if len(plan.Sources) != 5 {
		t.Errorf("Sources 长度 = %d, 期望 5", len(plan.Sources))
	}
}

// TestRecallPlannerAgent_Run_LLMInvalidJSON_RuleFallback 验证无效 JSON 时规则兜底。
func TestRecallPlannerAgent_Run_LLMInvalidJSON_RuleFallback(t *testing.T) {
	llm := &stubFALLMClient{structuredResp: json.RawMessage("bad json")}
	a := NewRecallPlannerAgent(llm, &stubFAToolExecutor{}, domain.AgentOptions{})
	out, err := a.Run(context.Background(), domain.AgentInput{Message: "test"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	plan := out.Result.(RecallPlan)
	if len(plan.Sources) != 5 {
		t.Errorf("Sources 长度 = %d, 期望 5", len(plan.Sources))
	}
}

// TestRecallPlannerAgent_Run_StatePreserved 验证原有 State 保留并新增 recall_plan。
func TestRecallPlannerAgent_Run_StatePreserved(t *testing.T) {
	llm := &stubFALLMClient{structuredErr: errors.New("down")}
	a := NewRecallPlannerAgent(llm, &stubFAToolExecutor{}, domain.AgentOptions{})
	input := domain.AgentInput{
		Message: "test",
		State:   map[string]any{"existing": "val", "intent": Intent{Complexity: 0.3}},
	}
	out, err := a.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if v, ok := out.State["existing"]; !ok || v != "val" {
		t.Errorf("原有 State 应保留")
	}
	if _, ok := out.State["recall_plan"]; !ok {
		t.Errorf("State 应新增 recall_plan")
	}
	// intent 应保留。
	if _, ok := out.State["intent"]; !ok {
		t.Errorf("State 应保留 intent")
	}
	// 原始 input.State 不应被修改。
	if _, ok := input.State["recall_plan"]; ok {
		t.Errorf("原始 input.State 不应被修改")
	}
}

// TestRecallPlannerAgent_Run_ResultIsRecallPlan 验证 Result 类型为 RecallPlan。
func TestRecallPlannerAgent_Run_ResultIsRecallPlan(t *testing.T) {
	a := NewRecallPlannerAgent(nil, &stubFAToolExecutor{}, domain.AgentOptions{})
	out, err := a.Run(context.Background(), domain.AgentInput{Message: "test"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if _, ok := out.Result.(RecallPlan); !ok {
		t.Errorf("Result 应为 RecallPlan 类型, 实际 %T", out.Result)
	}
}

// TestRuleBasedPlan_Default 验证默认 5 路等权重。
func TestRuleBasedPlan_Default(t *testing.T) {
	plan := ruleBasedPlan(Intent{Complexity: 0.3})
	if len(plan.Sources) != 5 {
		t.Errorf("Sources 长度 = %d, 期望 5", len(plan.Sources))
	}
	for src, w := range plan.Weights {
		if w != 0.2 {
			t.Errorf("Weights[%s] = %f, 期望 0.2", src, w)
		}
	}
}

// TestRuleBasedPlan_Complex 验证 complex intent 强化 graph+cf 且权重和为 1.0。
func TestRuleBasedPlan_Complex(t *testing.T) {
	plan := ruleBasedPlan(Intent{Complexity: 0.7})
	if plan.Weights[string(SourceGraph)] != 0.3 {
		t.Errorf("Weights[graph] = %f, 期望 0.3", plan.Weights[string(SourceGraph)])
	}
	// 验证权重和为 1.0。
	var sum float64
	for _, w := range plan.Weights {
		sum += w
	}
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("权重和 = %f, 期望 1.0", sum)
	}
}

// TestRuleBasedPlan_Boundary_069 验证 complexity=0.69 时不强化（< 0.7）。
func TestRuleBasedPlan_Boundary_069(t *testing.T) {
	plan := ruleBasedPlan(Intent{Complexity: 0.69})
	// 0.69 < 0.7 → 默认等权重。
	if plan.Weights[string(SourceGraph)] != 0.2 {
		t.Errorf("complexity=0.69 应默认等权重, Weights[graph] = %f, 期望 0.2", plan.Weights[string(SourceGraph)])
	}
}

// TestRuleBasedPlan_Boundary_070 验证 complexity=0.70 时强化（>= 0.7）。
func TestRuleBasedPlan_Boundary_070(t *testing.T) {
	plan := ruleBasedPlan(Intent{Complexity: 0.70})
	if plan.Weights[string(SourceGraph)] != 0.3 {
		t.Errorf("complexity=0.70 应强化 graph, Weights[graph] = %f, 期望 0.3", plan.Weights[string(SourceGraph)])
	}
}

// TestReadIntent 验证从 State 读取 Intent 各场景。
func TestReadIntent(t *testing.T) {
	t.Run("nil state", func(t *testing.T) {
		_, ok := readIntent(nil)
		if ok {
			t.Errorf("nil state 应返回 false")
		}
	})
	t.Run("no intent key", func(t *testing.T) {
		_, ok := readIntent(map[string]any{"other": 1})
		if ok {
			t.Errorf("无 intent key 应返回 false")
		}
	})
	t.Run("wrong type", func(t *testing.T) {
		_, ok := readIntent(map[string]any{"intent": "string"})
		if ok {
			t.Errorf("类型错误应返回 false")
		}
	})
	t.Run("correct", func(t *testing.T) {
		intent := Intent{Label: "test", Complexity: 0.5}
		got, ok := readIntent(map[string]any{"intent": intent})
		if !ok {
			t.Fatalf("应返回 true")
		}
		if got.Label != "test" {
			t.Errorf("Label = %q, 期望 test", got.Label)
		}
		if got.Complexity != 0.5 {
			t.Errorf("Complexity = %f, 期望 0.5", got.Complexity)
		}
	})
}

// TestParseRecallPlanJSON 验证 JSON 解析正确。
func TestParseRecallPlanJSON(t *testing.T) {
	raw := json.RawMessage(`{"sources":["content","cf"],"weights":{"content":0.5,"cf":0.5}}`)
	plan, err := parseRecallPlanJSON(raw)
	if err != nil {
		t.Fatalf("parseRecallPlanJSON 错误: %v", err)
	}
	if len(plan.Sources) != 2 {
		t.Errorf("Sources 长度 = %d, 期望 2", len(plan.Sources))
	}
	if plan.Sources[0] != SourceContent {
		t.Errorf("Sources[0] = %q, 期望 content", plan.Sources[0])
	}
	if plan.Sources[1] != SourceCF {
		t.Errorf("Sources[1] = %q, 期望 cf", plan.Sources[1])
	}
	if plan.Weights["content"] != 0.5 {
		t.Errorf("Weights[content] = %f, 期望 0.5", plan.Weights["content"])
	}
}

// TestParseRecallPlanJSON_Invalid 验证无效 JSON 返回错误。
func TestParseRecallPlanJSON_Invalid(t *testing.T) {
	_, err := parseRecallPlanJSON(json.RawMessage("bad"))
	if err == nil {
		t.Errorf("期望解析错误")
	}
}

// TestParseRecallPlanJSON_Empty 验证空 JSON 对象解析为零值。
func TestParseRecallPlanJSON_Empty(t *testing.T) {
	plan, err := parseRecallPlanJSON(json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("空 JSON 应解析成功: %v", err)
	}
	if len(plan.Sources) != 0 {
		t.Errorf("Sources 应为空, 实际 %d", len(plan.Sources))
	}
	if plan.Weights == nil {
		t.Errorf("Weights 不应为 nil（应初始化为空 map）")
	}
}

// TestRecallSourceConstants 验证召回源常量值。
func TestRecallSourceConstants(t *testing.T) {
	if SourceContent != "content" {
		t.Errorf("SourceContent = %q, 期望 content", SourceContent)
	}
	if SourceCF != "cf" {
		t.Errorf("SourceCF = %q, 期望 cf", SourceCF)
	}
	if SourceGraph != "graph" {
		t.Errorf("SourceGraph = %q, 期望 graph", SourceGraph)
	}
	if SourceChannel != "channel" {
		t.Errorf("SourceChannel = %q, 期望 channel", SourceChannel)
	}
	if SourceRule != "rule" {
		t.Errorf("SourceRule = %q, 期望 rule", SourceRule)
	}
}

// TestRecallPlannerAgent_ImplementsDomainAgent 编译期验证 RecallPlannerAgent 实现 domain.Agent。
func TestRecallPlannerAgent_ImplementsDomainAgent(t *testing.T) {
	var _ domain.Agent = (*RecallPlannerAgent)(nil)
}
