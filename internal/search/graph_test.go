package search

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	frameworktrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

func TestSearchGraphRunnerUsesNativeSpansAndFixedSnapshot(t *testing.T) {
	c := new(calls)
	service := service(c, PlanFunc(oneQuery), CheckFunc(allow), policy())
	ag, err := NewSearchGraphAgent(service)
	if err != nil {
		t.Fatal(err)
	}
	input := Request{Query: "question", Depth: Fast, Intelligence: High, Snapshot: snapshot()}
	fixed := input
	fixed.Snapshot = cloneSnapshot(input.Snapshot)
	option, err := SearchGraphRunOption(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Snapshot.Indexes[Dense] = ref("d")
	input.Snapshot.ValidRevisionIDs[0] = "withdrawn"

	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	oldTracer, oldProvider := frameworktrace.Tracer, frameworktrace.TracerProvider
	frameworktrace.TracerProvider = provider
	frameworktrace.Tracer = provider.Tracer("trpc.agent.go")
	defer func() {
		frameworktrace.Tracer, frameworktrace.TracerProvider = oldTracer, oldProvider
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown trace provider: %v", err)
		}
	}()
	ctx, parent := provider.Tracer("search-test").Start(context.Background(), "search-parent")
	r := runner.NewRunner("search-graph-test", ag)
	defer func() { _ = r.Close() }()
	stream, err := r.Run(ctx, "user-1", "session-1", model.NewUserMessage("execute search"), option)
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	var graphDone, runnerDone int
	for e := range stream {
		if e.IsTerminalError() {
			t.Fatalf("search graph failed: %+v", e.Error)
		}
		if e.IsRunnerCompletion() {
			runnerDone++
		}
		got, complete, decodeErr := SearchGraphResultFromCompletion(e, fixed)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if complete {
			graphDone++
			result = got
			for _, raw := range e.StateDelta {
				if strings.Contains(string(raw), "fixed text") {
					t.Fatal("indexed text leaked into Graph state")
				}
			}
		}
	}
	parent.End()
	if graphDone != 1 || runnerDone != 1 || result.Status != "complete" ||
		result.Snapshot.Indexes[Dense] != ref("a") || result.Snapshot.ValidRevisionIDs[0] != "rev-a" ||
		len(c.dense) != 1 || len(c.sparse) != 1 || len(c.multi) != 1 {
		t.Fatalf("fixed framework search graph result: graph=%d runner=%d result=%+v calls=%+v", graphDone, runnerDone, result, c)
	}
	got := make(map[string]sdktrace.ReadOnlySpan)
	counts := make(map[string]int)
	for _, span := range spans.Ended() {
		got[span.Name()] = span
		counts[span.Name()]++
	}
	root := got["search-parent"]
	if root == nil {
		t.Fatalf("missing parent span: %v", reflect.ValueOf(got).MapKeys())
	}
	for _, name := range []string{"invoke_agent search_execute", "workflow execute_graph search_execute", "workflow execute_function_node execute_search"} {
		span := got[name]
		if counts[name] != 1 || span == nil || span.InstrumentationScope().Name != "trpc.agent.go" ||
			span.SpanContext().TraceID() != root.SpanContext().TraceID() {
			t.Fatalf("missing/wrong native framework span %q: %v", name, reflect.ValueOf(got).MapKeys())
		}
	}
	if got["invoke_agent search_execute"].Parent().SpanID() != root.SpanContext().SpanID() {
		t.Fatal("search Agent span is not a child of the request parent")
	}
	if got["workflow execute_graph search_execute"].Parent().SpanID() != got["invoke_agent search_execute"].SpanContext().SpanID() ||
		got["workflow execute_function_node execute_search"].Parent().SpanID() != got["workflow execute_graph search_execute"].SpanContext().SpanID() {
		t.Fatal("framework Agent, Graph and function node spans are not a parent-child chain")
	}
}

func TestSearchGraphRejectsInvalidInputAndBadCompletion(t *testing.T) {
	if _, err := NewSearchGraphAgent(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil service: %v", err)
	}
	if _, err := SearchGraphRunOption(Request{Query: " ", Depth: Fast, Intelligence: Low, Snapshot: snapshot()}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty query: %v", err)
	}
	option, err := SearchGraphRunOption(Request{Query: "q", Depth: Fast, Intelligence: Low, Snapshot: snapshot()})
	if err != nil {
		t.Fatal(err)
	}
	options := agent.RunOptions{RuntimeState: map[string]any{"other": "kept"}}
	option(&options)
	if options.RuntimeState["other"] != "kept" {
		t.Fatal("search option replaced unrelated runtime state")
	}
	if _, complete, err := SearchGraphResultFromCompletion(nil, Request{}); complete || err != nil {
		t.Fatalf("nil completion: complete=%v err=%v", complete, err)
	}
	e := graph.NewGraphCompletionEvent()
	if _, complete, err := SearchGraphResultFromCompletion(e, Request{}); !complete || !errors.Is(err, ErrSearchGraphOutput) {
		t.Fatalf("missing output: complete=%v err=%v", complete, err)
	}
	e.StateDelta[searchGraphResultKey] = []byte(`{"status":"complete"}`)
	if _, complete, err := SearchGraphResultFromCompletion(e, Request{}); !complete || !errors.Is(err, ErrSearchGraphOutput) {
		t.Fatalf("invalid output: complete=%v err=%v", complete, err)
	}
	valid := Request{Query: "q", Depth: Fast, Intelligence: Low, Snapshot: snapshot()}
	validResult := Result{Status: "empty", StopReason: "no_evidence", Profile: Profile{RequestedDepth: Fast,
		EffectiveDepth: Fast, RequestedIntelligence: Low, EffectiveIntelligence: Low, PolicyVersion: "v1"}, Snapshot: cloneSnapshot(valid.Snapshot)}
	raw, err := json.Marshal(validResult)
	if err != nil {
		t.Fatal(err)
	}
	e.StateDelta[searchGraphResultKey] = raw
	wrong := valid
	wrong.Snapshot = cloneSnapshot(valid.Snapshot)
	wrong.Snapshot.Generation++
	if _, complete, err := SearchGraphResultFromCompletion(e, wrong); !complete || !errors.Is(err, ErrSearchGraphOutput) {
		t.Fatalf("wrong fixed request accepted: complete=%v err=%v", complete, err)
	}
}

func TestSearchGraphPropagatesPlannerFailureWithoutResult(t *testing.T) {
	want := errors.New("planner unavailable")
	s := service(new(calls), PlanFunc(func(context.Context, PlanInput) ([]string, error) {
		return nil, want
	}), CheckFunc(allow), policy())
	ag, err := NewSearchGraphAgent(s)
	if err != nil {
		t.Fatal(err)
	}
	option, err := SearchGraphRunOption(Request{Query: "question", Depth: Fast, Intelligence: Low, Snapshot: snapshot()})
	if err != nil {
		t.Fatal(err)
	}
	r := runner.NewRunner("search-graph-failure-test", ag)
	defer func() { _ = r.Close() }()
	stream, err := r.Run(context.Background(), "user-1", "session-1", model.NewUserMessage("execute search"), option)
	if err != nil {
		t.Fatal(err)
	}
	var terminal, graphResult bool
	for e := range stream {
		terminal = terminal || (e.IsTerminalError() && e.Error != nil && strings.Contains(e.Error.Message, want.Error()))
		_, isResult, _ := SearchGraphResultFromCompletion(e, Request{})
		graphResult = graphResult || isResult
	}
	if !terminal || graphResult {
		t.Fatalf("planner failure became success: terminal=%v result=%v", terminal, graphResult)
	}
}
