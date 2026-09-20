// trace_test.go — trace.go 单测（Task 14.1）。
//
// 测试覆盖：
//   - NoopTracer/NoopSpan 空实现不 panic
//   - GenAISpan 属性设置（SetGenAIAttrs/SetPathAttr/SetGraphAttr）
//   - GenAISpan End 不 panic
//   - ExtractTraceID 从 context 提取 trace_id（含未注入场景）
//
// stub 类型用 OBS 前缀避免与 callbacks_test.go 冲突。
package obs

import (
	"context"
	"testing"
)

// OBSSpanStub 记录 span 调用，用于断言 GenAISpan 是否正确转发到底层 Span。
type OBSSpanStub struct {
	attrs  map[string]any
	events []struct {
		name  string
		attrs map[string]string
	}
	statusCode   SpanStatusCode
	statusDesc   string
	ended        bool
}

// SetAttribute 记录属性。
func (s *OBSSpanStub) SetAttribute(key string, value any) {
	if s.attrs == nil {
		s.attrs = make(map[string]any)
	}
	s.attrs[key] = value
}

// AddEvent 记录事件。
func (s *OBSSpanStub) AddEvent(name string, attrs map[string]string) {
	s.events = append(s.events, struct {
		name  string
		attrs map[string]string
	}{name: name, attrs: attrs})
}

// SetStatus 记录状态。
func (s *OBSSpanStub) SetStatus(code SpanStatusCode, description string) {
	s.statusCode = code
	s.statusDesc = description
}

// End 标记 span 已结束。
func (s *OBSSpanStub) End() {
	s.ended = true
}

// OBSTracerStub 记录 StartSpan 调用，返回 OBSSpanStub。
type OBSTracerStub struct {
	spans []*OBSSpanStub
}

// StartSpan 返回新 OBSSpanStub。
func (t *OBSTracerStub) StartSpan(ctx context.Context, _ string, _ map[string]string) (context.Context, Span) {
	stub := &OBSSpanStub{}
	t.spans = append(t.spans, stub)
	return ctx, stub
}

// ----------------------------------------------------------------------------
// NoopTracer / NoopSpan 测试
// ----------------------------------------------------------------------------

func TestNoopTracer_StartSpan_ReturnsNoopSpan(t *testing.T) {
	tracer := NoopTracer{}
	ctx := context.Background()
	newCtx, span := tracer.StartSpan(ctx, "test.span", nil)
	if newCtx != ctx {
		t.Fatal("NoopTracer should return original context")
	}
	if span == nil {
		t.Fatal("span should not be nil")
	}
	// 所有方法不应 panic
	span.SetAttribute("k", "v")
	span.AddEvent("evt", nil)
	span.SetStatus(SpanStatusOK, "ok")
	span.End()
}

func TestNoopSpan_AllMethods_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NoopSpan panicked: %v", r)
		}
	}()
	s := NoopSpan{}
	s.SetAttribute("k", 1)
	s.AddEvent("evt", map[string]string{"a": "b"})
	s.SetStatus(SpanStatusError, "err")
	s.End()
}

// ----------------------------------------------------------------------------
// GenAISpan 测试
// ----------------------------------------------------------------------------

func TestNewGenAISpan_NilTracer_FallbackToNoop(t *testing.T) {
	s := NewGenAISpan(nil, context.Background(), "test.span")
	if s == nil {
		t.Fatal("GenAISpan should not be nil")
	}
	if s.span == nil {
		t.Fatal("underlying span should not be nil")
	}
	// 不应 panic
	s.SetGenAIAttrs("gpt-4", "p", "c", 1, 2, 3)
	s.SetPathAttr("fast")
	s.SetGraphAttr("MATCH (n) RETURN n", 5, 6)
	s.End()
}

func TestGenAISpan_SetGenAIAttrs_ForwardsToSpan(t *testing.T) {
	tracer := &OBSTracerStub{}
	s := NewGenAISpan(tracer, context.Background(), "recommend.slow")
	s.SetGenAIAttrs("gpt-4", "hello", "world", 100, 50, 25)

	if len(tracer.spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(tracer.spans))
	}
	stub := tracer.spans[0]
	if stub.attrs["gen_ai.request.model"] != "gpt-4" {
		t.Fatalf("model = %v, want gpt-4", stub.attrs["gen_ai.request.model"])
	}
	if stub.attrs["gen_ai.prompt"] != "hello" {
		t.Fatalf("prompt = %v, want hello", stub.attrs["gen_ai.prompt"])
	}
	if stub.attrs["gen_ai.completion"] != "world" {
		t.Fatalf("completion = %v, want world", stub.attrs["gen_ai.completion"])
	}
	if stub.attrs["gen_ai.usage.input_tokens"] != 100 {
		t.Fatalf("input_tokens = %v, want 100", stub.attrs["gen_ai.usage.input_tokens"])
	}
	if stub.attrs["gen_ai.usage.output_tokens"] != 50 {
		t.Fatalf("output_tokens = %v, want 50", stub.attrs["gen_ai.usage.output_tokens"])
	}
	if stub.attrs["gen_ai.usage.cached_tokens"] != 25 {
		t.Fatalf("cached_tokens = %v, want 25", stub.attrs["gen_ai.usage.cached_tokens"])
	}
	if stub.attrs["gen_ai.system"] != "trpc-agent-go" {
		t.Fatalf("system = %v, want trpc-agent-go", stub.attrs["gen_ai.system"])
	}

	// attrs 快照
	attrs := s.Attrs()
	if attrs["gen_ai.request.model"] != "gpt-4" {
		t.Fatalf("attrs model = %q, want gpt-4", attrs["gen_ai.request.model"])
	}
	if attrs["gen_ai.usage.input_tokens"] != "100" {
		t.Fatalf("attrs input_tokens = %q, want 100", attrs["gen_ai.usage.input_tokens"])
	}
}

