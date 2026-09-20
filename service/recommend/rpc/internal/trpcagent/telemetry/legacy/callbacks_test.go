package obs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// CallbackAdapter 单测（Task 5.4）
//
// 用 mock EventEmitter 测试 3 个回调方法发射正确事件。
// 事件类型常量引用 domain.EventXxx，确保与 Registry.EmitTyped 分发一致。
// ============================================================================

// mockEmitter 记录 Emit 调用的事件。
type mockEmitter struct {
	events []domain.BehaviorEvent
	err    error
}

func (m *mockEmitter) Emit(ctx context.Context, event domain.BehaviorEvent) error {
	if m.err != nil {
		return m.err
	}
	m.events = append(m.events, event)
	return nil
}

// quietLogger 返回静默 logger，避免污染测试输出。
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ----------------------------------------------------------------------------
// 构造测试
// ----------------------------------------------------------------------------

func TestNewCallbackAdapter_NilLogger_Default(t *testing.T) {
	a := NewCallbackAdapter(&mockEmitter{}, nil)
	if a == nil {
		t.Fatal("NewCallbackAdapter returned nil")
	}
	if a.logger == nil {
		t.Fatal("logger should default to slog.Default() when nil")
	}
}

// ----------------------------------------------------------------------------
// OnToolCall 测试
// ----------------------------------------------------------------------------

func TestOnToolCall_EmitsToolCallEvent(t *testing.T) {
	emitter := &mockEmitter{}
	a := NewCallbackAdapter(emitter, quietLogger())
	ctx := context.Background()

	err := a.OnToolCall(ctx, "recall.content", map[string]any{"q": "golang"}, []string{"a1", "a2"})
	if err != nil {
		t.Fatalf("OnToolCall error: %v", err)
	}
	if len(emitter.events) != 1 {
		t.Fatalf("emitted events = %d, want 1", len(emitter.events))
	}
	ev := emitter.events[0]
	if ev.EventType != domain.EventToolCall {
		t.Fatalf("EventType = %q, want %q", ev.EventType, domain.EventToolCall)
	}
	if ev.EventID == "" {
		t.Fatal("EventID should not be empty")
	}
	if ev.Payload["tool_name"] != "recall.content" {
		t.Fatalf("tool_name = %v, want recall.content", ev.Payload["tool_name"])
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("Timestamp should be set")
	}
}

// ----------------------------------------------------------------------------
// OnLLMCall 测试
// ----------------------------------------------------------------------------

func TestOnLLMCall_EmitsLLMCallEvent(t *testing.T) {
	emitter := &mockEmitter{}
	a := NewCallbackAdapter(emitter, quietLogger())
	ctx := context.Background()

	err := a.OnLLMCall(ctx, "gpt-4", "hello", "world", 42)
	if err != nil {
		t.Fatalf("OnLLMCall error: %v", err)
	}
	if len(emitter.events) != 1 {
		t.Fatalf("emitted events = %d, want 1", len(emitter.events))
	}
	ev := emitter.events[0]
	if ev.EventType != domain.EventLLMCall {
		t.Fatalf("EventType = %q, want %q", ev.EventType, domain.EventLLMCall)
	}
	if ev.Payload["model"] != "gpt-4" {
		t.Fatalf("model = %v, want gpt-4", ev.Payload["model"])
	}
	if ev.Payload["prompt"] != "hello" {
		t.Fatalf("prompt = %v, want hello", ev.Payload["prompt"])
	}
	if ev.Payload["response"] != "world" {
		t.Fatalf("response = %v, want world", ev.Payload["response"])
	}
	if ev.Payload["tokens_used"] != 42 {
		t.Fatalf("tokens_used = %v, want 42", ev.Payload["tokens_used"])
	}
}

// ----------------------------------------------------------------------------
// OnAgentEnd 测试
// ----------------------------------------------------------------------------

