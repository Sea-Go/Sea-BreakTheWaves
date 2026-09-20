// Package agent feedback_test.go — FeedbackAgent 单测（Task 11.10）。
//
// 覆盖：
//   - Name/Run 基本流程
//   - 4 种反馈类型（quality/cf/rerank/profile）触发对应工具
//   - tools 为 nil 时无操作
//   - 未知反馈类型不触发工具
//   - Feedback 从 map[string]any 提取
//   - tools.Invoke 失败时 feedback_applied=false
//   - State 保留输入键
//
// 使用 stdlib 手写 stub（stubTool 复用自 channel_test.go），不依赖 testify。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"sea/internal/domain"
)

// ============================================================================
// 测试用例
// ============================================================================

// TestFeedbackAgent_Name 验证 Name 返回 "feedback"。
func TestFeedbackAgent_Name(t *testing.T) {
	a := NewFeedbackAgent(nil, nil, domain.AgentOptions{})
	if a.Name() != "feedback" {
		t.Errorf("Name = %q, 期望 feedback", a.Name())
	}
}

// TestFeedbackAgent_Run_Quality 验证 quality 反馈触发 quality.feedback 工具。
func TestFeedbackAgent_Run_Quality(t *testing.T) {
	tool := &stubTool{}
	a := NewFeedbackAgent(nil, tool, domain.AgentOptions{})

	fb := Feedback{
		Type:      FeedbackTypeQuality,
		ArticleID: "art-1",
		UserID:    "u1",
		Score:     0.9,
		Reason:    "high quality",
	}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State:  map[string]any{"feedback": fb},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}

	if tool.invokeCount() != 1 {
		t.Errorf("期望 tools.Invoke 调用 1 次, 实际 %d 次", tool.invokeCount())
	}
	name, input, ok := tool.lastInvoke()
	if !ok || name != "quality.feedback" {
		t.Errorf("期望 Invoke quality.feedback, 实际 %s", name)
	}
	// 验证 input payload。
	var got Feedback
	if err := json.Unmarshal(input, &got); err != nil {
		t.Fatalf("解析 input payload 失败: %v", err)
	}
	if got.ArticleID != "art-1" {
		t.Errorf("payload ArticleID = %s, 期望 art-1", got.ArticleID)
	}
	if got.Score != 0.9 {
		t.Errorf("payload Score = %v, 期望 0.9", got.Score)
	}

	if out.State["feedback_applied"] != true {
		t.Errorf("feedback_applied 应为 true")
	}
	if out.Result != "feedback applied" {
		t.Errorf("Result = %v, 期望 feedback applied", out.Result)
	}
	if len(out.Trace) != 1 || out.Trace[0] != "feedback.quality" {
		t.Errorf("trace = %v, 期望 [feedback.quality]", out.Trace)
	}
}

// TestFeedbackAgent_Run_CF 验证 cf 反馈触发 cf.feedback 工具。
func TestFeedbackAgent_Run_CF(t *testing.T) {
	tool := &stubTool{}
	a := NewFeedbackAgent(nil, tool, domain.AgentOptions{})

	fb := Feedback{Type: FeedbackTypeCF, ArticleID: "art-2", UserID: "u2", Score: 0.8}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u2",
		State:  map[string]any{"feedback": fb},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}

	if tool.invokeCount() != 1 {
		t.Errorf("期望 tools.Invoke 调用 1 次, 实际 %d 次", tool.invokeCount())
	}
	name, _, _ := tool.lastInvoke()
	if name != "cf.feedback" {
		t.Errorf("期望 Invoke cf.feedback, 实际 %s", name)
	}
	if len(out.Trace) != 1 || out.Trace[0] != "feedback.cf" {
		t.Errorf("trace = %v, 期望 [feedback.cf]", out.Trace)
	}
}

// TestFeedbackAgent_Run_Rerank 验证 rerank 反馈触发 rerank.feedback 工具。
func TestFeedbackAgent_Run_Rerank(t *testing.T) {
	tool := &stubTool{}
	a := NewFeedbackAgent(nil, tool, domain.AgentOptions{})

	fb := Feedback{Type: FeedbackTypeRerank, ArticleID: "art-3", UserID: "u3", Score: 0.7}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u3",
		State:  map[string]any{"feedback": fb},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}

	if tool.invokeCount() != 1 {
		t.Errorf("期望 tools.Invoke 调用 1 次, 实际 %d 次", tool.invokeCount())
	}
	name, _, _ := tool.lastInvoke()
	if name != "rerank.feedback" {
		t.Errorf("期望 Invoke rerank.feedback, 实际 %s", name)
	}
	if len(out.Trace) != 1 || out.Trace[0] != "feedback.rerank" {
		t.Errorf("trace = %v, 期望 [feedback.rerank]", out.Trace)
	}
}

