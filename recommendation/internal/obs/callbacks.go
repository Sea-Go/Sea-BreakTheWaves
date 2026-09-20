package obs

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"sea/internal/domain"
)

// ============================================================================
// trpc-agent-go Callbacks 接入适配器（Task 5.4）
//
// 该文件将 trpc-agent-go 的 Agent 循环回调适配为 domain.BehaviorEvent，
// 通过 domain.EventEmitter 统一发射，供 HookRegistry 消费。
//
// 设计说明：
//   - 不直接 import trpc-agent-go，避免离线编译依赖；CallbackAdapter 提供
//     On* 方法，由调用方在 Agent 初始化时注册为 trpc-agent-go 的 Callback。
//   - 事件类型常量引用 internal/domain/event.go 提供的 domain.EventXxx 常量
//     （由 Task 5.1 并行创建），确保与 Registry.EmitTyped 类型分发一致。
//
// 二开扩展点：
//   - 新增回调：在 CallbackAdapter 增加方法并发射对应 BehaviorEvent
//   - 对接 trpc-agent-go：在 Agent 初始化时将 CallbackAdapter 方法注册为
//     trpc-agent-go 的 Callback（TODO 待 trpc-agent-go callback API 确认）
// ============================================================================

// callbackEventSeq 事件 ID 自增序号，保证回调发射事件 ID 唯一。
var callbackEventSeq uint64

// CallbackAdapter 将 trpc-agent-go 回调适配为 BehaviorEvent。
// 通过 NewCallbackAdapter 构造，各 On* 方法发射对应事件到 EventEmitter。
//
// 二开：在 trpc-agent-go Agent 初始化时，将 CallbackAdapter 的 On* 方法
// 注册为 Callback，即可把 Agent 循环内的工具调用/LLM 调用/Agent 结束
// 统一接入行为事件体系。
type CallbackAdapter struct {
	emitter domain.EventEmitter
	logger  *slog.Logger
}

// NewCallbackAdapter 创建回调适配器。
// emitter 行为事件发射器（domain.EventEmitter 实现，可为 nil，此时仅记录日志）。
// logger 结构化日志器（可为 nil，默认用 slog.Default()）。
func NewCallbackAdapter(emitter domain.EventEmitter, logger *slog.Logger) *CallbackAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &CallbackAdapter{emitter: emitter, logger: logger}
}

// OnToolCall 工具调用回调，发射 EventToolCall BehaviorEvent。
// toolName 工具名；input 调用输入；output 调用输出。
// TODO 对接 trpc-agent-go 实际 callback 注册（签名待确认）。
func (a *CallbackAdapter) OnToolCall(ctx context.Context, toolName string, input, output any) error {
	ev := domain.BehaviorEvent{
		EventID:   newCallbackEventID(),
		EventType: domain.EventToolCall,
		Timestamp: time.Now(),
		Payload: map[string]any{
			"tool_name": toolName,
			"input":     input,
			"output":    output,
		},
	}
	return a.emit(ctx, ev)
}

// OnLLMCall LLM 调用回调，发射 EventLLMCall BehaviorEvent。
// model 模型名；prompt 输入提示；response 模型响应；tokensUsed token 用量。
// TODO 对接 trpc-agent-go 实际 callback 注册（签名待确认）。
func (a *CallbackAdapter) OnLLMCall(ctx context.Context, model string, prompt, response string, tokensUsed int) error {
	ev := domain.BehaviorEvent{
		EventID:   newCallbackEventID(),
		EventType: domain.EventLLMCall,
		Timestamp: time.Now(),
		Payload: map[string]any{
			"model":       model,
			"prompt":      prompt,
			"response":    response,
			"tokens_used": tokensUsed,
		},
	}
	return a.emit(ctx, ev)
}

// OnAgentEnd Agent 结束回调，发射 EventAgentEnd BehaviorEvent。
// agentName Agent 名称；result Agent 输出结果。
// TODO 对接 trpc-agent-go 实际 callback 注册（签名待确认）。
func (a *CallbackAdapter) OnAgentEnd(ctx context.Context, agentName string, result any) error {
	ev := domain.BehaviorEvent{
		EventID:   newCallbackEventID(),
		EventType: domain.EventAgentEnd,
		Timestamp: time.Now(),
		Payload: map[string]any{
			"agent_name": agentName,
			"result":     result,
		},
	}
	return a.emit(ctx, ev)
}

// emit 发射事件并记录日志，emitter 为 nil 时仅记录日志（事件丢弃）。
func (a *CallbackAdapter) emit(ctx context.Context, ev domain.BehaviorEvent) error {
	if a.emitter == nil {
		a.logger.WarnContext(ctx, "callback adapter emitter nil, event dropped",
			slog.String("event_type", ev.EventType))
		return nil
	}
	if err := a.emitter.Emit(ctx, ev); err != nil {
		a.logger.ErrorContext(ctx, "emit behavior event failed",
			slog.String("event_type", ev.EventType),
			slog.Any("err", err),
		)
		return err
	}
	return nil
}

// newCallbackEventID 生成回调事件 ID（自增序号，保证唯一）。
// TODO 可对接 google/uuid（go.mod 已有 github.com/google/uuid）。
func newCallbackEventID() string {
	n := atomic.AddUint64(&callbackEventSeq, 1)
	return fmt.Sprintf("cb-%d", n)
}
