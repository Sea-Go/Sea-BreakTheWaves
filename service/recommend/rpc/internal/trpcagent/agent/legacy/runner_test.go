// Package agent runner_test.go — Runner 单元测试（Task 12.2）。
//
// 该文件用 stdlib 手写 stub（不引入 testify）覆盖 Runner 的：
//   - 事件发射（emitter.Emit 被调用，事件类型与 PathTaken 对应）
//   - hooks 顺序处理（按注入顺序调用，错误隔离不影响其他 Hook）
//   - trace 收集（TraceCollector.Collect 被调用）
//   - PathTaken 透传（fast/slow/hybrid 三种路径）
//   - RunWithRequest（直接用完整 request 调用）
//   - 错误场景（nil orchestrator / orchestrator 失败仍发射事件）
package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// stub 实现
// ----------------------------------------------------------------------------

// stubOrchestratorRunner 测试用 Orchestrator stub（实现 domain.Orchestrator）。
type stubOrchestratorRunner struct {
	resp    domain.RecommendResponse
	err     error
	calls   int
	lastReq domain.RecommendRequest
	mu      sync.Mutex
}

func (s *stubOrchestratorRunner) Recommend(_ context.Context, req domain.RecommendRequest) (domain.RecommendResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastReq = req
	return s.resp, s.err
}

// captureEmitter 测试用 EventEmitter，记录所有发射的事件。
type captureEmitter struct {
	mu      sync.Mutex
	events  []domain.BehaviorEvent
	emitErr error
}

func (c *captureEmitter) Emit(_ context.Context, event domain.BehaviorEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return c.emitErr
}

// recordingHook 测试用 Hook，记录调用顺序与事件。
type recordingHook struct {
	name      string
	callOrder *[]string
	mu        *sync.Mutex
	events    *[]domain.BehaviorEvent
	err       error
}

func (h *recordingHook) Name() string { return h.name }

func (h *recordingHook) OnEvent(_ context.Context, event domain.BehaviorEvent) error {
	if h.mu != nil {
		h.mu.Lock()
		defer h.mu.Unlock()
	}
	if h.callOrder != nil {
		*h.callOrder = append(*h.callOrder, h.name)
	}
	if h.events != nil {
		*h.events = append(*h.events, event)
	}
	return h.err
}

// captureTraceCollector 测试用 TraceCollector，记录所有收集的事件。
type captureTraceCollector struct {
	mu     sync.Mutex
	events []domain.BehaviorEvent
}

