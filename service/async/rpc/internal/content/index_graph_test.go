package content

import (
	"context"
	"errors"
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
	"trpc.group/trpc-go/trpc-agent-go/runner"
	frameworktrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

type indexGraphFixture struct {
	index func(context.Context, string, Fence, map[string]corpus.Ref) (IndexBuildResult, error)
}

func (f *indexGraphFixture) Index(ctx context.Context, buildID string, fence Fence, resume map[string]corpus.Ref) (IndexBuildResult, error) {
	return f.index(ctx, buildID, fence, resume)
}

func indexGraphValues() (string, Fence, IndexBuildResult) {
	_, fence, ref := prepareGraphValues()
	result := IndexBuildResult{BuildID: fence.BuildID, ReleaseID: "release-1", Generation: 3,
		ChunkManifest: ref, IndexManifest: ref, State: "READY",
		Lanes: map[string]corpus.Ref{"dense": ref, "sparse": ref, "multivector": ref}}
	return fence.BuildID, fence, result
}

func runIndexGraph(t *testing.T, ctx context.Context, indexer IndexService, buildID string, fence Fence) ([]*event.Event, error) {
	t.Helper()
	ag, err := NewIndexGraphAgent(indexer)
	if err != nil {
		t.Fatal(err)
	}
	option, err := IndexGraphRunOption(buildID, fence, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := runner.NewRunner("index-graph-test", ag, runner.WithPlugins(prepareErrorOwnership{}))
	defer func() {
		if err := r.Close(); err != nil {
			t.Errorf("close Runner: %v", err)
		}
	}()
	stream, err := r.Run(ctx, "user-1", "session-1", model.NewUserMessage("index"), option)
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

func TestIndexGraphRunnerReceiptAndNativeSpans(t *testing.T) {
	buildID, fence, result := indexGraphValues()
	indexer := &indexGraphFixture{index: func(ctx context.Context, id string, got Fence, _ map[string]corpus.Ref) (IndexBuildResult, error) {
		if ctx.Err() != nil || id != buildID || got != fence {
			t.Errorf("changed graph request: %s %+v %v", id, got, ctx.Err())
		}
		return result, nil
	}}
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	oldTracer, oldProvider := frameworktrace.Tracer, frameworktrace.TracerProvider
	frameworktrace.TracerProvider = provider
	frameworktrace.Tracer = provider.Tracer("trpc.agent.go")
	defer func() {
		frameworktrace.Tracer, frameworktrace.TracerProvider = oldTracer, oldProvider
		_ = provider.Shutdown(context.Background())
	}()
	ctx, parent := provider.Tracer("test-parent").Start(context.Background(), "index-parent")
	events, err := runIndexGraph(t, ctx, indexer, buildID, fence)
	parent.End()
	if err != nil {
		t.Fatal(err)
	}
	var graphDone, runnerDone int
	for _, e := range events {
		if e.IsTerminalError() {
			t.Fatalf("unexpected terminal Graph error: %+v", e.Error)
		}
		if e.IsRunnerCompletion() {
			runnerDone++
		}
		receipt, completed, err := IndexGraphReceiptFromCompletion(e)
		if err != nil {
			t.Fatal(err)
		}
		if completed {
			graphDone++
			if receipt.State != "READY" || receipt.IndexManifest != result.IndexManifest || receipt.AttemptID != fence.AttemptID {
				t.Fatalf("wrong index receipt: %+v", receipt)
			}
		}
	}
	if graphDone != 1 || runnerDone != 1 {
		t.Fatalf("Graph/Runner completions %d/%d", graphDone, runnerDone)
	}
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, span := range spans.Ended() {
		byName[span.Name()] = span
	}
	parentSpan := byName["index-parent"]
	if parentSpan == nil {
		t.Fatal("missing parent trace")
	}
	for _, name := range []string{"invoke_agent content_index", "workflow execute_graph content_index", "workflow execute_function_node index_fixed_generation"} {
		span := byName[name]
		if span == nil || span.InstrumentationScope().Name != "trpc.agent.go" || span.SpanContext().TraceID() != parentSpan.SpanContext().TraceID() {
			t.Fatalf("missing framework-native same-trace span %s", name)
		}
	}
}

func TestIndexGraphRejectsInvalidAndFailedResult(t *testing.T) {
	buildID, fence, ready := indexGraphValues()
	if _, err := NewIndexGraphAgent((*indexGraphFixture)(nil)); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := IndexGraphRunOption(buildID, Fence{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := IndexGraphRunOption(buildID, fence, map[string]corpus.Ref{"other": ready.IndexManifest}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	for _, scenario := range []string{"error", "partial", "wrong_build"} {
		t.Run(scenario, func(t *testing.T) {
			indexer := &indexGraphFixture{index: func(context.Context, string, Fence, map[string]corpus.Ref) (IndexBuildResult, error) {
				switch scenario {
				case "error":
					return IndexBuildResult{}, ErrConflict
				case "partial":
					got := ready
					got.State = "BUILDING"
					return got, nil
				default:
					got := ready
					got.BuildID = "other"
					return got, nil
				}
			}}
			events, err := runIndexGraph(t, context.Background(), indexer, buildID, fence)
			if err != nil {
				t.Fatal(err)
			}
			var terminal bool
			for _, e := range events {
				if e.IsTerminalError() && e.Error != nil {
					terminal = true
				}
				if _, done, _ := IndexGraphReceiptFromCompletion(e); done {
					t.Fatal("failed index produced READY Graph receipt")
				}
			}
			if !terminal {
				t.Fatal("missing terminal Graph error")
			}
		})
	}
}

func TestIndexGraphCancellationAndCompletionParsing(t *testing.T) {
	buildID, fence, _ := indexGraphValues()
	started := make(chan struct{})
	stopped := make(chan error, 1)
	indexer := &indexGraphFixture{index: func(ctx context.Context, _ string, _ Fence, _ map[string]corpus.Ref) (IndexBuildResult, error) {
		close(started)
		<-ctx.Done()
		stopped <- ctx.Err()
		return IndexBuildResult{}, ctx.Err()
	}}
	ag, err := NewIndexGraphAgent(indexer)
	if err != nil {
		t.Fatal(err)
	}
	option, err := IndexGraphRunOption(buildID, fence, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := runner.NewRunner("index-cancel-test", ag, runner.WithPlugins(prepareErrorOwnership{}))
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	stream, err := r.Run(ctx, "user-1", "session-1", model.NewUserMessage("index"), option)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		cancel()
		t.Fatal("index node did not start")
	}
	cancel()
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("index node did not stop")
	}
	for e := range stream {
		if _, done, _ := IndexGraphReceiptFromCompletion(e); done {
			t.Fatal("cancelled index produced receipt")
		}
	}
	if _, done, err := IndexGraphReceiptFromCompletion(nil); done || err != nil {
		t.Fatal("nil event accepted")
	}
	e := graph.NewGraphCompletionEvent()
	if _, done, err := IndexGraphReceiptFromCompletion(e); !done || !errors.Is(err, ErrIndexGraphOutput) {
		t.Fatal("missing receipt accepted")
	}
	e.StateDelta[indexGraphReceiptKey] = []byte(strings.Repeat("x", 3))
	if _, done, err := IndexGraphReceiptFromCompletion(e); !done || err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestIndexGraphRunOptionCopiesResume(t *testing.T) {
	buildID, fence, result := indexGraphValues()
	resume := map[string]corpus.Ref{"sparse": result.IndexManifest}
	option, err := IndexGraphRunOption(buildID, fence, resume)
	if err != nil {
		t.Fatal(err)
	}
	delete(resume, "sparse")
	opts := agent.RunOptions{RuntimeState: map[string]any{"existing": "kept"}}
	option(&opts)
	request, ok := opts.RuntimeState[indexGraphRequestKey].(IndexGraphRequest)
	if !ok || opts.RuntimeState["existing"] != "kept" || request.ResumeIndexes["sparse"] != result.IndexManifest {
		t.Fatalf("request state mutated: %+v", opts.RuntimeState)
	}
}

func TestIndexGraphRealPGCoordinatorKeepsFixedGeneration(t *testing.T) {
	coordinator, store, fence, lanes := indexFixture(t)
	events, err := runIndexGraph(t, context.Background(), coordinator, fence.BuildID, fence)
	if err != nil {
		t.Fatal(err)
	}
	var graphDone, runnerDone int
	var receipt IndexGraphReceipt
	for _, e := range events {
		if e.IsTerminalError() {
			t.Fatalf("PG-backed index Graph failed: %+v", e.Error)
		}
		if e.IsRunnerCompletion() {
			runnerDone++
		}
		got, done, err := IndexGraphReceiptFromCompletion(e)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			graphDone++
			receipt = got
		}
	}
	committed, err := store.Get(context.Background(), fence.BuildID)
	if err != nil {
		t.Fatal(err)
	}
	if graphDone != 1 || runnerDone != 1 || committed.State != "READY" || committed.Result == nil ||
		*committed.Result != receipt.IndexManifest || committed.Generation != receipt.Generation ||
		committed.AttemptID != fence.AttemptID || committed.LeaseEpoch != fence.LeaseEpoch {
		t.Fatalf("Graph receipt not backed by fixed PG READY: %d/%d %+v %+v", graphDone, runnerDone, committed, receipt)
	}
	for lane, fixture := range lanes {
		if fixture.calls != 1 || committed.Lanes[lane] != receipt.Lanes[lane] {
			t.Fatalf("lane %s did not commit exactly once", lane)
		}
	}
}
