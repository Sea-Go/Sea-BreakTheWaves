// Package agent runner.go — Runner 集成与事件流（Task 12.2）。
//
// 该文件实现 Runner：作为多 Agent 架构与外部入口（HTTP/gRPC/Kafka/AG-UI）之间的
// 集成层，封装 Orchestrator 调用 + 行为事件发射 + Hook 顺序处理 + Trace 收集，
// 让上层入口只需关注协议适配，无需关心事件流细节。
//
// 职责：
//   - 调用 domain.Orchestrator.Recommend 执行推荐主流程
//   - 根据 response.PathTaken 发射对应行为事件（fast_path_hit/slow_path_hit/hybrid_merge）
//   - 顺序调用注入的 Hook 处理事件（错误隔离，单个 Hook 失败不影响主流程）
//   - 将事件透传给 TraceCollector 用于离线 trace 收集
//   - 返回 RecommendResponse（含 PathTaken/Cost/GraphTrace）
//
// 二开扩展点：
//   - 通过 WithEmitter 注入自定义 EventEmitter（如接入 Kafka/ES）
//   - 通过 WithHooks 注入业务 Hook（如画像更新/质量反馈/CF 反馈）
//   - 通过 WithTraceCollector 注入 trace 收集器（如 Jaeger/OTel）
//
// 不直接 import trpc-agent-go / neo4j / milvus，所有依赖通过 interface 注入。
package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// interface 抽象（与 domain/event 包同构，本文件本地定义避免直接依赖）
// ----------------------------------------------------------------------------

// EventEmitter 事件发射 interface，与 domain.EventEmitter 同构。
//
// 职责：统一发射行为事件，供 Hook 顺序消费。
// 真实实现可委托给 internal/event.Registry。
//
// 二开扩展点：实现该 interface 接入自定义事件出口（Kafka/ES/日志）。
type EventEmitter interface {
	// Emit 发射行为事件。
	// ctx 上下文；event 行为事件。
	// 返回 error（聚合错误，不影响主流程）。
	Emit(ctx context.Context, event domain.BehaviorEvent) error
}

// Hook 行为事件 Hook 契约，与 domain.Hook 同构。
//
// 职责：顺序处理行为事件，错误隔离不影响主流程与其他 Hook。
// 真实实现可委托给 internal/event.Hook。
//
// 二开扩展点：实现该 interface 并通过 WithHooks 注入。
type Hook interface {
	// Name 返回 Hook 名称（用于日志与调试）。
	Name() string
	// OnEvent 处理事件；错误隔离不影响主流程与其他 Hook。
	OnEvent(ctx context.Context, event domain.BehaviorEvent) error
}

// TraceCollector Trace 收集器 interface，用于离线 trace 收集。
//
// 职责：收集行为事件用于离线 trace 回放与调试。
// 与 EventEmitter 区别：TraceCollector 同步收集、不返回 error、专用于 trace。
//
// 二开扩展点：实现该 interface 接入 Jaeger/OTel/本地文件 trace 收集。
type TraceCollector interface {
	// Collect 收集一条行为事件到 trace。
	Collect(event domain.BehaviorEvent)
}

// ----------------------------------------------------------------------------
// 默认 Noop 实现（用于离线测试与未注入场景）
// ----------------------------------------------------------------------------

// NoopEmitter 默认 EventEmitter 实现，不发射任何事件。
// 用于未注入 EventEmitter 或离线测试场景。
type NoopEmitter struct{}

// Emit 直接返回 nil，不发射事件。
func (NoopEmitter) Emit(_ context.Context, _ domain.BehaviorEvent) error { return nil }

// NoopHook 默认 Hook 实现，不处理任何事件。
type NoopHook struct{}

// Name 返回 "noop"。
func (NoopHook) Name() string { return "noop" }

// OnEvent 直接返回 nil，不处理事件。
func (NoopHook) OnEvent(_ context.Context, _ domain.BehaviorEvent) error { return nil }

// NoopTraceCollector 默认 TraceCollector 实现，不收集任何 trace。
type NoopTraceCollector struct{}

// Collect 空实现，丢弃事件。
func (NoopTraceCollector) Collect(_ domain.BehaviorEvent) {}

// 编译期断言：Noop 实现满足对应 interface。
var (
	_ EventEmitter   = NoopEmitter{}
	_ Hook           = NoopHook{}
	_ TraceCollector = NoopTraceCollector{}
	_ EventEmitter   = (*NoopEmitter)(nil)
	_ Hook           = (*NoopHook)(nil)
	_ TraceCollector = (*NoopTraceCollector)(nil)
)

// ----------------------------------------------------------------------------
// RunnerOption
// ----------------------------------------------------------------------------

// RunnerOption Runner 配置选项函数。
// 通过 functional options 模式注入 EventEmitter/Hooks/TraceCollector。
type RunnerOption func(*Runner)