func (c *captureTraceCollector) Collect(event domain.BehaviorEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

// ----------------------------------------------------------------------------
// 测试用例
// ----------------------------------------------------------------------------

// TestNewRunner_Defaults 验证未注入依赖时使用 Noop 默认实现。
func TestNewRunner_Defaults(t *testing.T) {
	orch := &stubOrchestratorRunner{}
	r := NewRunner("test", orch)
	if r.Name() != "test" {
		t.Errorf("Name = %q, 期望 test", r.Name())
	}
	if r.emitter == nil {
		t.Errorf("emitter 不应为 nil（默认 NoopEmitter）")
	}
	if _, ok := r.emitter.(NoopEmitter); !ok {
		t.Errorf("emitter 类型 = %T, 期望 NoopEmitter", r.emitter)
	}
	if _, ok := r.traceCollector.(NoopTraceCollector); !ok {
		t.Errorf("traceCollector 类型 = %T, 期望 NoopTraceCollector", r.traceCollector)
	}
	if len(r.hooks) != 0 {
		t.Errorf("hooks 长度 = %d, 期望 0", len(r.hooks))
	}
}

// TestRunner_PathTakenFast 验证 fast 路径事件发射与 PathTaken 透传。
func TestRunner_PathTakenFast(t *testing.T) {
	orch := &stubOrchestratorRunner{
		resp: domain.RecommendResponse{
			PathTaken:  "fast",
			Channel:    "home",
			Candidates: []domain.Candidate{{ArticleID: "a1"}},
		},
	}
	emitter := &captureEmitter{}
	tracer := &captureTraceCollector{}
	r := NewRunner("test", orch, WithEmitter(emitter), WithTraceCollector(tracer))

	resp, err := r.Run(context.Background(), "u1", "s1", "hello")
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if resp.PathTaken != "fast" {
		t.Errorf("resp.PathTaken = %q, 期望 fast", resp.PathTaken)
	}
	// 验证 emitter 收到 1 条事件，类型为 fast_path_hit。
	if len(emitter.events) != 1 {
		t.Fatalf("emitter 事件数 = %d, 期望 1", len(emitter.events))
	}
	ev := emitter.events[0]
	if ev.EventType != domain.EventFastPathHit {
		t.Errorf("EventType = %q, 期望 %q", ev.EventType, domain.EventFastPathHit)
	}
	if ev.PathTaken != "fast" {
		t.Errorf("event.PathTaken = %q, 期望 fast", ev.PathTaken)
	}
	if ev.UserID != "u1" {
		t.Errorf("UserID = %q, 期望 u1", ev.UserID)
	}
	// 验证 Payload 含 session_id 与 query。
	if v, _ := ev.Payload["session_id"].(string); v != "s1" {
		t.Errorf("Payload[session_id] = %q, 期望 s1", v)
	}
	if v, _ := ev.Payload["query"].(string); v != "hello" {
		t.Errorf("Payload[query] = %q, 期望 hello", v)
	}
	// 验证 trace collector 收到事件。
	if len(tracer.events) != 1 {
		t.Fatalf("tracer 事件数 = %d, 期望 1", len(tracer.events))
	}
	if tracer.events[0].EventType != domain.EventFastPathHit {
		t.Errorf("trace EventType = %q, 期望 %q", tracer.events[0].EventType, domain.EventFastPathHit)
	}
}

// TestRunner_PathTakenSlow 验证 slow 路径事件类型为 slow_path_hit。
func TestRunner_PathTakenSlow(t *testing.T) {
	orch := &stubOrchestratorRunner{
		resp: domain.RecommendResponse{
			PathTaken:  "slow",
			Candidates: []domain.Candidate{{ArticleID: "a1"}},
		},
	}
	emitter := &captureEmitter{}
	r := NewRunner("test", orch, WithEmitter(emitter))

	_, err := r.Run(context.Background(), "u1", "s1", "对比 A vs B")
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if len(emitter.events) != 1 {
		t.Fatalf("emitter 事件数 = %d, 期望 1", len(emitter.events))
	}
	if emitter.events[0].EventType != domain.EventSlowPathHit {
		t.Errorf("EventType = %q, 期望 %q", emitter.events[0].EventType, domain.EventSlowPathHit)
	}
}

// TestRunner_PathTakenHybrid 验证 hybrid 路径事件类型为 hybrid_merge。
func TestRunner_PathTakenHybrid(t *testing.T) {
	orch := &stubOrchestratorRunner{
		resp: domain.RecommendResponse{PathTaken: "hybrid"},
	}
	emitter := &captureEmitter{}
	r := NewRunner("test", orch, WithEmitter(emitter))

	_, err := r.Run(context.Background(), "u1", "s1", "推荐")
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if len(emitter.events) != 1 {
		t.Fatalf("emitter 事件数 = %d, 期望 1", len(emitter.events))
	}
	if emitter.events[0].EventType != domain.EventHybridMerge {
		t.Errorf("EventType = %q, 期望 %q", emitter.events[0].EventType, domain.EventHybridMerge)
	}
}

// TestRunner_HooksOrder 验证 hooks 按注入顺序调用。
func TestRunner_HooksOrder(t *testing.T) {
	orch := &stubOrchestratorRunner{
		resp: domain.RecommendResponse{PathTaken: "fast"},
	}
	var order []string
	var mu sync.Mutex
	hook1 := &recordingHook{name: "h1", callOrder: &order, mu: &mu}
	hook2 := &recordingHook{name: "h2", callOrder: &order, mu: &mu}
	hook3 := &recordingHook{name: "h3", callOrder: &order, mu: &mu}
	r := NewRunner("test", orch, WithHooks(hook1, hook2, hook3))

	_, err := r.Run(context.Background(), "u1", "s1", "msg")
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	want := []string{"h1", "h2", "h3"}
	if len(order) != len(want) {
		t.Fatalf("hooks 调用次数 = %d, 期望 %d", len(order), len(want))
	}
	for i, name := range want {
		if order[i] != name {
			t.Errorf("调用顺序[%d] = %q, 期望 %q", i, order[i], name)
		}
	}
}

// TestRunner_HookErrorIsolation 验证 hook 错误隔离：单个 hook 失败不影响其他 hook。
func TestRunner_HookErrorIsolation(t *testing.T) {
	orch := &stubOrchestratorRunner{
		resp: domain.RecommendResponse{PathTaken: "fast"},
	}
	var order []string
	var mu sync.Mutex
	// h1 返回错误，h2/h3 仍应被调用。
	h1 := &recordingHook{name: "h1", callOrder: &order, mu: &mu, err: errors.New("h1 error")}
	h2 := &recordingHook{name: "h2", callOrder: &order, mu: &mu}
	h3 := &recordingHook{name: "h3", callOrder: &order, mu: &mu}
	r := NewRunner("test", orch, WithHooks(h1, h2, h3))

	_, err := r.Run(context.Background(), "u1", "s1", "msg")
	if err != nil {
		t.Fatalf("Run 不应失败（hook 错误隔离）: %v", err)
	}
	want := []string{"h1", "h2", "h3"}
	if len(order) != len(want) {
		t.Fatalf("hooks 调用次数 = %d, 期望 %d（h1 失败也应记录）", len(order), len(want))
	}
	for i, name := range want {
		if order[i] != name {
			t.Errorf("调用顺序[%d] = %q, 期望 %q", i, order[i], name)
		}
	}
}

// TestRunner_TraceCollected 验证 trace 收集到 TraceCollector。
func TestRunner_TraceCollected(t *testing.T) {
	orch := &stubOrchestratorRunner{
		resp: domain.RecommendResponse{
			PathTaken: "slow",
			Candidates: []domain.Candidate{
				{ArticleID: "a1"},
				{ArticleID: "a2"},
			},
			Cost: domain.CostReport{LLMCalls: 2, TokensIn: 200},
		},
	}
	emitter := &captureEmitter{}
	tracer := &captureTraceCollector{}
	r := NewRunner("test", orch, WithEmitter(emitter), WithTraceCollector(tracer))

	_, err := r.Run(context.Background(), "u1", "s1", "msg")
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if len(tracer.events) != 1 {
		t.Fatalf("trace 事件数 = %d, 期望 1", len(tracer.events))
	}
	ev := tracer.events[0]
	if ev.EventType != domain.EventSlowPathHit {
		t.Errorf("trace EventType = %q, 期望 %q", ev.EventType, domain.EventSlowPathHit)
	}
	// 验证 Payload 含 cost 与 candidate_count。
	if cnt, _ := ev.Payload["candidate_count"].(int); cnt != 2 {
		t.Errorf("Payload[candidate_count] = %v, 期望 2", ev.Payload["candidate_count"])
	}
}

// TestRunner_RunWithRequest 验证直接用完整 request 调用。
func TestRunner_RunWithRequest(t *testing.T) {
	orch := &stubOrchestratorRunner{
		resp: domain.RecommendResponse{PathTaken: "fast", Channel: "tech"},
	}
	emitter := &captureEmitter{}
	r := NewRunner("test", orch, WithEmitter(emitter))

	req := domain.RecommendRequest{
		UserKey: domain.UserKey{UserID: "u2"},
		Channel: "tech",
		TopK:    5,
	}
	resp, err := r.RunWithRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("RunWithRequest 错误: %v", err)
	}
	if resp.PathTaken != "fast" {
		t.Errorf("PathTaken = %q, 期望 fast", resp.PathTaken)
	}
	// 验证 orchestrator 收到完整 request。
	if orch.lastReq.UserKey.UserID != "u2" {
		t.Errorf("orchestrator 收到 UserID = %q, 期望 u2", orch.lastReq.UserKey.UserID)
	}
	if orch.lastReq.Channel != "tech" {
		t.Errorf("orchestrator 收到 Channel = %q, 期望 tech", orch.lastReq.Channel)
	}
	if orch.lastReq.TopK != 5 {
		t.Errorf("orchestrator 收到 TopK = %d, 期望 5", orch.lastReq.TopK)
	}
	// 验证事件 Channel 字段。
	if len(emitter.events) != 1 {
		t.Fatalf("emitter 事件数 = %d, 期望 1", len(emitter.events))
	}
	if emitter.events[0].Channel != "tech" {
		t.Errorf("event.Channel = %q, 期望 tech", emitter.events[0].Channel)
	}
}