func TestGenAISpan_SetPathAttr_ForwardsToSpan(t *testing.T) {
	tracer := &OBSTracerStub{}
	s := NewGenAISpan(tracer, context.Background(), "recommend.fast")
	s.SetPathAttr("fast")

	stub := tracer.spans[0]
	if stub.attrs["genrec.path"] != "fast" {
		t.Fatalf("path = %v, want fast", stub.attrs["genrec.path"])
	}
	if s.Attrs()["genrec.path"] != "fast" {
		t.Fatalf("attrs path = %q, want fast", s.Attrs()["genrec.path"])
	}
}

func TestGenAISpan_SetGraphAttr_ForwardsToSpan(t *testing.T) {
	tracer := &OBSTracerStub{}
	s := NewGenAISpan(tracer, context.Background(), "recommend.graph")
	s.SetGraphAttr("MATCH (n:Tag) RETURN n LIMIT 10", 5, 8)

	stub := tracer.spans[0]
	if stub.attrs["genrec.graph.cypher"] != "MATCH (n:Tag) RETURN n LIMIT 10" {
		t.Fatalf("cypher = %v", stub.attrs["genrec.graph.cypher"])
	}
	if stub.attrs["genrec.graph.node_count"] != 5 {
		t.Fatalf("node_count = %v, want 5", stub.attrs["genrec.graph.node_count"])
	}
	if stub.attrs["genrec.graph.edge_count"] != 8 {
		t.Fatalf("edge_count = %v, want 8", stub.attrs["genrec.graph.edge_count"])
	}
	attrs := s.Attrs()
	if attrs["genrec.graph.node_count"] != "5" {
		t.Fatalf("attrs node_count = %q, want 5", attrs["genrec.graph.node_count"])
	}
	if attrs["genrec.graph.edge_count"] != "8" {
		t.Fatalf("attrs edge_count = %q, want 8", attrs["genrec.graph.edge_count"])
	}
}

func TestGenAISpan_End_CallsSpanEnd(t *testing.T) {
	tracer := &OBSTracerStub{}
	s := NewGenAISpan(tracer, context.Background(), "recommend.end")
	s.End()
	if !tracer.spans[0].ended {
		t.Fatal("span.End() should be called")
	}
}

func TestGenAISpan_Attrs_ReturnsCopy(t *testing.T) {
	s := NewGenAISpan(NoopTracer{}, context.Background(), "test")
	s.SetPathAttr("hybrid")
	attrs := s.Attrs()
	attrs["genrec.path"] = "mutated"
	// 原快照不应被外部修改影响
	if s.Attrs()["genrec.path"] != "hybrid" {
		t.Fatalf("attrs should be a copy, got %q", s.Attrs()["genrec.path"])
	}
}

// ----------------------------------------------------------------------------
// ExtractTraceID / WithTraceID 测试
// ----------------------------------------------------------------------------

func TestExtractTraceID_NoTraceID_ReturnsEmpty(t *testing.T) {
	if got := ExtractTraceID(context.Background()); got != "" {
		t.Fatalf("ExtractTraceID = %q, want empty", got)
	}
}

func TestExtractTraceID_NilContext_ReturnsEmpty(t *testing.T) {
	if got := ExtractTraceID(nil); got != "" {
		t.Fatalf("ExtractTraceID(nil) = %q, want empty", got)
	}
}

func TestWithTraceID_ThenExtract_RoundTrip(t *testing.T) {
	ctx := WithTraceID(context.Background(), "trace-abc-123")
	if got := ExtractTraceID(ctx); got != "trace-abc-123" {
		t.Fatalf("ExtractTraceID = %q, want trace-abc-123", got)
	}
}

func TestWithTraceID_NilContext_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("WithTraceID(nil) panicked: %v", r)
		}
	}()
	ctx := WithTraceID(nil, "trace-xyz")
	if got := ExtractTraceID(ctx); got != "trace-xyz" {
		t.Fatalf("ExtractTraceID = %q, want trace-xyz", got)
	}
}

// ----------------------------------------------------------------------------
// SpanStatusCode 常量测试
// ----------------------------------------------------------------------------

func TestSpanStatusCode_Values(t *testing.T) {
	if SpanStatusUnset != 0 {
		t.Fatalf("SpanStatusUnset = %d, want 0", SpanStatusUnset)
	}
	if SpanStatusOK != 1 {
		t.Fatalf("SpanStatusOK = %d, want 1", SpanStatusOK)
	}
	if SpanStatusError != 2 {
		t.Fatalf("SpanStatusError = %d, want 2", SpanStatusError)
	}
}

// ----------------------------------------------------------------------------
// itoa 内部工具测试
// ----------------------------------------------------------------------------

func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{100, "100"},
		{-5, "-5"},
		{-100, "-100"},
	}
	for _, c := range cases {
		if got := itoa(c.in); got != c.want {
			t.Fatalf("itoa(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ----------------------------------------------------------------------------
// TraceContext 结构测试
// ----------------------------------------------------------------------------

func TestTraceContext_FieldsAccessible(t *testing.T) {
	tc := TraceContext{
		TraceID:   "trace-1",
		SpanID:    "span-1",
		PathTaken: "fast",
	}
	if tc.TraceID != "trace-1" {
		t.Fatalf("TraceID = %q", tc.TraceID)
	}
	if tc.SpanID != "span-1" {
		t.Fatalf("SpanID = %q", tc.SpanID)
	}
	if tc.PathTaken != "fast" {
		t.Fatalf("PathTaken = %q", tc.PathTaken)
	}
}