func TestOnAgentEnd_EmitsAgentEndEvent(t *testing.T) {
	emitter := &mockEmitter{}
	a := NewCallbackAdapter(emitter, quietLogger())
	ctx := context.Background()

	result := map[string]any{"candidates": []string{"a1"}}
	err := a.OnAgentEnd(ctx, "OrchestratorAgent", result)
	if err != nil {
		t.Fatalf("OnAgentEnd error: %v", err)
	}
	if len(emitter.events) != 1 {
		t.Fatalf("emitted events = %d, want 1", len(emitter.events))
	}
	ev := emitter.events[0]
	if ev.EventType != domain.EventAgentEnd {
		t.Fatalf("EventType = %q, want %q", ev.EventType, domain.EventAgentEnd)
	}
	if ev.Payload["agent_name"] != "OrchestratorAgent" {
		t.Fatalf("agent_name = %v, want OrchestratorAgent", ev.Payload["agent_name"])
	}
}

// ----------------------------------------------------------------------------
// nil emitter 测试
// ----------------------------------------------------------------------------

func TestCallbackAdapter_NilEmitter_NoPanic(t *testing.T) {
	a := NewCallbackAdapter(nil, quietLogger())
	ctx := context.Background()

	// 三个回调在 emitter 为 nil 时不应 panic，应返回 nil
	if err := a.OnToolCall(ctx, "t", nil, nil); err != nil {
		t.Fatalf("OnToolCall error: %v", err)
	}
	if err := a.OnLLMCall(ctx, "m", "p", "r", 0); err != nil {
		t.Fatalf("OnLLMCall error: %v", err)
	}
	if err := a.OnAgentEnd(ctx, "n", nil); err != nil {
		t.Fatalf("OnAgentEnd error: %v", err)
	}
}

// ----------------------------------------------------------------------------
// emitter 错误传播测试
// ----------------------------------------------------------------------------

func TestCallbackAdapter_EmitterError_Propagated(t *testing.T) {
	expected := errors.New("emit failed")
	emitter := &mockEmitter{err: expected}
	a := NewCallbackAdapter(emitter, quietLogger())
	ctx := context.Background()

	err := a.OnToolCall(ctx, "t", nil, nil)
	if !errors.Is(err, expected) {
		t.Fatalf("OnToolCall err = %v, want %v", err, expected)
	}
	if len(emitter.events) != 0 {
		t.Fatalf("events = %d, want 0 on error", len(emitter.events))
	}
}

// ----------------------------------------------------------------------------
// 事件 ID 唯一性测试
// ----------------------------------------------------------------------------

func TestCallbackAdapter_EventIDUnique(t *testing.T) {
	emitter := &mockEmitter{}
	a := NewCallbackAdapter(emitter, quietLogger())
	ctx := context.Background()

	_ = a.OnToolCall(ctx, "t", nil, nil)
	_ = a.OnLLMCall(ctx, "m", "p", "r", 0)
	_ = a.OnAgentEnd(ctx, "n", nil)

	if len(emitter.events) != 3 {
		t.Fatalf("emitted events = %d, want 3", len(emitter.events))
	}
	ids := map[string]bool{}
	for _, ev := range emitter.events {
		if ev.EventID == "" {
			t.Fatal("EventID should not be empty")
		}
		if ids[ev.EventID] {
			t.Fatalf("duplicate EventID: %s", ev.EventID)
		}
		ids[ev.EventID] = true
	}
}

// ----------------------------------------------------------------------------
// 事件类型分别正确测试
// ----------------------------------------------------------------------------

func TestCallbackAdapter_AllEventTypes(t *testing.T) {
	emitter := &mockEmitter{}
	a := NewCallbackAdapter(emitter, quietLogger())
	ctx := context.Background()

	_ = a.OnToolCall(ctx, "t", nil, nil)
	_ = a.OnLLMCall(ctx, "m", "p", "r", 0)
	_ = a.OnAgentEnd(ctx, "n", nil)

	if len(emitter.events) != 3 {
		t.Fatalf("emitted events = %d, want 3", len(emitter.events))
	}
	want := []string{domain.EventToolCall, domain.EventLLMCall, domain.EventAgentEnd}
	for i, w := range want {
		if emitter.events[i].EventType != w {
			t.Fatalf("events[%d].EventType = %q, want %q", i, emitter.events[i].EventType, w)
		}
	}
}