// TestRunner_OrchestratorError 验证 orchestrator 失败时返回 error 且仍发射事件。
func TestRunner_OrchestratorError(t *testing.T) {
	orch := &stubOrchestratorRunner{
		err: errors.New("orchestrator down"),
	}
	emitter := &captureEmitter{}
	tracer := &captureTraceCollector{}
	r := NewRunner("test", orch, WithEmitter(emitter), WithTraceCollector(tracer))

	_, err := r.Run(context.Background(), "u1", "s1", "msg")
	if err == nil {
		t.Fatalf("orchestrator 失败时应返回 error")
	}
	// 验证仍发射了 1 条事件（runner_error）。
	if len(emitter.events) != 1 {
		t.Fatalf("emitter 事件数 = %d, 期望 1（失败也发射）", len(emitter.events))
	}
	if emitter.events[0].EventType != "runner_error" {
		t.Errorf("EventType = %q, 期望 runner_error", emitter.events[0].EventType)
	}
	// 验证 trace 也收集了。
	if len(tracer.events) != 1 {
		t.Fatalf("trace 事件数 = %d, 期望 1", len(tracer.events))
	}
}

// TestRunner_NilOrchestrator 验证 nil orchestrator 返回错误。
func TestRunner_NilOrchestrator(t *testing.T) {
	r := NewRunner("test", nil)
	_, err := r.Run(context.Background(), "u1", "s1", "msg")
	if err == nil {
		t.Fatalf("nil orchestrator 应返回 error")
	}
	_, err = r.RunWithRequest(context.Background(), domain.RecommendRequest{})
	if err == nil {
		t.Fatalf("nil orchestrator RunWithRequest 应返回 error")
	}
}

