package content

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/plugin"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	frameworktrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

type prepareGraphFixture struct {
	prepare func(context.Context, BuildInput, Fence) (Prepared, error)
}

// Production Runtime clones error events at this public plugin hook before
// Runner repairs their content. Component tests use the same ownership rule
// so framework Graph tracing never reads an Event Runner is mutating.
type prepareErrorOwnership struct{}

func (prepareErrorOwnership) Name() string { return "content.test-error-ownership" }
func (prepareErrorOwnership) Register(r *plugin.Registry) {
	r.OnEvent(func(_ context.Context, _ *agent.Invocation, e *event.Event) (*event.Event, error) {
		if e != nil && e.Response != nil && e.Error != nil {
			return e.Clone(), nil
		}
		return nil, nil
	})
}

func (f *prepareGraphFixture) Prepare(ctx context.Context, input BuildInput, fence Fence) (Prepared, error) {
	return f.prepare(ctx, input, fence)
}

func prepareGraphValues() (BuildInput, Fence, corpus.Ref) {
	hash := strings.Repeat("a", 64)
	input := BuildInput{BuildID: "build-1", ModuleID: "module-1", ReleaseID: "release-1",
		Generation: 3, InputHash: hash, OperationID: "operation-1", Revisions: []string{"revision-b", "revision-a"}}
	fence := Fence{BuildID: input.BuildID, AttemptID: "attempt-1", LeaseEpoch: 7,
		CancelVersion: 2, ExpiresAt: time.Now().Add(time.Hour)}
	ref := corpus.Ref{Key: "sha256/" + hash, SHA256: hash}
	return input, fence, ref
}

func successfulGraphPrepared(input BuildInput, fence Fence, ref corpus.Ref) Prepared {
	return Prepared{
		Manifest: corpus.ChunkManifest{ModuleID: input.ModuleID, ReleaseID: input.ReleaseID,
			InputManifestHash: input.InputHash, Chunks: []corpus.Chunk{{ID: "chunk-1", Text: "private chunk body"}}},
		Ref:   ref,
		Build: Build{BuildInput: input, Fence: fence, State: "BUILDING", Chunks: &ref},
	}
}

func runPrepareGraph(t *testing.T, ctx context.Context, preparer PrepareService, input BuildInput, fence Fence) ([]*event.Event, error) {
	t.Helper()
	ag, err := NewPrepareGraphAgent(preparer)
	if err != nil {
		t.Fatal(err)
	}
	option, err := PrepareGraphRunOption(input, fence)
	if err != nil {
		t.Fatal(err)
	}
	r := runner.NewRunner("prepare-graph-test", ag, runner.WithPlugins(prepareErrorOwnership{}))
	defer func() {
		if err := r.Close(); err != nil {
			t.Errorf("close runner: %v", err)
		}
	}()
	stream, err := r.Run(ctx, "user-1", "session-1", model.NewUserMessage("prepare"), option)
	if err != nil {
		return nil, err
	}
	var events []*event.Event
	for e := range stream {
		if e != nil {
			events = append(events, e)
		}
	}
	return events, nil
}

