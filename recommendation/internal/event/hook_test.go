package event

import (
	"context"
	"errors"
	"sync"
	"testing"

	"sea/internal/domain"
)

// ============================================================================
// 该文件测试 internal/event/hook.go 的 Hook interface 体系与 Registry。
// 覆盖：多 hook 顺序调用、错误隔离、PartialHookErrors 聚合、EmitTyped 类型分发。
// ============================================================================

// recorderHook 记录 OnEvent 调用顺序的基础 Hook，用于验证顺序与错误隔离。
type recorderHook struct {
	name    string
	callSeq *[]string
	mu      *sync.Mutex
	err     error // OnEvent 返回的错误（nil 表示成功）
}

func (h *recorderHook) Name() string { return h.name }
func (h *recorderHook) OnEvent(_ context.Context, _ domain.BehaviorEvent) error {
	// mu/callSeq 可为 nil：仅用于返回错误的 hook 无需记录调用顺序。
	if h.mu != nil && h.callSeq != nil {
		h.mu.Lock()
		*h.callSeq = append(*h.callSeq, h.name)
		h.mu.Unlock()
	}
	return h.err
}

// TestRegistry_MultipleHooksSequential 验证多 hook 按 Register 顺序被调用。
func TestRegistry_MultipleHooksSequential(t *testing.T) {
	r := NewRegistry()
	var mu sync.Mutex
	var seq []string
	for _, name := range []string{"h1", "h2", "h3"} {
		if err := r.Register(&recorderHook{name: name, callSeq: &seq, mu: &mu}); err != nil {
			t.Fatalf("Register %s 失败: %v", name, err)
		}
	}
	if err := r.Emit(context.Background(), domain.BehaviorEvent{EventType: domain.EventClick}); err != nil {
		t.Fatalf("Emit 期望 nil，实际 %v", err)
	}
	want := []string{"h1", "h2", "h3"}
	if len(seq) != len(want) {
		t.Fatalf("调用顺序长度期望 %d，实际 %d（%v）", len(want), len(seq), seq)
	}
	for i, n := range want {
		if seq[i] != n {
			t.Errorf("第 %d 个调用期望 %q，实际 %q", i, n, seq[i])
		}
	}
}

// TestRegistry_ErrorIsolation 验证错误隔离：一个 hook 返回 error，
// 其他 hook 仍按顺序执行。
func TestRegistry_ErrorIsolation(t *testing.T) {
	r := NewRegistry()
	var mu sync.Mutex
	var seq []string
	hooks := []*recorderHook{
		{name: "ok1", callSeq: &seq, mu: &mu},
		{name: "fail", callSeq: &seq, mu: &mu, err: errors.New("boom")},
		{name: "ok2", callSeq: &seq, mu: &mu},
	}
	for _, h := range hooks {
		if err := r.Register(h); err != nil {
			t.Fatalf("Register %s 失败: %v", h.name, err)
		}
	}
	err := r.Emit(context.Background(), domain.BehaviorEvent{EventType: domain.EventClick})
	if err == nil {
		t.Fatal("Emit 期望返回错误，实际 nil")
	}
	// 三个 hook 都应被调用（错误隔离）。
	if len(seq) != 3 {
		t.Errorf("期望 3 个 hook 全部执行，实际 %d（%v）", len(seq), seq)
	}
	want := []string{"ok1", "fail", "ok2"}
	for i, n := range want {
		if seq[i] != n {
			t.Errorf("第 %d 个调用期望 %q，实际 %q", i, n, seq[i])
		}
	}
}

// TestRegistry_PartialHookErrorsAggregation 验证 PartialHookErrors 聚合多个错误。
func TestRegistry_PartialHookErrorsAggregation(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&recorderHook{name: "fail1", err: errors.New("e1")}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&recorderHook{name: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&recorderHook{name: "fail2", err: errors.New("e2")}); err != nil {
		t.Fatal(err)
	}
	err := r.Emit(context.Background(), domain.BehaviorEvent{EventType: domain.EventClick})
	if err == nil {
		t.Fatal("期望错误，实际 nil")
	}
	var partial PartialHookErrors
	if !errors.As(err, &partial) {
		t.Fatalf("期望 PartialHookErrors 类型，实际 %T（%v）", err, err)
	}
	if len(partial.Errors) != 2 {
		t.Errorf("期望 2 个聚合错误，实际 %d", len(partial.Errors))
	}
	// 验证 Unwrap 返回切片。
	if errs := partial.Unwrap(); len(errs) != 2 {
		t.Errorf("Unwrap 期望 2 个错误，实际 %d", len(errs))
	}
	// 验证错误信息包含数量。
	if msg := partial.Error(); msg == "" {
		t.Error("Error() 期望非空")
	}
}