// TestRunner_NilReceiver 验证 nil receiver 安全处理。
func TestRunner_NilReceiver(t *testing.T) {
	var r *Runner
	_, err := r.Run(context.Background(), "u1", "s1", "msg")
	if err == nil {
		t.Errorf("nil receiver Run 应返回 error")
	}
	_, err = r.RunWithRequest(context.Background(), domain.RecommendRequest{})
	if err == nil {
		t.Errorf("nil receiver RunWithRequest 应返回 error")
	}
}

// TestRunner_WithHooksNilSkip 验证 WithHooks 跳过 nil Hook。
func TestRunner_WithHooksNilSkip(t *testing.T) {
	orch := &stubOrchestratorRunner{
		resp: domain.RecommendResponse{PathTaken: "fast"},
	}
	h1 := &recordingHook{name: "h1"}
	r := NewRunner("test", orch, WithHooks(h1, nil, nil))
	if len(r.hooks) != 1 {
		t.Errorf("hooks 长度 = %d, 期望 1（nil 应被跳过）", len(r.hooks))
	}
}

// TestRunner_WithEmitterNilIgnore 验证 WithEmitter(nil) 忽略，保留默认。
func TestRunner_WithEmitterNilIgnore(t *testing.T) {
	orch := &stubOrchestratorRunner{}
	r := NewRunner("test", orch, WithEmitter(nil))
	if _, ok := r.emitter.(NoopEmitter); !ok {
		t.Errorf("emitter 类型 = %T, 期望 NoopEmitter（nil 应被忽略）", r.emitter)
	}
}