// TestFeedbackAgent_Run_Profile 验证 profile 反馈触发 profile.feedback 工具。
func TestFeedbackAgent_Run_Profile(t *testing.T) {
	tool := &stubTool{}
	a := NewFeedbackAgent(nil, tool, domain.AgentOptions{})

	fb := Feedback{Type: FeedbackTypeProfile, ArticleID: "art-4", UserID: "u4", Score: 0.6}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u4",
		State:  map[string]any{"feedback": fb},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}

	if tool.invokeCount() != 1 {
		t.Errorf("期望 tools.Invoke 调用 1 次, 实际 %d 次", tool.invokeCount())
	}
	name, _, _ := tool.lastInvoke()
	if name != "profile.feedback" {
		t.Errorf("期望 Invoke profile.feedback, 实际 %s", name)
	}
	if len(out.Trace) != 1 || out.Trace[0] != "feedback.profile" {
		t.Errorf("trace = %v, 期望 [feedback.profile]", out.Trace)
	}
}

// TestFeedbackAgent_Run_NilTools 验证 tools 为 nil 时无操作。
func TestFeedbackAgent_Run_NilTools(t *testing.T) {
	a := NewFeedbackAgent(nil, nil, domain.AgentOptions{})

	fb := Feedback{Type: FeedbackTypeQuality, ArticleID: "art-1", UserID: "u1", Score: 0.9}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State:  map[string]any{"feedback": fb},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if out.State["feedback_applied"] != false {
		t.Errorf("tools 为 nil 时 feedback_applied 应为 false")
	}
	if out.Result != "feedback applied" {
		t.Errorf("Result = %v, 期望 feedback applied", out.Result)
	}
	if len(out.Trace) != 0 {
		t.Errorf("tools 为 nil 时 trace 应为空, 实际 %v", out.Trace)
	}
}

// TestFeedbackAgent_Run_UnknownType 验证未知反馈类型不触发工具。
func TestFeedbackAgent_Run_UnknownType(t *testing.T) {
	tool := &stubTool{}
	a := NewFeedbackAgent(nil, tool, domain.AgentOptions{})

	fb := Feedback{Type: "unknown_type", ArticleID: "art-1", UserID: "u1"}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State:  map[string]any{"feedback": fb},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if tool.invokeCount() != 0 {
		t.Errorf("未知反馈类型不应触发工具, 实际调用 %d 次", tool.invokeCount())
	}
	if out.State["feedback_applied"] != false {
		t.Errorf("未知反馈类型 feedback_applied 应为 false")
	}
	if len(out.Trace) != 0 {
		t.Errorf("未知反馈类型 trace 应为空, 实际 %v", out.Trace)
	}
}

// TestFeedbackAgent_Run_FromMap 验证从 map[string]any 提取 Feedback。
func TestFeedbackAgent_Run_FromMap(t *testing.T) {
	tool := &stubTool{}
	a := NewFeedbackAgent(nil, tool, domain.AgentOptions{})

	fbMap := map[string]any{
		"type":       "quality",
		"article_id": "art-5",
		"user_id":    "u5",
		"score":      0.85,
		"reason":     "good",
	}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u5",
		State:  map[string]any{"feedback": fbMap},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if tool.invokeCount() != 1 {
		t.Errorf("期望 tools.Invoke 调用 1 次, 实际 %d 次", tool.invokeCount())
	}
	name, input, _ := tool.lastInvoke()
	if name != "quality.feedback" {
		t.Errorf("期望 Invoke quality.feedback, 实际 %s", name)
	}
	var got Feedback
	if err := json.Unmarshal(input, &got); err != nil {
		t.Fatalf("解析 input payload 失败: %v", err)
	}
	if got.ArticleID != "art-5" {
		t.Errorf("payload ArticleID = %s, 期望 art-5", got.ArticleID)
	}
	if got.Score != 0.85 {
		t.Errorf("payload Score = %v, 期望 0.85", got.Score)
	}
	if out.State["feedback_applied"] != true {
		t.Errorf("feedback_applied 应为 true")
	}
}