// TestRegistry_NoErrorReturnsNil 验证全部 hook 成功时 Emit 返回 nil。
func TestRegistry_NoErrorReturnsNil(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&recorderHook{name: "ok1"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&recorderHook{name: "ok2"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Emit(context.Background(), domain.BehaviorEvent{EventType: domain.EventClick}); err != nil {
		t.Errorf("全部成功时期望 nil，实际 %v", err)
	}
}

// TestRegistry_RegisterNilHook 验证注册 nil hook 返回错误。
func TestRegistry_RegisterNilHook(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Error("Register(nil) 期望返回错误，实际 nil")
	}
}

// TestRegistry_SatisfiesInterfaces 验证 Registry 同时实现
// domain.HookRegistry 与 domain.EventEmitter interface。
func TestRegistry_SatisfiesInterfaces(t *testing.T) {
	r := NewRegistry()
	var _ domain.HookRegistry = r
	var _ domain.EventEmitter = r
}

// ----------------------------------------------------------------------------
// EmitTyped 类型分发测试
// ----------------------------------------------------------------------------

// toolCallRecorder 同时实现基础 Hook 与 ToolCallHook，记录各方法调用。
type toolCallRecorder struct {
	name       string
	onEvent    int
	onToolCall int
	mu         sync.Mutex
	err        error
}

func (h *toolCallRecorder) Name() string { return h.name }
func (h *toolCallRecorder) OnEvent(_ context.Context, _ domain.BehaviorEvent) error {
	h.mu.Lock()
	h.onEvent++
	h.mu.Unlock()
	return nil
}
func (h *toolCallRecorder) OnToolCall(_ context.Context, _ ToolCallEvent) error {
	h.mu.Lock()
	h.onToolCall++
	h.mu.Unlock()
	return h.err
}

// TestEmitTyped_ToolCallDispatch 验证注册 ToolCallHook 后，
// emit ToolCallEvent 调用 OnToolCall（而非 OnEvent）。
func TestEmitTyped_ToolCallDispatch(t *testing.T) {
	r := NewRegistry()
	rec := &toolCallRecorder{name: "tool-hook"}
	if err := r.Register(rec); err != nil {
		t.Fatal(err)
	}
	if err := r.EmitTyped(context.Background(), domain.BehaviorEvent{
		EventType: domain.EventToolCall,
		UserID:    "u-1",
	}); err != nil {
		t.Fatalf("EmitTyped 期望 nil，实际 %v", err)
	}
	if rec.onToolCall != 1 {
		t.Errorf("OnToolCall 期望调用 1 次，实际 %d 次", rec.onToolCall)
	}
	// 实现了 ToolCallHook 时不应回退调用 OnEvent。
	if rec.onEvent != 0 {
		t.Errorf("OnEvent 期望调用 0 次（已分发到 OnToolCall），实际 %d 次", rec.onEvent)
	}
}

// TestEmitTyped_FallbackToOnEvent 验证未实现扩展 interface 的 Hook
// 回退调用 OnEvent。
func TestEmitTyped_FallbackToOnEvent(t *testing.T) {
	r := NewRegistry()
	var mu sync.Mutex
	var seq []string
	base := &recorderHook{name: "base", callSeq: &seq, mu: &mu}
	if err := r.Register(base); err != nil {
		t.Fatal(err)
	}
	// 发射 tool_call 事件，但 base 仅实现基础 Hook，应回退 OnEvent。
	if err := r.EmitTyped(context.Background(), domain.BehaviorEvent{
		EventType: domain.EventToolCall,
	}); err != nil {
		t.Fatalf("EmitTyped 期望 nil，实际 %v", err)
	}
	if len(seq) != 1 || seq[0] != "base" {
		t.Errorf("期望回退调用 base.OnEvent，实际 %v", seq)
	}
}

// TestEmitTyped_RecommendDispatch 验证 RecommendHook 分发（fast_path_hit 事件）。
func TestEmitTyped_RecommendDispatch(t *testing.T) {
	rec := &recommendRecorder{name: "rec-hook"}
	r := NewRegistry()
	if err := r.Register(rec); err != nil {
		t.Fatal(err)
	}
	if err := r.EmitTyped(context.Background(), domain.BehaviorEvent{
		EventType: domain.EventFastPathHit,
	}); err != nil {
		t.Fatalf("EmitTyped 期望 nil，实际 %v", err)
	}
	if rec.onRecommend != 1 {
		t.Errorf("OnRecommend 期望调用 1 次，实际 %d 次", rec.onRecommend)
	}
	if rec.onEvent != 0 {
		t.Errorf("OnEvent 期望 0 次，实际 %d 次", rec.onEvent)
	}
}

// recommendRecorder 实现 RecommendHook。
type recommendRecorder struct {
	name        string
	onEvent     int
	onRecommend int
	err         error
}

func (h *recommendRecorder) Name() string { return h.name }
func (h *recommendRecorder) OnEvent(_ context.Context, _ domain.BehaviorEvent) error {
	h.onEvent++
	return nil
}
func (h *recommendRecorder) OnRecommend(_ context.Context, _ RecommendEvent) error {
	h.onRecommend++
	return h.err
}

// TestEmitTyped_GraphQueryDispatch 验证 GraphQueryHook 分发（graph_query 与 cypher_generated 事件）。
func TestEmitTyped_GraphQueryDispatch(t *testing.T) {
	rec := &graphRecorder{name: "graph-hook"}
	r := NewRegistry()
	if err := r.Register(rec); err != nil {
		t.Fatal(err)
	}
	for _, et := range []string{domain.EventGraphQuery, domain.EventCypherGenerated} {
		if err := r.EmitTyped(context.Background(), domain.BehaviorEvent{EventType: et}); err != nil {
			t.Fatalf("EmitTyped(%q) 期望 nil，实际 %v", et, err)
		}
	}
	if rec.onGraph != 2 {
		t.Errorf("OnGraphQuery 期望调用 2 次，实际 %d 次", rec.onGraph)
	}
}

// graphRecorder 实现 GraphQueryHook。
type graphRecorder struct {
	name    string
	onEvent int
	onGraph int
	err     error
}

func (h *graphRecorder) Name() string { return h.name }
func (h *graphRecorder) OnEvent(_ context.Context, _ domain.BehaviorEvent) error {
	h.onEvent++
	return nil
}
func (h *graphRecorder) OnGraphQuery(_ context.Context, _ GraphEvent) error {
	h.onGraph++
	return h.err
}

// TestEmitTyped_ErrorIsolation 验证 EmitTyped 的错误隔离：
// 一个扩展 Hook 失败不影响其他 Hook 执行，且聚合为 PartialHookErrors。
func TestEmitTyped_ErrorIsolation(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&toolCallRecorder{name: "fail", err: errors.New("tool-err")}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seq []string
	if err := r.Register(&recorderHook{name: "base", callSeq: &seq, mu: &mu}); err != nil {
		t.Fatal(err)
	}
	err := r.EmitTyped(context.Background(), domain.BehaviorEvent{
		EventType: domain.EventToolCall,
	})
	if err == nil {
		t.Fatal("EmitTyped 期望返回错误，实际 nil")
	}
	var partial PartialHookErrors
	if !errors.As(err, &partial) {
		t.Fatalf("期望 PartialHookErrors，实际 %T", err)
	}
	if len(partial.Errors) != 1 {
		t.Errorf("期望 1 个聚合错误，实际 %d", len(partial.Errors))
	}
	// base hook 仍应被调用（回退 OnEvent）。
	if len(seq) != 1 || seq[0] != "base" {
		t.Errorf("期望 base 仍被调用（错误隔离），实际 %v", seq)
	}
}

// TestInvoke_DelegatesToEmit 验证 Invoke（domain.HookRegistry）委托 Emit。
func TestInvoke_DelegatesToEmit(t *testing.T) {
	r := NewRegistry()
	var mu sync.Mutex
	var seq []string
	if err := r.Register(&recorderHook{name: "h", callSeq: &seq, mu: &mu}); err != nil {
		t.Fatal(err)
	}
	if err := r.Invoke(context.Background(), domain.BehaviorEvent{EventType: domain.EventClick}); err != nil {
		t.Fatalf("Invoke 期望 nil，实际 %v", err)
	}
	if len(seq) != 1 {
		t.Errorf("Invoke 期望触发 1 次调用，实际 %d", len(seq))
	}
}

// TestRegistry_ConcurrentSafe 验证 Register 与 Emit 并发安全（基础冒烟）。
func TestRegistry_ConcurrentSafe(t *testing.T) {
	r := NewRegistry()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = r.Emit(context.Background(), domain.BehaviorEvent{EventType: domain.EventClick})
		}
	}()
	for i := 0; i < 50; i++ {
		_ = r.Register(&recorderHook{name: "h"})
	}
	<-done
}
