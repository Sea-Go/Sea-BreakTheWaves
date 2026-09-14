package usermodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

type factAppendFunc func(context.Context, Event) (Receipt, error)

func (f factAppendFunc) Append(ctx context.Context, e Event) (Receipt, error) { return f(ctx, e) }

type factGraphSpanExporter struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (x *factGraphSpanExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.spans = append(x.spans, spans...)
	return nil
}
func (*factGraphSpanExporter) Shutdown(context.Context) error { return nil }
func (x *factGraphSpanExporter) Snapshot() []sdktrace.ReadOnlySpan {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]sdktrace.ReadOnlySpan(nil), x.spans...)
}

func TestFactGraphRunOptionAndReceiptContract(t *testing.T) {
	e := fixture("graph-input", 1)
	bad := e
	bad.Subject.TenantID = ""
	if _, err := FactGraphRunOption(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incomplete subject accepted: %v", err)
	}
	option, err := FactGraphRunOption(e)
	if err != nil {
		t.Fatal(err)
	}
	options := agent.RunOptions{RuntimeState: map[string]any{"other": "preserved"}}
	option(&options)
	got, ok := options.RuntimeState[factGraphRequestKey].(Event)
	if !ok || got.Subject != e.Subject || got.EventKey != e.EventKey || options.RuntimeState["other"] != "preserved" {
		t.Fatalf("run option lost fixed identity or other state: %+v", options.RuntimeState)
	}
	if _, complete, err := FactGraphReceiptFromCompletion(nil); complete || err != nil {
		t.Fatalf("nil event complete=%v err=%v", complete, err)
	}
	completion := graph.NewGraphCompletionEvent()
	if _, complete, err := FactGraphReceiptFromCompletion(completion); !complete || !errors.Is(err, ErrFactGraphOutput) {
		t.Fatalf("missing receipt complete=%v err=%v", complete, err)
	}
	completion.StateDelta[factGraphReceiptKey] = []byte(`{"subject_ref":{"authority_id":"a","tenant_id":"t","subject_id":"s"},"producer":"p","event_id":"e","normalized_hash":"` + strings.Repeat("a", 64) + `","status":"pending_dependency","accepted_version":1}`)
	if _, complete, err := FactGraphReceiptFromCompletion(completion); !complete || !errors.Is(err, ErrFactGraphOutput) {
		t.Fatalf("pending fact obtained accepted version: complete=%v err=%v", complete, err)
	}
}

func TestFactGraphRunnerErrorAndCancellation(t *testing.T) {
	var logs bytes.Buffer
	b, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-usermodel-graph-unit",
		Environment: "test", Version: "fixture", InstanceID: "unit", Output: &logs,
		Level: slog.LevelInfo, TraceExporter: &factGraphSpanExporter{}, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := b.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	e := fixture("graph-failure", 1)
	if _, err := NewFactGraphAgent(factAppendFunc(nil)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed nil writer accepted: %v", err)
	}
	for _, scenario := range []string{"store_error", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			started := make(chan struct{})
			writer := factAppendFunc(func(ctx context.Context, got Event) (Receipt, error) {
				if got.Subject != e.Subject || got.EventKey != e.EventKey {
					t.Errorf("Graph changed subject or event key: %+v", got)
				}
				close(started)
				if scenario == "store_error" {
					return Receipt{}, ErrConflict
				}
				<-ctx.Done()
				return Receipt{}, ctx.Err()
			})
			ag, err := NewFactGraphAgent(writer)
			if err != nil {
				t.Fatal(err)
			}
			option, err := FactGraphRunOption(e)
			if err != nil {
				t.Fatal(err)
			}
			r := runner.NewRunner("usermodel-graph-failure-test", ag)
			defer r.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stream, err := r.Run(ctx, "subject-fixture", "session-fixture", model.NewUserMessage("record_user_fact"), option)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("Graph node did not reach writer")
			}
			if scenario == "cancel" {
				cancel()
			}
			var rejected bool
			for ev := range stream {
				got, complete, extractErr := FactGraphReceiptFromCompletion(ev)
				if complete {
					if got != (FactGraphReceipt{}) || extractErr == nil {
						t.Fatal("failed or cancelled Graph emitted accepted receipt")
					}
					if scenario == "store_error" && errors.Is(extractErr, ErrConflict) {
						rejected = true
					}
				}
			}
			if scenario == "store_error" && !rejected {
				t.Fatal("writer failure did not produce bounded Graph rejection")
			}
		})
	}
}