// WithEmitter 注入 EventEmitter。
// emitter 为 nil 时忽略（保留默认 NoopEmitter）。
func WithEmitter(emitter EventEmitter) RunnerOption {
	return func(r *Runner) {
		if emitter != nil {
			r.emitter = emitter
		}
	}
}

// WithHooks 注入若干 Hook，按传入顺序追加到 hooks 列表。
// 重复调用会累加；nil Hook 会被跳过。
func WithHooks(hooks ...Hook) RunnerOption {
	return func(r *Runner) {
		for _, h := range hooks {
			if h == nil {
				continue
			}
			r.hooks = append(r.hooks, h)
		}
	}
}

// WithTraceCollector 注入 TraceCollector。
// collector 为 nil 时忽略（保留默认 NoopTraceCollector）。
func WithTraceCollector(collector TraceCollector) RunnerOption {
	return func(r *Runner) {
		if collector != nil {
			r.traceCollector = collector
		}
	}
}

// ----------------------------------------------------------------------------
// Runner
// ----------------------------------------------------------------------------

// Runner 集成 Orchestrator 与事件流的入口封装。
//
// 职责：
//   - 调用 Orchestrator.Recommend 执行推荐主流程
//   - 根据 response.PathTaken 发射行为事件（fast_path_hit/slow_path_hit/hybrid_merge）
//   - 顺序调用 Hooks 处理事件（错误隔离）
//   - 透传事件给 TraceCollector
//
// 字段语义：
//   - name：Runner 名称（用于日志与多实例区分，如 "http" / "grpc" / "kafka"）
//   - orchestrator：推荐编排器（domain.Orchestrator interface）
//   - emitter：事件发射器（默认 NoopEmitter）
//   - hooks：Hook 列表（按注入顺序调用）
//   - traceCollector：trace 收集器（默认 NoopTraceCollector）
//
// 二开扩展点：通过 RunnerOption 注入自定义依赖。
type Runner struct {
	// name Runner 名称。
	name string
	// orchestrator 推荐编排器。
	orchestrator domain.Orchestrator
	// emitter 事件发射器。
	emitter EventEmitter
	// hooks Hook 列表。
	hooks []Hook
	// traceCollector trace 收集器。
	traceCollector TraceCollector
}