func TestPrepareGraphRunnerReceiptAndNativeSpans(t *testing.T) {
	input, fence, ref := prepareGraphValues()
	var called bool
	preparer := &prepareGraphFixture{prepare: func(ctx context.Context, got BuildInput, gotFence Fence) (Prepared, error) {
		called = true
		if ctx.Err() != nil || !reflect.DeepEqual(got.Revisions, []string{"revision-a", "revision-b"}) ||
			gotFence.AttemptID != fence.AttemptID {
			t.Errorf("graph passed changed input or fence: input=%+v fence=%+v err=%v", got, gotFence, ctx.Err())
		}
		return successfulGraphPrepared(got, gotFence, ref), nil
	}}
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
	ctx, parent := provider.Tracer("test-parent").Start(context.Background(), "parent")
	events, err := runPrepareGraph(t, ctx, preparer, input, fence)
	parent.End()
	if err != nil || !called {
		t.Fatalf("runner err=%v preparer called=%v", err, called)
	}
	var receipt PrepareGraphReceipt
	var graphCompletions, runnerCompletions int
	for _, e := range events {
		if e.IsTerminalError() {
			t.Fatalf("successful graph emitted terminal error: %+v", e.Error)
		}
		if e.IsRunnerCompletion() {
			runnerCompletions++
		}
		got, complete, decodeErr := PrepareGraphReceiptFromCompletion(e)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if complete {
			graphCompletions++
			receipt = got
			if _, ok := e.StateDelta[prepareGraphRequestKey]; !ok {
				t.Error("completion lacks fixed request state")
			}
			for _, value := range e.StateDelta {
				if strings.Contains(string(value), "private chunk body") {
					t.Fatal("chunk text leaked into Graph completion")
				}
			}
		}
	}
	if graphCompletions != 1 || runnerCompletions != 1 {
		t.Fatalf("graph/runner completions=%d/%d", graphCompletions, runnerCompletions)
	}
	if receipt.BuildID != input.BuildID || receipt.ChunkManifest != ref || receipt.ChunkCount != 1 ||
		receipt.State != "BUILDING" || receipt.AttemptID != fence.AttemptID || receipt.LeaseEpoch != fence.LeaseEpoch {
		t.Fatalf("receipt differs from fixed attempt: %+v", receipt)
	}
	gotSpans := spans.Ended()
	byName := make(map[string]sdktrace.ReadOnlySpan)
	for _, span := range gotSpans {
		byName[span.Name()] = span
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.AsString(), "private chunk body") {
				t.Fatalf("chunk text leaked into native span %s", span.Name())
			}
		}
	}
	root := byName["parent"]
	if root == nil {
		t.Fatalf("missing parent span: %v", spanNamesForPrepare(gotSpans))
	}
	for _, name := range []string{"invoke_agent content_prepare", "workflow execute_graph content_prepare", "workflow execute_function_node prepare_chunks"} {
		span := byName[name]
		if span == nil {
			t.Fatalf("missing native Graph span %s: %v", name, spanNamesForPrepare(gotSpans))
		}
		if span.InstrumentationScope().Name != "trpc.agent.go" || span.SpanContext().TraceID() != root.SpanContext().TraceID() {
			t.Errorf("native Graph span %s has wrong scope/trace", name)
		}
	}
	if byName["invoke_agent content_prepare"].Parent().SpanID() != root.SpanContext().SpanID() {
		t.Error("GraphAgent span is not child of caller span")
	}
}

func spanNamesForPrepare(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name())
	}
	return names
}

func TestPrepareGraphRejectsInvalidAndPropagatesFailure(t *testing.T) {
	input, fence, ref := prepareGraphValues()
	called := false
	preparer := &prepareGraphFixture{prepare: func(_ context.Context, got BuildInput, gotFence Fence) (Prepared, error) {
		called = true
		return successfulGraphPrepared(got, gotFence, ref), nil
	}}
	if _, err := NewPrepareGraphAgent((*prepareGraphFixture)(nil)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed nil dependency err=%v", err)
	}
	bad := fence
	bad.BuildID = "different-build"
	if _, err := PrepareGraphRunOption(input, bad); !errors.Is(err, ErrInvalid) || called {
		t.Fatalf("invalid fence err=%v called=%v", err, called)
	}
	input.Revisions = []string{"duplicate", "duplicate"}
	if _, err := PrepareGraphRunOption(input, fence); !errors.Is(err, ErrInvalid) || called {
		t.Fatalf("invalid revisions err=%v called=%v", err, called)
	}
	input, fence, _ = prepareGraphValues()
	preparer.prepare = func(context.Context, BuildInput, Fence) (Prepared, error) {
		called = true
		return Prepared{}, ErrConflict
	}
	events, err := runPrepareGraph(t, context.Background(), preparer, input, fence)
	if err != nil || !called {
		t.Fatalf("runner err=%v called=%v", err, called)
	}
	var terminal bool
	for _, e := range events {
		if e.IsTerminalError() && e.Error != nil && strings.Contains(e.Error.Message, ErrConflict.Error()) {
			terminal = true
		}
		if _, complete, _ := PrepareGraphReceiptFromCompletion(e); complete {
			t.Fatal("failed prepare emitted graph receipt")
		}
	}
	if !terminal {
		t.Fatalf("domain failure was not propagated as terminal Graph/Runner event: %d events", len(events))
	}
}