// TestFeedbackAgent_Run_InvokeFail 验证 tools.Invoke 失败时 feedback_applied=false。
func TestFeedbackAgent_Run_InvokeFail(t *testing.T) {
	tool := &stubTool{invokeErr: errors.New("tool down")}
	a := NewFeedbackAgent(nil, tool, domain.AgentOptions{})

	fb := Feedback{Type: FeedbackTypeQuality, ArticleID: "art-1", UserID: "u1"}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State:  map[string]any{"feedback": fb},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	// Invoke 失败不阻断主流程，feedback_applied=false。
	if out.State["feedback_applied"] != false {
		t.Errorf("Invoke 失败时 feedback_applied 应为 false")
	}
	if out.Result != "feedback applied" {
		t.Errorf("Result = %v, 期望 feedback applied", out.Result)
	}
}

// TestFeedbackAgent_Run_NoFeedback 验证 State 中无 feedback 时不 panic。
func TestFeedbackAgent_Run_NoFeedback(t *testing.T) {
	tool := &stubTool{}
	a := NewFeedbackAgent(nil, tool, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State:  map[string]any{},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if tool.invokeCount() != 0 {
		t.Errorf("无 feedback 时不应调用工具, 实际 %d 次", tool.invokeCount())
	}
	if out.State["feedback_applied"] != false {
		t.Errorf("无 feedback 时 feedback_applied 应为 false")
	}
}

// TestFeedbackAgent_Run_StatePreserved 验证 Run 保留输入 State 中的其他键。
func TestFeedbackAgent_Run_StatePreserved(t *testing.T) {
	tool := &stubTool{}
	a := NewFeedbackAgent(nil, tool, domain.AgentOptions{})

	fb := Feedback{Type: FeedbackTypeCF, ArticleID: "art-1", UserID: "u1"}
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State: map[string]any{
			"feedback":     fb,
			"existing_key": "existing_value",
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if out.State["existing_key"] != "existing_value" {
		t.Errorf("输入 State 应被保留, existing_key = %v", out.State["existing_key"])
	}
}

// TestExtractFeedback 验证 extractFeedback 从不同类型提取 Feedback。
func TestExtractFeedback(t *testing.T) {
	// Feedback struct 直接传入。
	fb := Feedback{Type: "quality", ArticleID: "a1", UserID: "u1", Score: 0.9, Reason: "good"}
	got := extractFeedback(fb)
	if got.Type != "quality" || got.ArticleID != "a1" || got.Score != 0.9 {
		t.Errorf("extractFeedback(Feedback) = %+v, 不匹配", got)
	}

	// map[string]any 传入。
	m := map[string]any{
		"type":       "cf",
		"article_id": "a2",
		"user_id":    "u2",
		"score":      0.5,
		"reason":     "test",
	}
	got = extractFeedback(m)
	if got.Type != "cf" || got.ArticleID != "a2" || got.Score != 0.5 {
		t.Errorf("extractFeedback(map) = %+v, 不匹配", got)
	}

	// nil 传入。
	got = extractFeedback(nil)
	if got.Type != "" {
		t.Errorf("extractFeedback(nil) 应返回零值, 实际 %+v", got)
	}
}

// TestFeedbackAgent_ImplementsDomainAgent 验证 FeedbackAgent 实现 domain.Agent 接口。
func TestFeedbackAgent_ImplementsDomainAgent(t *testing.T) {
	var _ domain.Agent = (*FeedbackAgent)(nil)
	var _ domain.Agent = NewFeedbackAgent(nil, nil, domain.AgentOptions{})
}

// TestFeedbackTypeConstants 验证反馈类型常量值。
func TestFeedbackTypeConstants(t *testing.T) {
	if FeedbackTypeQuality != "quality" {
		t.Errorf("FeedbackTypeQuality = %q, 期望 quality", FeedbackTypeQuality)
	}
	if FeedbackTypeCF != "cf" {
		t.Errorf("FeedbackTypeCF = %q, 期望 cf", FeedbackTypeCF)
	}
	if FeedbackTypeRerank != "rerank" {
		t.Errorf("FeedbackTypeRerank = %q, 期望 rerank", FeedbackTypeRerank)
	}
	if FeedbackTypeProfile != "profile" {
		t.Errorf("FeedbackTypeProfile = %q, 期望 profile", FeedbackTypeProfile)
	}
}