// TestRunner_WithTraceCollectorNilIgnore 验证 WithTraceCollector(nil) 忽略，保留默认。
func TestRunner_WithTraceCollectorNilIgnore(t *testing.T) {
	orch := &stubOrchestratorRunner{}
	r := NewRunner("test", orch, WithTraceCollector(nil))
	if _, ok := r.traceCollector.(NoopTraceCollector); !ok {
		t.Errorf("traceCollector 类型 = %T, 期望 NoopTraceCollector（nil 应被忽略）", r.traceCollector)
	}
}

// TestPathToEventType 验证 PathTaken 到事件类型的映射。
func TestPathToEventType(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"fast", domain.EventFastPathHit},
		{"slow", domain.EventSlowPathHit},
		{"hybrid", domain.EventHybridMerge},
		{"", domain.EventHybridMerge},        // 默认
		{"unknown", domain.EventHybridMerge}, // 未知默认
	}
	for _, c := range cases {
		got := pathToEventType(c.path)
		if got != c.want {
			t.Errorf("pathToEventType(%q) = %q, 期望 %q", c.path, got, c.want)
		}
	}
}

// TestNoopImplementations 验证 Noop 实现不 panic 且行为正确。
func TestNoopImplementations(t *testing.T) {
	emitter := NoopEmitter{}
	if err := emitter.Emit(context.Background(), domain.BehaviorEvent{}); err != nil {
		t.Errorf("NoopEmitter.Emit 返回 error: %v", err)
	}
	hook := NoopHook{}
	if name := hook.Name(); name != "noop" {
		t.Errorf("NoopHook.Name = %q, 期望 noop", name)
	}
	if err := hook.OnEvent(context.Background(), domain.BehaviorEvent{}); err != nil {
		t.Errorf("NoopHook.OnEvent 返回 error: %v", err)
	}
	// NoopTraceCollector.Collect 不应 panic。
	collector := NoopTraceCollector{}
	collector.Collect(domain.BehaviorEvent{})
}

// TestRunner_HooksReceiveSameEvent 验证所有 hooks 收到同一条事件。
func TestRunner_HooksReceiveSameEvent(t *testing.T) {
	orch := &stubOrchestratorRunner{
		resp: domain.RecommendResponse{PathTaken: "fast"},
	}
	var mu sync.Mutex
	var collected []domain.BehaviorEvent
	collectEvent := func() *recordingHook {
		return &recordingHook{
			name:   "collector",
			mu:     &mu,
			events: &collected,
		}
	}
	r := NewRunner("test", orch, WithHooks(collectEvent(), collectEvent(), collectEvent()))

	_, err := r.Run(context.Background(), "u1", "s1", "msg")
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if len(collected) != 3 {
		t.Fatalf("collected 事件数 = %d, 期望 3", len(collected))
	}
	// 验证 3 个 hook 收到的事件 EventType 相同。
	for i, ev := range collected {
		if ev.EventType != domain.EventFastPathHit {
			t.Errorf("hook[%d] EventType = %q, 期望 %q", i, ev.EventType, domain.EventFastPathHit)
		}
	}
}

// TestRunner_EmitterErrorIgnored 验证 emitter.Emit 失败不影响主流程。
func TestRunner_EmitterErrorIgnored(t *testing.T) {
	orch := &stubOrchestratorRunner{
		resp: domain.RecommendResponse{PathTaken: "fast"},
	}
	emitter := &captureEmitter{emitErr: errors.New("emitter down")}
	r := NewRunner("test", orch, WithEmitter(emitter))

	_, err := r.Run(context.Background(), "u1", "s1", "msg")
	if err != nil {
		t.Fatalf("emitter 失败不应影响主流程: %v", err)
	}
	// 验证事件仍被记录（emitter.Emit 返回 error 前已 append）。
	if len(emitter.events) != 1 {
		t.Errorf("emitter 事件数 = %d, 期望 1", len(emitter.events))
	}
}
