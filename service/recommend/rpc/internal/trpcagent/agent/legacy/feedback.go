// Package agent feedback.go — FeedbackAgent 反馈闭环（Task 11.10）。
//
// 职责：根据反馈类型触发不同闭环（quality/cf/rerank/profile），通过 tools.Invoke
// 写入对应反馈通道。FeedbackAgent 不需要 LLM，构造函数签名统一为 3 参数（llm 忽略）。
//
// 二开扩展点：
//   - 替换 ToolExecutor：接入自研工具框架
//   - 扩展反馈类型：在 switch 中追加 case 与常量
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// 反馈类型常量。
const (
	// FeedbackTypeQuality 质量反馈：写入 quality.feedback 通道。
	FeedbackTypeQuality = "quality"
	// FeedbackTypeCF CF 反馈：写入 cf.feedback 通道（共现矩阵更新）。
	FeedbackTypeCF = "cf"
	// FeedbackTypeRerank Rerank 反馈：写入 rerank.feedback 通道（收集 rerank 样本）。
	FeedbackTypeRerank = "rerank"
	// FeedbackTypeProfile 画像反馈：写入 profile.feedback 通道（更新画像）。
	FeedbackTypeProfile = "profile"
)

// Feedback 反馈数据结构。
//
// 字段语义：
//   - Type：反馈类型（quality/cf/rerank/profile）
//   - ArticleID：文章 ID
//   - UserID：用户 ID
//   - Score：反馈分数（0-1，正反馈接近 1，负反馈接近 0）
//   - Reason：反馈原因（可选）
type Feedback struct {
	// Type 反馈类型。
	Type string `json:"type"`
	// ArticleID 文章 ID。
	ArticleID string `json:"article_id"`
	// UserID 用户 ID。
	UserID string `json:"user_id"`
	// Score 反馈分数。
	Score float64 `json:"score"`
	// Reason 反馈原因。
	Reason string `json:"reason"`
}

// feedbackToolName 反馈类型对应的工具名。
var feedbackToolName = map[string]string{
	FeedbackTypeQuality: "quality.feedback",
	FeedbackTypeCF:      "cf.feedback",
	FeedbackTypeRerank:  "rerank.feedback",
	FeedbackTypeProfile: "profile.feedback",
}

// FeedbackAgent 反馈闭环 Agent。
//
// 职责：
//   - 从 input.State["feedback"] 读取 Feedback
//   - 根据反馈类型调用对应工具（quality.feedback/cf.feedback/rerank.feedback/profile.feedback）
//   - tools 为 nil 时直接返回成功（无操作）
//
// 二开扩展点：替换 ToolExecutor 接入自研工具框架；扩展反馈类型常量与工具映射。
type FeedbackAgent struct {
	// tools 工具执行器（可为 nil，nil 时无操作）。
	tools ToolExecutor
	// opts Agent 选项。
	opts domain.AgentOptions
}

// 编译期断言：FeedbackAgent 实现 domain.Agent。
var _ domain.Agent = (*FeedbackAgent)(nil)

// NewFeedbackAgent 构造 FeedbackAgent。
// _ LLM 客户端（FeedbackAgent 不需要 LLM，参数为构造函数签名统一）；
// tools 工具执行器；opts Agent 选项。返回 domain.Agent 接口。
func NewFeedbackAgent(_ LLMClient, tools ToolExecutor, opts domain.AgentOptions) domain.Agent {
	return &FeedbackAgent{tools: tools, opts: opts}
}

// Name 返回 Agent 名称。
func (a *FeedbackAgent) Name() string { return "feedback" }

// Run 实现 domain.Agent.Run。
//
// 流程：
//  1. 从 input.State["feedback"] 读取 Feedback
//  2. 根据反馈类型触发不同闭环：
//     - quality → tools.Invoke("quality.feedback", ...)
//     - cf → tools.Invoke("cf.feedback", ...)
//     - rerank → tools.Invoke("rerank.feedback", ...)
//     - profile → tools.Invoke("profile.feedback", ...)
//  3. tools 为 nil 时直接返回成功（无操作）
//  4. State 写入 feedback_applied（bool）；Result 写入 "feedback applied"
func (a *FeedbackAgent) Run(ctx context.Context, input domain.AgentInput) (domain.AgentOutput, error) {
	trace := make([]string, 0, 1)
	state := copyState(input.State)

	feedback := extractFeedback(input.State["feedback"])

	// tools 为 nil 时直接返回成功（无操作）。
	if a.tools == nil {
		state["feedback_applied"] = false
		return domain.AgentOutput{
			State:  state,
			Result: "feedback applied",
			Trace:  trace,
		}, nil
	}

	// 根据反馈类型触发不同闭环。
	applied := false
	if toolName, ok := feedbackToolName[feedback.Type]; ok {
		applied = a.invokeFeedback(ctx, toolName, feedback)
		trace = append(trace, "feedback."+feedback.Type)
	}

	state["feedback_applied"] = applied
	return domain.AgentOutput{
		State:  state,
		Result: "feedback applied",
		Trace:  trace,
	}, nil
}

// invokeFeedback 调用反馈工具。
// ctx 上下文；toolName 工具名；feedback 反馈数据。返回是否成功。
func (a *FeedbackAgent) invokeFeedback(ctx context.Context, toolName string, feedback Feedback) bool {
	payload, err := json.Marshal(feedback)
	if err != nil {
		return false
	}
	_, err = a.tools.Invoke(ctx, toolName, payload)
	return err == nil
}

// extractFeedback 从 input.State["feedback"] 提取 Feedback。
// 支持 Feedback struct 与 map[string]any 两种形式。
// raw 原始值。返回 Feedback（无数据时返回零值）。
func extractFeedback(raw any) Feedback {
	if raw == nil {
		return Feedback{}
	}
	switch v := raw.(type) {
	case Feedback:
		return v
	case map[string]any:
		f := Feedback{
			Type:      getStringFromAny(v["type"]),
			ArticleID: getStringFromAny(v["article_id"]),
			UserID:    getStringFromAny(v["user_id"]),
			Reason:    getStringFromAny(v["reason"]),
		}
		f.Score = getFloatFromAny(v["score"])
		return f
	}
	return Feedback{}
}

// getStringFromAny 从 any 安全提取 string。
func getStringFromAny(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// getFloatFromAny 从 any 安全提取 float64（兼容 float64/float32/int/int64）。
func getFloatFromAny(v any) float64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