func TestPrepareGraphCancellationReachesPreparer(t *testing.T) {
	input, fence, _ := prepareGraphValues()
	started := make(chan struct{})
	stopped := make(chan error, 1)
	preparer := &prepareGraphFixture{prepare: func(ctx context.Context, _ BuildInput, _ Fence) (Prepared, error) {
		close(started)
		<-ctx.Done()
		stopped <- ctx.Err()
		return Prepared{}, ctx.Err()
	}}
	ag, err := NewPrepareGraphAgent(preparer)
	if err != nil {
		t.Fatal(err)
	}
	option, err := PrepareGraphRunOption(input, fence)
	if err != nil {
		t.Fatal(err)
	}
	r := runner.NewRunner("prepare-graph-cancel-test", ag, runner.WithPlugins(prepareErrorOwnership{}))
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := r.Run(ctx, "user-1", "session-1", model.NewUserMessage("prepare"), option)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("preparer did not start")
	}
	cancel()
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("preparer cancellation=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("preparer did not observe Runner cancellation")
	}
	for e := range stream {
		if _, complete, _ := PrepareGraphReceiptFromCompletion(e); complete {
			t.Fatal("canceled prepare emitted graph receipt")
		}
	}
}

func TestPrepareGraphRejectsPreparedFromOtherAttempt(t *testing.T) {
	input, fence, ref := prepareGraphValues()
	preparer := &prepareGraphFixture{prepare: func(_ context.Context, got BuildInput, gotFence Fence) (Prepared, error) {
		prepared := successfulGraphPrepared(got, gotFence, ref)
		prepared.Build.LeaseEpoch++
		return prepared, nil
	}}
	events, err := runPrepareGraph(t, context.Background(), preparer, input, fence)
	if err != nil {
		t.Fatal(err)
	}
	var failed bool
	for _, e := range events {
		if e.IsTerminalError() && e.Error != nil && strings.Contains(e.Error.Message, ErrConflict.Error()) {
			failed = true
		}
		if _, complete, _ := PrepareGraphReceiptFromCompletion(e); complete {
			t.Fatal("mismatched lease emitted graph receipt")
		}
	}
	if !failed {
		t.Fatal("mismatched prepared result was not rejected")
	}
}

func TestPrepareGraphReceiptExtractionRequiresCompletion(t *testing.T) {
	if _, complete, err := PrepareGraphReceiptFromCompletion(nil); complete || err != nil {
		t.Fatalf("nil event complete=%v err=%v", complete, err)
	}
	e := graph.NewGraphCompletionEvent()
	if _, complete, err := PrepareGraphReceiptFromCompletion(e); !complete || !errors.Is(err, ErrPrepareGraphOutput) {
		t.Fatalf("missing receipt complete=%v err=%v", complete, err)
	}
	e.StateDelta[prepareGraphReceiptKey] = []byte("not-json")
	if _, complete, err := PrepareGraphReceiptFromCompletion(e); !complete || err == nil {
		t.Fatalf("malformed receipt complete=%v err=%v", complete, err)
	}
}

func TestPrepareGraphRunOptionCopiesRequestAndPreservesRuntimeState(t *testing.T) {
	input, fence, _ := prepareGraphValues()
	option, err := PrepareGraphRunOption(input, fence)
	if err != nil {
		t.Fatal(err)
	}
	input.Revisions[0] = "mutated"
	options := agent.RunOptions{RuntimeState: map[string]any{"existing": "kept"}}
	option(&options)
	if options.RuntimeState["existing"] != "kept" {
		t.Fatal("PrepareGraphRunOption replaced unrelated runtime state")
	}
	request, ok := options.RuntimeState[prepareGraphRequestKey].(PrepareGraphRequest)
	if !ok || !reflect.DeepEqual(request.Input.Revisions, []string{"revision-a", "revision-b"}) {
		t.Fatalf("request was mutated after option creation: %+v", options.RuntimeState[prepareGraphRequestKey])
	}
}