// NewRunner 构造 Runner。
// name Runner 名称（可为空）；orchestrator 推荐编排器（不可为 nil）；
// opts 可选配置（WithEmitter/WithHooks/WithTraceCollector）。
// 返回 *Runner。未注入的依赖使用 Noop 默认实现。
func NewRunner(name string, orchestrator domain.Orchestrator, opts ...RunnerOption) *Runner {
	r := &Runner{
		name:           name,
		orchestrator:   orchestrator,
		emitter:        NoopEmitter{},
		hooks:          make([]Hook, 0),
		traceCollector: NoopTraceCollector{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// Name 返回 Runner 名称。
func (r *Runner) Name() string { return r.name }

// Run 执行推荐主流程，封装 Orchestrator 调用 + 事件发射 + Hook 处理 + Trace 收集。
//
// 流程：
//  1. 构造 RecommendRequest（UserKey.UserID=userID；sessionID 与 message 通过 Payload 透传）
//  2. 调用 orchestrator.Recommend
//  3. 根据 response.PathTaken 发射行为事件（fast_path_hit/slow_path_hit/hybrid_merge）
//  4. 顺序调用 hooks 处理事件（错误隔离，单个 Hook 失败不影响主流程与其他 Hook）
//  5. 收集事件到 TraceCollector
//  6. 返回 response（含 PathTaken/Cost/GraphTrace）
//
// 注意：RecommendRequest 无 SessionID/Query 字段，sessionID 与 message 通过
// 事件 Payload 透传给下游 Hook/TraceCollector，便于行为日志关联。
func (r *Runner) Run(ctx context.Context, userID, sessionID, message string) (domain.RecommendResponse, error) {
	if r == nil {
		return domain.RecommendResponse{}, fmt.Errorf("runner: nil receiver")
	}
	if r.orchestrator == nil {
		return domain.RecommendResponse{}, fmt.Errorf("runner: nil orchestrator")
	}
	req := domain.RecommendRequest{
		UserKey: domain.UserKey{UserID: userID},
	}
	// 通过 context 携带 sessionID 与 message，供下游 Hook/TraceCollector 关联。
	ctx = withRunnerMeta(ctx, runnerMeta{SessionID: sessionID, Message: message})
	return r.RunWithRequest(ctx, req)
}

// RunWithRequest 用完整 RecommendRequest 执行推荐主流程。
//
// 与 Run 区别：直接接收完整 request，不做字段映射，便于上层复用已构造的请求。
// 事件发射 / Hook 处理 / Trace 收集逻辑与 Run 一致。
func (r *Runner) RunWithRequest(ctx context.Context, req domain.RecommendRequest) (domain.RecommendResponse, error) {
	if r == nil {
		return domain.RecommendResponse{}, fmt.Errorf("runner: nil receiver")
	}
	if r.orchestrator == nil {
		return domain.RecommendResponse{}, fmt.Errorf("runner: nil orchestrator")
	}
	meta := runnerMetaFromCtx(ctx)
	resp, err := r.orchestrator.Recommend(ctx, req)
	if err != nil {
		// 即使失败也发射一条事件，便于失败率监控。
		failureEvent := r.buildEvent(req, resp, meta, err)
		r.fireEvent(ctx, failureEvent)
		return domain.RecommendResponse{}, fmt.Errorf("runner: orchestrator recommend: %w", err)
	}
	// 根据 PathTaken 发射对应事件。
	event := r.buildEvent(req, resp, meta, nil)
	r.fireEvent(ctx, event)
	return resp, nil
}

// fireEvent 发射事件：先 emitter.Emit，再顺序调用 hooks，最后收集 trace。
// 错误隔离：emitter/hook 失败不影响主流程与其他 Hook，仅记录到最后返回（此处忽略）。
func (r *Runner) fireEvent(ctx context.Context, event domain.BehaviorEvent) {
	if r.emitter != nil {
		// emitter 错误不影响主流程。
		_ = r.emitter.Emit(ctx, event)
	}
	for _, h := range r.hooks {
		if h == nil {
			continue
		}
		// hook 错误隔离：单个失败不阻断后续 hook。
		_ = h.OnEvent(ctx, event)
	}
	if r.traceCollector != nil {
		r.traceCollector.Collect(event)
	}
}

// buildEvent 根据请求/响应/错误构造行为事件。
//
// 事件类型映射：
//   - err != nil → "runner_error"（自定义事件，便于失败率监控）
//   - resp.PathTaken="fast" → EventFastPathHit
//   - resp.PathTaken="slow" → EventSlowPathHit
//   - resp.PathTaken="hybrid" → EventHybridMerge
//   - 其他 → EventHybridMerge（默认）
//
// Payload 携带 session_id/message/cost/candidate_count/error 等扩展字段。
func (r *Runner) buildEvent(req domain.RecommendRequest, resp domain.RecommendResponse, meta runnerMeta, err error) domain.BehaviorEvent {
	eventType := pathToEventType(resp.PathTaken)
	if err != nil {
		eventType = "runner_error"
	}
	payload := map[string]any{
		"runner_name":     r.name,
		"candidate_count": len(resp.Candidates),
		"cost":            resp.Cost,
	}
	if req.UserKey.UserID != "" {
		payload["user_id"] = req.UserKey.UserID
	}
	if req.Channel != "" {
		payload["channel"] = req.Channel
	}
	if resp.PathTaken != "" {
		payload["path_taken"] = resp.PathTaken
	}
	if resp.GraphTrace != nil {
		payload["graph_trace"] = resp.GraphTrace
	}
	if meta.SessionID != "" {
		payload["session_id"] = meta.SessionID
	}
	if meta.Message != "" {
		payload["query"] = meta.Message
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	return domain.BehaviorEvent{
		EventID:   fmt.Sprintf("runner-%d", time.Now().UnixNano()),
		EventType: eventType,
		UserID:    req.UserKey.UserID,
		Channel:   resp.Channel,
		PathTaken: resp.PathTaken,
		Timestamp: time.Now(),
		Payload:   payload,
	}
}

// pathToEventType 将 PathTaken 映射为事件类型常量。
// 未知路径默认返回 EventHybridMerge。
func pathToEventType(pathTaken string) string {
	switch pathTaken {
	case "fast":
		return domain.EventFastPathHit
	case "slow":
		return domain.EventSlowPathHit
	case "hybrid":
		return domain.EventHybridMerge
	default:
		return domain.EventHybridMerge
	}
}

// ----------------------------------------------------------------------------
// context 携带 sessionID 与 message（避免修改 RecommendRequest）
// ----------------------------------------------------------------------------

// runnerMetaKey context key 类型，避免键冲突。
type runnerMetaKey struct{}

// runnerMeta 携带 sessionID 与 message。
type runnerMeta struct {
	SessionID string
	Message   string
}

// withRunnerMeta 将 sessionID 与 message 通过 context 携带。
func withRunnerMeta(ctx context.Context, meta runnerMeta) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, runnerMetaKey{}, meta)
}

// runnerMetaFromCtx 从 context 读取 runnerMeta，无则返回零值。
func runnerMetaFromCtx(ctx context.Context) runnerMeta {
	if ctx == nil {
		return runnerMeta{}
	}
	if v, ok := ctx.Value(runnerMetaKey{}).(runnerMeta); ok {
		return v
	}
	return runnerMeta{}
}
