package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	agentlog "trpc.group/trpc-go/trpc-agent-go/log"
)

type spans struct {
	mu    sync.Mutex
	items []sdktrace.ReadOnlySpan
	fail  bool
}

func (s *spans) ExportSpans(_ context.Context, items []sdktrace.ReadOnlySpan) error {
	s.mu.Lock()
	s.items = append(s.items, items...)
	s.mu.Unlock()
	if s.fail {
		return errors.New("task collector failed")
	}
	return nil
}
func (*spans) Shutdown(context.Context) error { return nil }

func testBundle(t *testing.T, output io.Writer, exporter sdktrace.SpanExporter) *Bundle {
	t.Helper()
	b, err := New(context.Background(), Config{Service: "btw-worker", Environment: "test", Version: "revision-123",
		InstanceID: "worker-one", Output: output, Level: slog.LevelDebug, TraceExporter: exporter, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeLines(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("non-JSON log line: %v", err)
		}
		records = append(records, record)
	}
	return records
}

func TestRealJSONSpanAndMetricOutput(t *testing.T) {
	var output bytes.Buffer
	exporter := &spans{}
	b := testBundle(t, &output, exporter)
	ctx, success, err := b.Begin(context.Background(), "runtime", "runtime.run", slog.String("run_id", "run-a"), slog.String("session_id", "session-a"))
	if err != nil {
		t.Fatal(err)
	}
	success.End(ctx, "succeeded", "", nil, slog.Int("event_count", 3))
	success.End(ctx, "failed", "SHOULD_NOT_COUNT", errors.New("duplicate end"))
	ctx, rejected, err := b.Begin(context.Background(), "runtime", "runtime.run", slog.String("run_id", "run-b"))
	if err != nil {
		t.Fatal(err)
	}
	rejected.End(ctx, "rejected", "LEASE_EXPIRED", errors.New("lease expired"))
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	records := decodeLines(t, output.Bytes())
	if len(records) != 4 {
		t.Fatalf("stage start/end pairs=%d", len(records))
	}
	for _, r := range records {
		for _, key := range []string{"timestamp", "level", "service", "environment", "service_version", "instance_id", "component", "log_source", "event", "message", "trace_id", "span_id"} {
			if r[key] == nil {
				t.Fatalf("missing %s in actual output: %+v", key, r)
			}
		}
		if r["service_version"] != "revision-123" || r["component"] != "runtime" || r["log_source"] != "application" {
			t.Fatalf("wrong base attributes: %+v", r)
		}
		timestamp, ok := r["timestamp"].(string)
		if !ok {
			t.Fatalf("timestamp is not a string: %+v", r)
		}
		if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil || !strings.HasSuffix(timestamp, "Z") {
			t.Fatalf("timestamp is not UTC RFC3339: %q %v", timestamp, err)
		}
		traceID, err := trace.TraceIDFromHex(r["trace_id"].(string))
		if err != nil || !traceID.IsValid() {
			t.Fatalf("invalid trace ID: %+v", r)
		}
		spanID, err := trace.SpanIDFromHex(r["span_id"].(string))
		if err != nil || !spanID.IsValid() {
			t.Fatalf("invalid span ID: %+v", r)
		}
	}
	if records[1]["run_id"] != "run-a" || records[1]["outcome"] != "succeeded" || records[1]["event_count"] != float64(3) {
		t.Fatalf("success terminal lost correlation: %+v", records[1])
	}
	if records[3]["run_id"] != "run-b" || records[3]["level"] != "warn" || records[3]["error_code"] != "LEASE_EXPIRED" {
		t.Fatalf("rejected terminal: %+v", records[3])
	}
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	if len(exporter.items) != 2 {
		t.Fatalf("actual exported spans=%d", len(exporter.items))
	}
	for _, span := range exporter.items {
		if span.Name() != "runtime.run" || !span.SpanContext().IsValid() {
			t.Fatal("span not exported with valid context")
		}
	}
	response := httptest.NewRecorder()
	b.MetricsHandler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	for _, line := range []string{`sea_btw_operations_total{component="runtime",outcome="succeeded"} 1`,
		`sea_btw_operations_total{component="runtime",outcome="rejected"} 1`,
		`sea_btw_operations_inflight{component="runtime"} 0`} {
		if !strings.Contains(body, line) {
			t.Fatalf("missing metric %s", line)
		}
	}
	if strings.Contains(body, `run-a`) || strings.Contains(body, `session-a`) {
		t.Fatal("high-cardinality ID became metric label")
	}
}

func TestCloseWaitsForStageAndCanRetryAfterDeadline(t *testing.T) {
	b := testBundle(t, io.Discard, &spans{})
	ctx, stage, err := b.Begin(context.Background(), "runtime", "runtime.run")
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := b.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("closed active stage: %v", err)
	}
	if _, _, err := b.Begin(context.Background(), "runtime", "runtime.run"); !errors.Is(err, ErrConfig) {
		t.Fatal("new stage admitted during shutdown")
	}
	stage.End(ctx, "cancelled", "CANCELLED", context.Canceled)
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type brokenOutput struct{}

func (brokenOutput) Write([]byte) (int, error) { return 0, errors.New("task sink unavailable") }

func TestWriteFailureIncrementsMetricWithoutRecursiveLog(t *testing.T) {
	b := testBundle(t, brokenOutput{}, &spans{})
	ctx, stage, err := b.Begin(context.Background(), "runtime", "runtime.run")
	if err != nil {
		t.Fatal(err)
	}
	stage.End(ctx, "failed", "TASK_FAILURE", errors.New("operation failed"))
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	b.MetricsHandler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(response.Body.String(), "sea_btw_log_write_failures_total 2") {
		t.Fatal("log write errors disappeared")
	}
}

func TestLockedFrameworkContextBridgeUsesSameJSONSink(t *testing.T) {
	var output bytes.Buffer
	b := testBundle(t, &output, &spans{fail: true})
	if err := b.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	ctx, stage, err := b.Begin(context.Background(), "runtime", "runtime.run", slog.String("run_id", "run-framework"))
	if err != nil {
		t.Fatal(err)
	}
	agentlog.InfofContext(ctx, "framework context %d", 1)
	agentlog.Default.Warn("framework no-context warning")
	stage.End(ctx, "failed", "TASK_FAILURE", errors.New("operation failed"))
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	records := decodeLines(t, output.Bytes())
	var contextual, plain, exported bool
	for _, r := range records {
		switch r["message"] {
		case "framework context 1":
			contextual = r["log_source"] == "framework" && r["trace_id"] != nil && r["span_id"] != nil
		case "framework no-context warning":
			plain = r["log_source"] == "framework" && r["trace_id"] == nil
		case "trace export failed":
			exported = r["log_source"] == "collector" && r["error_code"] == "TRACE_EXPORT_FAILED"
		}
	}
	if !contextual || !plain || !exported {
		t.Fatalf("bridge or export failure missing: context=%t plain=%t export=%t", contextual, plain, exported)
	}
}
