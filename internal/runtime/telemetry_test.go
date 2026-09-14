package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type discardTraceExporter struct{}

func (discardTraceExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (discardTraceExporter) Shutdown(context.Context) error                             { return nil }

var observedOnce sync.Once
var observedBundle *telemetry.Bundle
var observedError error
var observedOutput bytes.Buffer

// Functional runner tests share a process-level install, just like an app.
// Observability acceptance separately asserts real JSON, Span and metrics
// output; these functional tests do not treat Discard as evidence.
func observedForTest(t *testing.T) *telemetry.Bundle {
	t.Helper()
	observedOnce.Do(func() {
		observedBundle, observedError = telemetry.New(context.Background(), telemetry.Config{Service: "btw-test", Environment: "test", Version: "test-revision",
			InstanceID: "test-instance", Output: &observedOutput, Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: discardTraceExporter{}})
		if observedError == nil {
			observedError = observedBundle.InstallGlobals()
		}
	})
	if observedError != nil {
		t.Fatal(observedError)
	}
	return observedBundle
}

func TestRunnerTerminalCreatesTraceLinkedStageAndMetric(t *testing.T) {
	b := observedForTest(t)
	runID := "run-observed-stage"
	r := &Runtime{runner: runnerStub{[]*event.Event{{RequestID: runID, Response: &model.Response{Done: true,
		Object: model.ObjectTypeRunnerCompletion}}}}, observed: b, active: map[string]context.CancelFunc{}}
	q := runtimeRequest()
	q.RunID, q.SessionID = runID, "session-observed-stage"
	result, err := r.Run(context.Background(), q, nil)
	if err != nil || !result.Completed {
		t.Fatalf("runner result=%+v error=%v", result, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(observedOutput.Bytes()), []byte("\n")) {
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		if entry["run_id"] == runID {
			records = append(records, entry)
		}
	}
	if len(records) != 2 || records[0]["event"] != "runtime.run.started" || records[1]["event"] != "runtime.run.finished" ||
		records[1]["outcome"] != "succeeded" || records[1]["completed"] != true || records[1]["event_count"] != float64(1) ||
		records[0]["trace_id"] != records[1]["trace_id"] || records[0]["span_id"] != records[1]["span_id"] {
		t.Fatalf("run lost structured correlation: %+v", records)
	}
	response := httptest.NewRecorder()
	b.MetricsHandler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(response.Body.String(), `sea_btw_operations_total{component="runtime",outcome="succeeded"}`) {
		t.Fatal("successful run not counted")
	}
}

func TestSinkRejectionKeepsFailureOutcome(t *testing.T) {
	b := observedForTest(t)
	runID := "run-sink-rejected"
	r := &Runtime{runner: runnerStub{[]*event.Event{{RequestID: runID, Response: &model.Response{Done: true,
		Object: model.ObjectTypeRunnerCompletion}}}}, observed: b, active: map[string]context.CancelFunc{}}
	q := runtimeRequest()
	q.RunID, q.SessionID = runID, "session-sink-rejected"
	want := errors.New("citation receipt rejected")
	_, err := r.Run(context.Background(), q, func(context.Context, *event.Event) error { return want })
	if !errors.Is(err, want) || errors.Is(err, context.Canceled) {
		t.Fatalf("sink failure became cancellation: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	var terminal map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(observedOutput.Bytes()), []byte("\n")) {
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		if entry["run_id"] == runID && entry["event"] == "runtime.run.finished" {
			terminal = entry
		}
	}
	if terminal == nil || terminal["outcome"] != "failed" || terminal["error_code"] != "RUN_SINK_FAILED" {
		t.Fatalf("sink rejection terminal was not failed: %+v", terminal)
	}
}

type panicRunner struct{}

func (panicRunner) Run(context.Context, string, string, model.Message, ...agent.RunOption) (<-chan *event.Event, error) {
	panic("fixture runner panic")
}
func (panicRunner) Close() error { return nil }

func TestRunnerPanicNeverEmitsSuccess(t *testing.T) {
	b := observedForTest(t)
	r := &Runtime{runner: panicRunner{}, observed: b, active: map[string]context.CancelFunc{}}
	q := runtimeRequest()
	q.RunID = "run-observed-panic"
	defer func() {
		if value := recover(); value == nil {
			t.Fatal("fatal runner panic was swallowed")
		}
		var terminal map[string]any
		for _, line := range bytes.Split(bytes.TrimSpace(observedOutput.Bytes()), []byte("\n")) {
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				t.Fatal(err)
			}
			if entry["run_id"] == q.RunID && entry["event"] == "runtime.run.finished" {
				terminal = entry
			}
		}
		if terminal == nil || terminal["outcome"] != "failed" || terminal["error_code"] != "RUN_PANIC" {
			t.Fatalf("panic recorded as success or without cause: %+v", terminal)
		}
	}()
	_, _ = r.Run(context.Background(), q, nil)
}

func TestMain(m *testing.M) {
	code := m.Run()
	if observedBundle != nil {
		if err := observedBundle.Close(context.Background()); err != nil {
			code = 1
		}
	}
	os.Exit(code)
}