// Framework tracing is process-global in v1.8.1. The child process is needed
// so the evidence cannot be satisfied by another package test's installation.
func TestFactGraphPostgresRunnerNativeTrace(t *testing.T) {
	if os.Getenv("USERMODEL_TEST_POSTGRES_DSN") == "" {
		t.Skip("run internal/usermodel/test-postgres.sh for real PostgreSQL acceptance")
	}
	if os.Getenv("SEA_USERMODEL_GRAPH_CHILD") == "1" {
		assertFactGraphPostgresRunnerNativeTrace(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFactGraphPostgresRunnerNativeTrace$", "-test.v")
	cmd.Env = append(os.Environ(), "SEA_USERMODEL_GRAPH_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated PG/Runner/Graph acceptance: %v\n%s", err, output)
	}
	if testing.Verbose() {
		t.Log(strings.TrimSpace(string(output)))
	}
}

func assertFactGraphPostgresRunnerNativeTrace(t *testing.T) {
	var logs bytes.Buffer
	exporter := &factGraphSpanExporter{}
	b, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-usermodel-graph-test",
		Environment: "test", Version: "fixed-v1.8.1", InstanceID: "isolated-graph", Output: &logs,
		Level: slog.LevelInfo, TraceExporter: exporter, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	s := testStore(t, b)
	sessions := inmemory.NewSessionService()
	g, err := NewFactGraphRuntime("usermodel-graph-test", s, sessions, b)
	if err != nil {
		t.Fatal(err)
	}
	ctx, parent := b.Tracer().Start(context.Background(), "trusted_adapter")
	e := fixture("graph-accepted", 1)
	accepted, err := g.Append(ctx, FactGraphRequest{Event: e, SessionID: "session-accepted", RunID: "run-accepted"})
	parent.End()
	if err != nil || accepted.Status != "accepted" || accepted.AcceptedVersion != 1 || accepted.Replay {
		t.Fatalf("accepted run=%+v err=%v", accepted, err)
	}
	outbox, err := s.OutboxAfter(context.Background(), e.Subject, 0, 10)
	if err != nil || len(outbox) != 1 || outbox[0].EventKey != e.EventKey || outbox[0].StateVersion != accepted.AcceptedVersion {
		t.Fatalf("accepted version lacks same-transaction Outbox: %+v err=%v", outbox, err)
	}
	replay, err := g.Append(context.Background(), FactGraphRequest{Event: e, SessionID: "session-replay", RunID: "run-replay"})
	if err != nil || !replay.Replay || replay.AcceptedVersion != accepted.AcceptedVersion {
		t.Fatalf("idempotent replay=%+v err=%v", replay, err)
	}
	pendingEvent := fixture("graph-pending", 2)
	pendingEvent.Action, pendingEvent.Supersedes = Correct, &EventKey{Producer: "rtw.product", EventID: "missing"}
	pending, err := g.Append(context.Background(), FactGraphRequest{Event: pendingEvent, SessionID: "session-pending", RunID: "run-pending"})
	if err != nil || pending.Status != "pending_dependency" || pending.AcceptedVersion != 0 {
		t.Fatalf("pending emitted fake accepted version: %+v err=%v", pending, err)
	}
	outbox, err = s.OutboxAfter(context.Background(), e.Subject, 0, 10)
	if err != nil || len(outbox) != 1 {
		t.Fatalf("pending created accepted outbox: %+v err=%v", outbox, err)
	}
	changed := e
	changed.ValueRef = "article/different-revision"
	conflict, err := g.Append(context.Background(), FactGraphRequest{Event: changed, SessionID: "session-conflict", RunID: "run-conflict"})
	if err == nil || conflict != (FactGraphReceipt{}) {
		t.Fatalf("conflict returned visible receipt: %+v err=%v", conflict, err)
	}
	cancelledEvent := fixture("graph-cancelled", 3)
	cancelledCtx, cancelRun := context.WithCancel(context.Background())
	cancelRun()
	cancelled, err := g.Append(cancelledCtx, FactGraphRequest{Event: cancelledEvent,
		SessionID: "session-cancelled", RunID: "run-cancelled"})
	if err == nil || cancelled != (FactGraphReceipt{}) {
		t.Fatalf("cancelled Run returned ACK: %+v err=%v", cancelled, err)
	}
	var cancelledCount int
	if err := s.db.QueryRow(context.Background(), "SELECT COUNT(*) FROM usermodel_events WHERE event_id=$1", cancelledEvent.EventID).Scan(&cancelledCount); err != nil || cancelledCount != 0 {
		t.Fatalf("cancelled Run committed fact: count=%d err=%v", cancelledCount, err)
	}
	if _, err := s.db.Exec(context.Background(), "DROP TABLE usermodel_outbox"); err != nil {
		t.Fatal(err)
	}
	failedEvent := fixture("graph-outbox-failure", 3)
	failed, err := g.Append(context.Background(), FactGraphRequest{Event: failedEvent, SessionID: "session-failed", RunID: "run-failed"})
	if err == nil || failed != (FactGraphReceipt{}) {
		t.Fatalf("outbox failure returned visible receipt: %+v err=%v", failed, err)
	}
	var eventCount int
	if err := s.db.QueryRow(context.Background(), "SELECT COUNT(*) FROM usermodel_events WHERE event_id=$1", failedEvent.EventID).Scan(&eventCount); err != nil || eventCount != 0 {
		t.Fatalf("outbox failure committed fact: count=%d err=%v", eventCount, err)
	}
	metrics := httptest.NewRecorder()
	b.MetricsHandler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	metricBody := metrics.Body.String()
	if metrics.Code != 200 || !strings.Contains(metricBody, `sea_btw_operations_total{component="usermodel",outcome="succeeded"}`) ||
		!strings.Contains(metricBody, `sea_btw_operations_total{component="usermodel",outcome="rejected"}`) ||
		!strings.Contains(metricBody, `sea_btw_operations_total{component="runtime",outcome="cancelled"}`) ||
		!strings.Contains(metricBody, "trpc_agent_go_agent_") {
		var selected []string
		for _, line := range strings.Split(metricBody, "\n") {
			if strings.Contains(line, "sea_btw_operations_total") || strings.Contains(line, "trpc_agent_go_agent_") {
				selected = append(selected, line)
			}
		}
		t.Fatalf("missing app/native framework metrics: status=%d lines=%v", metrics.Code, selected)
	}
	for _, identity := range []string{e.Subject.SubjectID, "session-accepted", "run-accepted", e.EventID} {
		if strings.Contains(metrics.Body.String(), identity) {
			t.Fatalf("request identity %q entered native metrics", identity)
		}
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Close(); err != nil {
		t.Fatal(err)
	}
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Close(flushCtx); err != nil {
		t.Fatal(err)
	}
	spans := exporter.Snapshot()
	byName := make(map[string]sdktrace.ReadOnlySpan)
	for _, span := range spans {
		if _, exists := byName[span.Name()]; !exists {
			byName[span.Name()] = span
		}
	}
	root := byName["trusted_adapter"]
	if root == nil {
		t.Fatalf("missing adapter root: %v", graphSpanNames(spans))
	}
	for _, name := range []string{"runtime.run", "invoke_agent usermodel_fact", "workflow execute_graph usermodel_fact",
		"workflow execute_function_node commit_fact", "usermodel.fact.append"} {
		span := byName[name]
		if span == nil || span.SpanContext().TraceID() != root.SpanContext().TraceID() {
			t.Fatalf("missing same-trace span %q: %v", name, graphSpanNames(spans))
		}
		if strings.HasPrefix(name, "invoke_agent") || strings.HasPrefix(name, "workflow ") {
			if span.InstrumentationScope().Name != "trpc.agent.go" {
				t.Fatalf("%s has non-framework scope %s", name, span.InstrumentationScope().Name)
			}
		}
	}
	if byName["runtime.run"].Parent().SpanID() != root.SpanContext().SpanID() ||
		byName["invoke_agent usermodel_fact"].Parent().SpanID() != byName["runtime.run"].SpanContext().SpanID() {
		t.Fatal("adapter -> Runtime -> native Agent ancestry lost")
	}
	if !descendsFromGraph(byName["usermodel.fact.append"], byName["workflow execute_function_node commit_fact"], spans) {
		t.Fatal("PG domain commit span not nested under native Graph node")
	}
	var linkedLog, domainRejected, runtimeStopped bool
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if row["event"] == "usermodel.fact.append.finished" && row["trace_id"] == root.SpanContext().TraceID().String() {
			linkedLog = true
		}
		if row["event"] == "usermodel.fact.append.finished" && row["outcome"] == "rejected" && row["error_code"] == "FACT_CONFLICT" {
			domainRejected = true
		}
		if row["event"] == "runtime.run.finished" && row["run_id"] == "run-conflict" &&
			row["outcome"] == "failed" && row["error_code"] == "RUN_SINK_FAILED" {
			runtimeStopped = true
		}
	}
	if !linkedLog || !domainRejected || !runtimeStopped {
		t.Fatalf("missing linked or rejection JSON stages: linked=%v domain_rejected=%v runtime_stopped=%v",
			linkedLog, domainRejected, runtimeStopped)
	}
	t.Logf("native usermodel graph trace=%s spans=%v", root.SpanContext().TraceID(), graphSpanNames(spans))
}

func graphSpanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name())
	}
	return names
}

func descendsFromGraph(span, root sdktrace.ReadOnlySpan, spans []sdktrace.ReadOnlySpan) bool {
	byID := make(map[trace.SpanID]sdktrace.ReadOnlySpan, len(spans))
	for _, item := range spans {
		byID[item.SpanContext().SpanID()] = item
	}
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
