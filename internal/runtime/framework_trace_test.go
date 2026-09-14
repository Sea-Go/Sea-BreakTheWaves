package runtime

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

// The framework owns package-level tracing state. Run this acceptance in a
// fresh process so unrelated tests cannot mask a missing process-level bridge.
func TestFrameworkNativeSpansShareRuntimeTrace(t *testing.T) {
	if os.Getenv("SEA_FRAMEWORK_TRACE_CHILD") == "1" {
		assertFrameworkNativeSpansShareRuntimeTrace(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFrameworkNativeSpansShareRuntimeTrace$", "-test.v")
	cmd.Env = append(os.Environ(), "SEA_FRAMEWORK_TRACE_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated Runner/LLMAgent/FunctionTool trace acceptance: %v\n%s", err, output)
	}
	if testing.Verbose() {
		t.Log(strings.TrimSpace(string(output)))
	}
}

type nativeSpanExporter struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (e *nativeSpanExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, spans...)
	return nil
}

func (*nativeSpanExporter) Shutdown(context.Context) error { return nil }

func (e *nativeSpanExporter) snapshot() []sdktrace.ReadOnlySpan {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sdktrace.ReadOnlySpan(nil), e.spans...)
}

type nativeTraceModel struct{ calls atomic.Int32 }

func (*nativeTraceModel) Info() model.Info { return model.Info{Name: "native-fixture"} }

func (m *nativeTraceModel) GenerateContent(ctx context.Context, _ *model.Request) (<-chan *model.Response, error) {
	responses := make(chan *model.Response, 1)
	if err := ctx.Err(); err != nil {
		close(responses)
		return responses, err
	}
	switch m.calls.Add(1) {
	case 1:
		finish := "tool_calls"
		message := model.NewAssistantMessage("")
		message.ToolCalls = []model.ToolCall{{ID: "native-tool-call", Type: "function",
			Function: model.FunctionDefinitionParam{Name: "native_lookup", Arguments: []byte(`{"key":"fixture"}`)}}}
		responses <- &model.Response{Done: true, Model: "dynamic-response-a", Choices: []model.Choice{{Message: message, FinishReason: &finish}}}
	default:
		finish := "stop"
		responses <- &model.Response{Done: true, Model: "dynamic-response-b", Choices: []model.Choice{{Message: model.NewAssistantMessage("done"), FinishReason: &finish}}}
	}
	close(responses)
	return responses, nil
}

type nativeLookupInput struct {
	Key string `json:"key" jsonschema:"description=Fixture lookup key"`
}

type nativeLookupOutput struct {
	Value string `json:"value"`
}

