// Package agent common.go — 多 Agent 共享契约。
//
// 该文件定义 12 个 Agent 共享的 LLM/工具/记忆等抽象 interface，避免各 Agent
// 文件重复定义导致冲突。所有 Agent 通过 interface 注入依赖，禁止直接 import
// trpc-agent-go，确保可独立编译与离线测试。
//
// 二开扩展点：
//   - 替换 LLMClient：实现该 interface 接入自研/第三方 LLM
//   - 替换 ToolExecutor：实现该 interface 接入自研工具调用框架
//   - 替换 MemoryStore：实现该 interface 接入自研记忆存储
package agent

import (
	"context"
	"encoding/json"
)

// LLMClient LLM 调用抽象，用于解耦 12 个 Agent 与 trpc-agent-go。
//
// 真实实现可包装 trpc-agent-go 的 llmagent.LLMAgent；
// 默认实现 DefaultLLMClient 直接返回空响应，用于离线测试。
//
// 二开扩展点：实现该 interface 接入自研/第三方 LLM（如 OpenAI/Claude/通义）。
type LLMClient interface {
	// Complete 普通补全，返回文本响应。
	// ctx 上下文；prompt 提示词；opts 调用选项（temperature/max_tokens 等）。
	Complete(ctx context.Context, prompt string, opts LLMOptions) (string, error)
	// CompleteWithStructuredOutput 结构化输出，按 JSON Schema 约束返回。
	// ctx 上下文；prompt 提示词；schema JSON Schema（描述输出结构）。
	// 返回 JSON 原始字节与 error。
	CompleteWithStructuredOutput(ctx context.Context, prompt string, schema map[string]any) (json.RawMessage, error)
	// CompleteWithLogprobs 带 logprobs 输出，用于 JudgeAgent 质量标签概率分布。
	// ctx 上下文；prompt 提示词；topLogprobs 每位返回的 top-N 概率。
	// 返回响应文本 + logprobs 二维切片 + error。
	CompleteWithLogprobs(ctx context.Context, prompt string, topLogprobs int) (string, [][]LogprobEntry, error)
}

// LLMOptions LLM 调用选项。
type LLMOptions struct {
	// Temperature 采样温度。
	Temperature float64
	// MaxTokens 最大输出 token。
	MaxTokens int
	// Model 模型名（覆盖默认）。
	Model string
	// EnableCache 是否启用 Prompt Cache（WithOptimizeForCache(true)）。
	EnableCache bool
}

// LogprobEntry logprobs 条目。
type LogprobEntry struct {
	// Token token 文本。
	Token string
	// Logprob 对数概率。
	Logprob float64
}

// ToolExecutor 工具调用抽象，用于 Agent 调用 Skills/Tools。
//
// 真实实现可包装 trpc-agent-go 的 Tool 框架；
// 默认实现 DefaultToolExecutor 直接返回错误，用于离线测试。
//
// 二开扩展点：实现该 interface 接入自研工具调用框架。
type ToolExecutor interface {
	// Invoke 调用工具。
	// ctx 上下文；name 工具名；input 工具输入（JSON）。
	// 返回工具输出（JSON）与 error。
	Invoke(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error)
	// List 列出可用工具名。
	List(ctx context.Context) ([]string, error)
}

// MemoryStore 记忆存储抽象，用于 ProfileAgent 等。
//
// 真实实现可包装 trpc-agent-go 的 Memory 模块；
// 默认实现 DefaultMemoryStore 用 sync.Map，用于离线测试。
//
// 二开扩展点：实现该 interface 接入自研记忆存储（如 Redis/Postgres）。
type MemoryStore interface {
	// Load 加载记忆。
	// ctx 上下文；key 记忆键（如 "user:123:topics"）。
	Load(ctx context.Context, key string) (string, error)
	// Save 保存记忆。
	Save(ctx context.Context, key, value string) error
	// Delete 删除记忆。
	Delete(ctx context.Context, key string) error
}

// DefaultLLMClient 默认 LLMClient 实现，直接返回空响应。
//
// 用于离线测试与兜底场景，不调用真实 LLM。
type DefaultLLMClient struct{}

// Complete 返回空字符串。
func (d *DefaultLLMClient) Complete(_ context.Context, _ string, _ LLMOptions) (string, error) {
	return "", nil
}

// CompleteWithStructuredOutput 返回空 JSON 对象。
func (d *DefaultLLMClient) CompleteWithStructuredOutput(_ context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
	return json.RawMessage("{}"), nil
}

// CompleteWithLogprobs 返回空响应与空 logprobs。
func (d *DefaultLLMClient) CompleteWithLogprobs(_ context.Context, _ string, _ int) (string, [][]LogprobEntry, error) {
	return "", nil, nil
}

// 编译期断言：DefaultLLMClient 实现 LLMClient。
var _ LLMClient = (*DefaultLLMClient)(nil)
