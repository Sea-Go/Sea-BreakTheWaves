// Package agent intent_test.go — IntentAgent 测试。
//
// 该文件测试 internal/agent/intent.go 的 IntentAgent，覆盖：
//   - LLM 成功解析 / LLM 失败规则兜底 / nil LLM 规则兜底 / 无效 JSON 规则兜底
//   - 规则兜底各关键词分支（latest/comparative/transactional/informational/区别）
//   - State 保留与 intent 写入 / Trace 正确性 / JSON 解析
//
// 共享 stub（stubFALLMClient 等）定义于 factory_test.go。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// TestIntentAgent_Name 验证 Agent 名称。
func TestIntentAgent_Name(t *testing.T) {
	a := NewIntentAgent(&stubFALLMClient{}, &stubFAToolExecutor{}, domain.AgentOptions{})
	if a.Name() != "intent" {
		t.Errorf("Name = %q, 期望 intent", a.Name())
	}
}

// TestIntentAgent_Run_LLMSuccess 验证 LLM 成功时使用 LLM 结果。
func TestIntentAgent_Run_LLMSuccess(t *testing.T) {
	resp := `{"label":"comparative","confidence":0.9,"signals":["comparison"],"complexity":0.85,"time_intent":"","entities":[{"name":"AI","type":"Tag"}]}`
	llm := &stubFALLMClient{structuredResp: json.RawMessage(resp)}
	a := NewIntentAgent(llm, &stubFAToolExecutor{}, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{Message: "AI vs ML 区别"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	intent, ok := out.Result.(Intent)
	if !ok {
		t.Fatalf("Result 类型断言失败")
	}
	if intent.Label != "comparative" {
		t.Errorf("Label = %q, 期望 comparative", intent.Label)
	}
	if intent.Complexity != 0.85 {
		t.Errorf("Complexity = %f, 期望 0.85", intent.Complexity)
	}
	if intent.Confidence != 0.9 {
		t.Errorf("Confidence = %f, 期望 0.9", intent.Confidence)
	}
	if len(intent.Entities) != 1 || intent.Entities[0].Name != "AI" {
		t.Errorf("Entities 解析错误: %+v", intent.Entities)
	}
	// State 应包含 intent。
	if _, ok := out.State["intent"]; !ok {
		t.Errorf("State 应包含 intent 字段")
	}
	// Trace 应包含 intent.parse。
	if len(out.Trace) != 1 || out.Trace[0] != "intent.parse" {
		t.Errorf("Trace = %v, 期望 [intent.parse]", out.Trace)
	}
}

// TestIntentAgent_Run_LLMFail_RuleFallback 验证 LLM 失败时规则兜底。
func TestIntentAgent_Run_LLMFail_RuleFallback(t *testing.T) {
	llm := &stubFALLMClient{structuredErr: errors.New("llm down")}
	a := NewIntentAgent(llm, &stubFAToolExecutor{}, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{Message: "最新 AI 文章"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	intent, ok := out.Result.(Intent)
	if !ok {
		t.Fatalf("Result 类型断言失败")
	}
	if intent.Label != "latest" {
		t.Errorf("Label = %q, 期望 latest", intent.Label)
	}
	if intent.Complexity != 0.5 {
		t.Errorf("Complexity = %f, 期望 0.5", intent.Complexity)
	}
	if intent.TimeIntent != "today" {
		t.Errorf("TimeIntent = %q, 期望 today", intent.TimeIntent)
	}
}

// TestIntentAgent_Run_NilLLM_RuleFallback 验证 nil LLM 时规则兜底。
func TestIntentAgent_Run_NilLLM_RuleFallback(t *testing.T) {
	a := NewIntentAgent(nil, &stubFAToolExecutor{}, domain.AgentOptions{})
	out, err := a.Run(context.Background(), domain.AgentInput{Message: "AI vs ML"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	intent := out.Result.(Intent)
	if intent.Label != "comparative" {
		t.Errorf("Label = %q, 期望 comparative", intent.Label)
	}
	if intent.Complexity != 0.8 {
		t.Errorf("Complexity = %f, 期望 0.8", intent.Complexity)
	}
}

// TestIntentAgent_Run_LLMInvalidJSON_RuleFallback 验证无效 JSON 时规则兜底。
func TestIntentAgent_Run_LLMInvalidJSON_RuleFallback(t *testing.T) {
	llm := &stubFALLMClient{structuredResp: json.RawMessage("invalid json")}
	a := NewIntentAgent(llm, &stubFAToolExecutor{}, domain.AgentOptions{})
	out, err := a.Run(context.Background(), domain.AgentInput{Message: "怎么买手机"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	intent := out.Result.(Intent)
	if intent.Label != "transactional" {
		t.Errorf("Label = %q, 期望 transactional", intent.Label)
	}
	if intent.Complexity != 0.3 {
		t.Errorf("Complexity = %f, 期望 0.3", intent.Complexity)
	}
}

// TestRuleBasedIntent 验证规则兜底各关键词分支。
func TestRuleBasedIntent(t *testing.T) {
	tests := []struct {
		query     string
		wantLabel string
		wantCompl float64
		wantTime  string
	}{
		{"最新科技新闻", "latest", 0.5, "today"},
		{"今天的新闻", "latest", 0.5, "today"},
		{"iPhone vs Android", "comparative", 0.8, ""},
		{"对比 React 和 Vue", "comparative", 0.8, ""},
		{"这两个有什么区别", "informational", 0.8, ""},
		{"怎么买基金", "transactional", 0.3, ""},
		{"购买笔记本电脑", "transactional", 0.3, ""},
		{"什么是机器学习", "informational", 0.3, ""},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			intent := ruleBasedIntent(tt.query)
			if intent.Label != tt.wantLabel {
				t.Errorf("Label = %q, 期望 %q", intent.Label, tt.wantLabel)
			}
			if intent.Complexity != tt.wantCompl {
				t.Errorf("Complexity = %f, 期望 %f", intent.Complexity, tt.wantCompl)
			}
			if intent.TimeIntent != tt.wantTime {
				t.Errorf("TimeIntent = %q, 期望 %q", intent.TimeIntent, tt.wantTime)
			}
		})
	}
}

// TestRuleBasedIntent_ConfidencePositive 验证规则兜底 Confidence 为正数。
func TestRuleBasedIntent_ConfidencePositive(t *testing.T) {
	intent := ruleBasedIntent("随便查查")
	if intent.Confidence <= 0 {
		t.Errorf("Confidence 应为正数, 实际 %f", intent.Confidence)
	}
}

// TestRuleBasedIntent_CaseInsensitive 验证大小写不敏感（VS 匹配 vs）。
func TestRuleBasedIntent_CaseInsensitive(t *testing.T) {
	intent := ruleBasedIntent("React VS Vue")
	if intent.Label != "comparative" {
		t.Errorf("Label = %q, 期望 comparative（大小写不敏感）", intent.Label)
	}
	if intent.Complexity != 0.8 {
		t.Errorf("Complexity = %f, 期望 0.8", intent.Complexity)
	}
}

// TestIntentAgent_Run_StatePreserved 验证原有 State 保留并新增 intent。
func TestIntentAgent_Run_StatePreserved(t *testing.T) {
	llm := &stubFALLMClient{structuredErr: errors.New("down")}
	a := NewIntentAgent(llm, &stubFAToolExecutor{}, domain.AgentOptions{})
	input := domain.AgentInput{
		Message: "test",
		State:   map[string]any{"existing": "value"},
	}
	out, err := a.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if v, ok := out.State["existing"]; !ok || v != "value" {
		t.Errorf("原有 State 应保留, 实际 %v", out.State["existing"])
	}
	if _, ok := out.State["intent"]; !ok {
		t.Errorf("State 应新增 intent 字段")
	}
	// 原始 input.State 不应被修改。
	if _, ok := input.State["intent"]; ok {
		t.Errorf("原始 input.State 不应被修改")
	}
}

// TestIntentAgent_Run_StateNil 验证 nil State 时正常工作。
func TestIntentAgent_Run_StateNil(t *testing.T) {
	llm := &stubFALLMClient{structuredErr: errors.New("down")}
	a := NewIntentAgent(llm, &stubFAToolExecutor{}, domain.AgentOptions{})
	out, err := a.Run(context.Background(), domain.AgentInput{Message: "test"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if out.State == nil {
		t.Errorf("State 不应为 nil")
	}
	if _, ok := out.State["intent"]; !ok {
		t.Errorf("State 应包含 intent")
	}
}

// TestIntentAgent_Run_ResultIsIntent 验证 Result 字段类型为 Intent。
func TestIntentAgent_Run_ResultIsIntent(t *testing.T) {
	a := NewIntentAgent(nil, &stubFAToolExecutor{}, domain.AgentOptions{})
	out, err := a.Run(context.Background(), domain.AgentInput{Message: "test"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if _, ok := out.Result.(Intent); !ok {
		t.Errorf("Result 应为 Intent 类型, 实际 %T", out.Result)
	}
}

// TestParseIntentJSON 验证 JSON 解析正确。
func TestParseIntentJSON(t *testing.T) {
	raw := json.RawMessage(`{"label":"info","confidence":0.7,"signals":["s1"],"complexity":0.4,"time_intent":"latest","entities":[{"name":"E1","type":"Tag"}]}`)
	intent, err := parseIntentJSON(raw)
	if err != nil {
		t.Fatalf("parseIntentJSON 错误: %v", err)
	}
	if intent.Label != "info" {
		t.Errorf("Label = %q", intent.Label)
	}
	if intent.Confidence != 0.7 {
		t.Errorf("Confidence = %f", intent.Confidence)
	}
	if len(intent.Signals) != 1 || intent.Signals[0] != "s1" {
		t.Errorf("Signals = %v", intent.Signals)
	}
	if intent.Complexity != 0.4 {
		t.Errorf("Complexity = %f", intent.Complexity)
	}
	if len(intent.Entities) != 1 || intent.Entities[0].Name != "E1" {
		t.Errorf("Entities = %v", intent.Entities)
	}
	if intent.TimeIntent != "latest" {
		t.Errorf("TimeIntent = %q", intent.TimeIntent)
	}
}

// TestParseIntentJSON_Invalid 验证无效 JSON 返回错误。
func TestParseIntentJSON_Invalid(t *testing.T) {
	_, err := parseIntentJSON(json.RawMessage("not json"))
	if err == nil {
		t.Errorf("期望解析错误")
	}
}

// TestParseIntentJSON_Empty 验证空 JSON 对象解析为零值 Intent。
func TestParseIntentJSON_Empty(t *testing.T) {
	intent, err := parseIntentJSON(json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("空 JSON 应解析成功: %v", err)
	}
	if intent.Label != "" {
		t.Errorf("Label 应为空, 实际 %q", intent.Label)
	}
	if intent.Complexity != 0 {
		t.Errorf("Complexity 应为 0, 实际 %f", intent.Complexity)
	}
}

// TestIntentAgent_ImplementsDomainAgent 编译期验证 IntentAgent 实现 domain.Agent。
func TestIntentAgent_ImplementsDomainAgent(t *testing.T) {
	var _ domain.Agent = (*IntentAgent)(nil)
}