func assertFrameworkNativeSpansShareRuntimeTrace(t *testing.T) {
	var output bytes.Buffer
	exporter := &nativeSpanExporter{}
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-native-trace-test",
		Environment: "test", Version: "fixed-v1.8.1", InstanceID: "native-test-process", Output: &output,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	var toolCalls atomic.Int32
	lookup := function.NewFunctionTool(func(ctx context.Context, input nativeLookupInput) (nativeLookupOutput, error) {
		toolCalls.Add(1)
		if input.Key != "fixture" {
			t.Errorf("typed FunctionTool received key %q", input.Key)
		}
		if !trace.SpanFromContext(ctx).SpanContext().IsValid() {
			t.Error("typed FunctionTool lost framework tracing context")
		}
		return nativeLookupOutput{Value: "found"}, nil
	}, function.WithName("native_lookup"), function.WithDescription("Read a fixture key."))
	fixtureModel := &nativeTraceModel{}
	ag := llmagent.New("native_agent", llmagent.WithModel(fixtureModel),
		llmagent.WithTools([]tool.Tool{lookup}), llmagent.WithGenerationConfig(model.GenerationConfig{Stream: false}))
	sessions := inmemory.NewSessionService()
	r, err := New("native-trace", ag, sessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeRequest()
	request.RunID, request.SessionID = "native-trace-run", "native-trace-session"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, runErr := r.Run(ctx, request, nil)
	metrics := httptest.NewRecorder()
	observed.MetricsHandler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	metricBody := metrics.Body.String()
	if metrics.Code != 200 {
		t.Fatalf("native framework metrics scrape status=%d body=%s", metrics.Code, metricBody)
	}
	for _, name := range []string{"sea_btw_operations_total", "trpc_agent_go_agent_", "trpc_agent_go_chat_", "trpc_agent_go_tool_"} {
		if !strings.Contains(metricBody, name) {
			t.Fatalf("missing framework/app metric family %q", name)
		}
	}
	for _, identity := range []string{"native-trace-session", "native-fixture", "native_agent", "native_lookup", "dynamic-response-a", "dynamic-response-b", "trpc_go_agent_user_id", "gen_ai_conversation_id", "gen_ai_agent_id"} {
		if strings.Contains(metricBody, identity) {
			t.Fatalf("high-cardinality framework identity entered /metrics: %s", identity)
		}
	}
	for _, line := range strings.Split(metricBody, "\n") {
		if strings.HasPrefix(line, "trpc_agent_go_") && strings.Contains(line, "request_cnt") && !strings.HasPrefix(line, "#") {
			t.Log("framework_metric", line)
		}
	}
	closeErr := r.Close()
	sessionErr := sessions.Close()
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer flushCancel()
	flushErr := observed.Close(flushCtx)
	if runErr != nil || closeErr != nil || sessionErr != nil || flushErr != nil || !result.Completed {
		t.Fatalf("real framework run=%+v run=%v runner close=%v session close=%v trace flush=%v", result, runErr, closeErr, sessionErr, flushErr)
	}
	if fixtureModel.calls.Load() != 2 || toolCalls.Load() != 1 {
		t.Fatalf("real model/tool calls=%d/%d; want 2/1", fixtureModel.calls.Load(), toolCalls.Load())
	}

	all := exporter.snapshot()
	byName := make(map[string][]sdktrace.ReadOnlySpan)
	byID := make(map[trace.SpanID]sdktrace.ReadOnlySpan)
	for _, span := range all {
		byName[span.Name()] = append(byName[span.Name()], span)
		byID[span.SpanContext().SpanID()] = span
	}
	for name, want := range map[string]int{"runtime.run": 1, "invoke_agent native_agent": 1,
		"chat native-fixture": 2, "execute_tool native_lookup": 1} {
		if got := len(byName[name]); got != want {
			t.Fatalf("native span %q count=%d want=%d; exported=%v", name, got, want, spanNames(all))
		}
	}
	root := byName["runtime.run"][0]
	if root.Parent().IsValid() {
		t.Fatalf("runtime root has unexpected parent %s", root.Parent().SpanID())
	}
	for _, name := range []string{"invoke_agent native_agent", "chat native-fixture", "execute_tool native_lookup"} {
		for _, span := range byName[name] {
			if span.InstrumentationScope().Name != "trpc.agent.go" {
				t.Errorf("%q belongs to %q, not native framework instrumentation", name, span.InstrumentationScope().Name)
			}
			if span.SpanContext().TraceID() != root.SpanContext().TraceID() {
				t.Errorf("%q trace %s differs from runtime trace %s", name, span.SpanContext().TraceID(), root.SpanContext().TraceID())
			}
			if !descendsFrom(span, root, byID) {
				t.Errorf("%q is not a descendant of runtime.run; parent=%s", name, span.Parent().SpanID())
			}
		}
	}
	if got := byName["invoke_agent native_agent"][0].Parent().SpanID(); got != root.SpanContext().SpanID() {
		t.Errorf("invoke_agent parent=%s want runtime.run span=%s", got, root.SpanContext().SpanID())
	}
	t.Logf("framework_span trace_id=%s names=%s", root.SpanContext().TraceID(), spanNames(all))
}

func descendsFrom(span, root sdktrace.ReadOnlySpan, byID map[trace.SpanID]sdktrace.ReadOnlySpan) bool {
	for parent := span.Parent(); parent.IsValid(); {
		if parent.SpanID() == root.SpanContext().SpanID() {
			return true
		}
		ancestor, ok := byID[parent.SpanID()]
		if !ok || ancestor == span {
			return false
		}
		parent = ancestor.Parent()
	}
	return false
}

func spanNames(spans []sdktrace.ReadOnlySpan) string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name())
	}
	return strings.Join(names, ", ")
}
