package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

type factSpanExporter struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (x *factSpanExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.spans = append(x.spans, spans...)
	return nil
}
func (*factSpanExporter) Shutdown(context.Context) error { return nil }
func (x *factSpanExporter) Snapshot() []sdktrace.ReadOnlySpan {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]sdktrace.ReadOnlySpan(nil), x.spans...)
}

type fixtureRTWFactBinder struct {
	url     string
	client  *http.Client
	pending bool
}

func (b fixtureRTWFactBinder) BindFact(ctx context.Context, source eventing.Event) (usermodel.Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.url+"/fixture/identity/"+source.AggregateID, nil)
	if err != nil {
		return usermodel.Event{}, err
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	response, err := b.client.Do(req)
	if err != nil {
		return usermodel.Event{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return usermodel.Event{}, errors.New("RTW fixture has no issued subject")
	}
	var subject usermodel.SubjectRef
	if err := json.NewDecoder(response.Body).Decode(&subject); err != nil {
		return usermodel.Event{}, err
	}
	result := usermodel.Event{Subject: subject, Action: usermodel.Assert, Kind: usermodel.Reading,
		Predicate: "read", ValueRef: "article/rev-1", ItemID: "article-1"}
	if b.pending {
		result.Action = usermodel.Correct
		result.Supersedes = &usermodel.EventKey{Producer: "rtw.product", EventID: "missing-predecessor"}
	}
	return result, nil
}

type fixtureFactSource struct {
	batch    eventing.Batch
	receipt  eventing.Receipt
	ackCount int
}

func (s *fixtureFactSource) ReadEvents(context.Context, string, string, int) (eventing.Batch, error) {
	return s.batch, nil
}
func (s *fixtureFactSource) EventReceipt(context.Context, string, string) (eventing.Receipt, error) {
	return s.receipt, nil
}
func (s *fixtureFactSource) AcknowledgeEvents(context.Context, string, eventing.Acknowledge) (eventing.DeliveryReceipt, error) {
	s.ackCount++
	return eventing.DeliveryReceipt{Consumer: s.batch.Consumer, Producer: s.batch.Producer,
		AcknowledgedOffset: s.batch.ToOffset, TechnicalStatus: "delivered"}, nil
}

func TestFactWorkerRejectsMalformedBatchBeforeCommit(t *testing.T) {
	w := &FactWorker{config: FactWorkerConfig{Consumer: "btw-facts", Producer: "rtw.product",
		EventType: "rtw.product.fact.v1", SchemaVersion: 1, BatchLimit: 2},
		bindings: map[factBindingKey]FactEventBinding{{"rtw.product.fact.v1", 1}: {EventType: "rtw.product.fact.v1", SchemaVersion: 1}}}
	base := eventing.Batch{Consumer: "btw-facts", Producer: "rtw.product", FromOffset: 1, ToOffset: 1,
		BatchHash: strings.Repeat("a", 64), Events: []eventing.Item{{Offset: 1,
			InputHash: strings.Repeat("b", 64), Event: eventing.Event{EventID: "e1", Producer: "rtw.product",
				EventType: "rtw.product.fact.v1", SchemaVersion: 1, OperationID: "op-1", AggregateID: "article-1",
				AggregateVersion: 1, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}}}}
	if err := w.validateBatch(base); err != nil {
		t.Fatal(err)
	}
	checks := []func(*eventing.Batch){
		func(b *eventing.Batch) { b.Consumer = "someone-else" },
		func(b *eventing.Batch) { b.Events[0].Offset = 2 },
		func(b *eventing.Batch) { b.Events[0].Event.Producer = "whalehall" },
		func(b *eventing.Batch) { b.Events[0].Event.SchemaVersion = 2 },
		func(b *eventing.Batch) { b.Events[0].Event.OccurredAt = "invalid" },
	}
	for index, change := range checks {
		copy := base
		copy.Events = append([]eventing.Item(nil), base.Events...)
		change(&copy)
		if err := w.validateBatch(copy); !errors.Is(err, ErrFactDeliveryContract) {
			t.Fatalf("case %d: %v", index, err)
		}
	}
}

// The framework installs process-global trace/metric hooks. The child owns
// one Bundle so native Graph spans cannot be inherited from another test.
func TestFactWorkerPostgresDCAndRTWFixture(t *testing.T) {
	if os.Getenv("USERMODEL_TEST_POSTGRES_DSN") == "" {
		t.Skip("run with an isolated PostgreSQL 16 DSN")
	}
	if os.Getenv("SEA_FACT_WORKER_CHILD") == "1" {
		assertFactWorkerPostgresDCAndRTWFixture(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFactWorkerPostgresDCAndRTWFixture$", "-test.v")
	cmd.Env = append(os.Environ(), "SEA_FACT_WORKER_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fact worker isolated acceptance: %v\n%s", err, output)
	}
	if testing.Verbose() {
		t.Log(strings.TrimSpace(string(output)))
	}
}

func factWorkerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	dsn := os.Getenv("USERMODEL_TEST_POSTGRES_DSN")
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "usermodel_worker_" + hex.EncodeToString(nonce[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	if err := usermodel.MigratePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func assertFactWorkerPostgresDCAndRTWFixture(t *testing.T) {
	var logs bytes.Buffer
	exporter := &factSpanExporter{}
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-fact-worker-fixture",
		Environment: "test", Version: "fixed-v1.8.1", InstanceID: "worker-fixture", Output: &logs,
		Level: slog.LevelInfo, TraceExporter: exporter, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	pool := factWorkerPool(t)
	store := usermodel.NewStore(pool, observed)
	graph, err := usermodel.NewFactGraphRuntime("fact-worker-fixture", store, inmemory.NewSessionService(), observed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := graph.Close(); err != nil {
			t.Error(err)
		}
		if err := observed.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	var dcTrace, rtwTrace string
	var fixtureMu sync.Mutex
	rtw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixtureMu.Lock()
		rtwTrace = r.Header.Get("traceparent")
		fixtureMu.Unlock()
		if r.URL.Path != "/fixture/identity/article-1" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "tenant-a", SubjectID: "issued-user-1"})
	}))
	defer rtw.Close()
	now := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	event := eventing.Event{EventID: "event-1", EventType: "rtw.product.fact.v1", SchemaVersion: 1,
		Producer: "rtw.product", AggregateID: "article-1", AggregateVersion: 1, OperationID: "op-1",
		OccurredAt: now.Format(time.RFC3339Nano), Payload: json.RawMessage(`{"subject_ref":{"authority_id":"evil","tenant_id":"other","subject_id":"impostor"}}`)}
	item := eventing.Item{Offset: 1, InputHash: strings.Repeat("a", 64), Event: event}
	batch := eventing.Batch{Consumer: "btw-facts", Producer: "rtw.product", FromOffset: 1, ToOffset: 1,
		BatchHash: strings.Repeat("b", 64), Events: []eventing.Item{item}}
	ackCount, loseFirstACK := 0, true
	dc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixtureMu.Lock()
		defer fixtureMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/event-consumers/btw-facts/events":
			if ackCount == 0 {
				_ = json.NewEncoder(w).Encode(batch)
			} else {
				_ = json.NewEncoder(w).Encode(eventing.Batch{Consumer: "btw-facts", Producer: "rtw.product",
					FromOffset: 2, ToOffset: 1, BatchHash: strings.Repeat("c", 64), Events: []eventing.Item{}})
			}
		case "/v1/events/rtw.product/event-1":
			dcTrace = r.Header.Get("traceparent")
			_ = json.NewEncoder(w).Encode(eventing.Receipt{EventID: "event-1", Producer: "rtw.product",
				TechnicalStatus: "accepted", ReceiptID: "dc-receipt-1", InputHash: item.InputHash,
				Offset: 1, ReceivedAt: now.Add(time.Second).Format(time.RFC3339Nano)})
		case "/v1/event-consumers/btw-facts/ack":
			if loseFirstACK {
				loseFirstACK = false
				http.Error(w, "fixture ACK unavailable", http.StatusServiceUnavailable)
				return
			}
			ackCount++
			_ = json.NewEncoder(w).Encode(eventing.DeliveryReceipt{Consumer: "btw-facts", Producer: "rtw.product",
				AcknowledgedOffset: 1, TechnicalStatus: "delivered"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer dc.Close()
	source, err := datacenter.New(httpclient.Config{BaseURL: dc.URL, Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewFactWorker(FactWorkerConfig{Consumer: "btw-facts", Producer: "rtw.product",
		EventType: "rtw.product.fact.v1", SchemaVersion: 1, BatchLimit: 10}, source,
		fixtureRTWFactBinder{url: rtw.URL, client: rtw.Client()}, graph, store, observed)
	if err != nil {
		t.Fatal(err)
	}
	first, err := worker.RunOnce(context.Background())
	fixtureMu.Lock()
	firstACKCount := ackCount
	fixtureMu.Unlock()
	if err == nil || first.Count != 1 || first.NewFacts != 1 || first.AckedOffset != 0 || firstACKCount != 0 {
		t.Fatalf("lost ACK first=%+v err=%v ack=%d", first, err, firstACKCount)
	}
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "tenant-a", SubjectID: "issued-user-1"}
	outbox, err := store.OutboxAfter(context.Background(), subject, 0, 10)
	if err != nil || len(outbox) != 1 || outbox[0].StateVersion != 1 {
		t.Fatalf("PG fact/Outbox not committed before ACK attempt: %+v %v", outbox, err)
	}
	second, err := worker.RunOnce(context.Background())
	fixtureMu.Lock()
	secondACKCount := ackCount
	fixtureMu.Unlock()
	if err != nil || second.Count != 1 || second.Replayed != 1 || second.NewFacts != 0 || second.AckedOffset != 1 || secondACKCount != 1 {
		t.Fatalf("idempotent replay second=%+v err=%v ack=%d", second, err, secondACKCount)
	}
	third, err := worker.RunOnce(context.Background())
	if err != nil || !third.Empty || third.Count != 0 || third.AckedOffset != 0 {
		t.Fatalf("empty batch=%+v err=%v", third, err)
	}
	current, err := store.Current(context.Background(), subject)
	if err != nil || current.StateVersion != 1 || len(current.Active) != 1 ||
		current.Active[0].EvidenceRef != "dc:event:rtw.product:1" || current.Active[0].EvidenceHash != item.InputHash {
		t.Fatalf("subject/evidence mismatch: %+v %v", current, err)
	}
	fixtureMu.Lock()
	actualDCTrace, actualRTWTrace := dcTrace, rtwTrace
	fixtureMu.Unlock()
	if actualDCTrace == "" || actualRTWTrace == "" || actualDCTrace[:min(len(actualDCTrace), 35)] != actualRTWTrace[:min(len(actualRTWTrace), 35)] {
		t.Fatalf("missing cross-service W3C context DC=%q RTW=%q", actualDCTrace, actualRTWTrace)
	}
	pendingEvent := event
	pendingEvent.EventID = "event-2"
	pendingEvent.AggregateVersion = 2
	pendingEvent.OperationID = "op-2"
	pendingItem := eventing.Item{Offset: 2, InputHash: strings.Repeat("d", 64), Event: pendingEvent}
	pendingSource := &fixtureFactSource{batch: eventing.Batch{Consumer: "btw-facts", Producer: "rtw.product",
		FromOffset: 2, ToOffset: 2, BatchHash: strings.Repeat("e", 64), Events: []eventing.Item{pendingItem}},
		receipt: eventing.Receipt{EventID: "event-2", Producer: "rtw.product", TechnicalStatus: "accepted",
			ReceiptID: "dc-receipt-2", InputHash: pendingItem.InputHash, Offset: 2,
			ReceivedAt: now.Add(2 * time.Second).Format(time.RFC3339Nano)}}
	pendingWorker, err := NewFactWorker(FactWorkerConfig{Consumer: "btw-facts", Producer: "rtw.product",
		EventType: "rtw.product.fact.v1", SchemaVersion: 1, BatchLimit: 10}, pendingSource,
		fixtureRTWFactBinder{url: rtw.URL, client: rtw.Client(), pending: true}, graph, store, observed)
	if err != nil {
		t.Fatal(err)
	}
	pendingResult, err := pendingWorker.RunOnce(context.Background())
	if !errors.Is(err, ErrFactDeliveryPending) || pendingResult.AckedOffset != 0 || pendingSource.ackCount != 0 {
		t.Fatalf("pending predecessor escaped delivery gate: %+v %v ack=%d", pendingResult, err, pendingSource.ackCount)
	}
	projected, err := store.Current(context.Background(), subject)
	if err != nil || projected.Pending != 1 || projected.StateVersion != 1 {
		t.Fatalf("pending fact wrongly accepted: %+v %v", projected, err)
	}
	outbox, err = store.OutboxAfter(context.Background(), subject, 0, 10)
	if err != nil || len(outbox) != 1 {
		t.Fatalf("pending fact created accepted Outbox: %+v %v", outbox, err)
	}
	predecessor := usermodel.Event{Subject: subject, EventKey: usermodel.EventKey{Producer: "rtw.product", EventID: "missing-predecessor"},
		Action: usermodel.Assert, Kind: usermodel.Reading, Predicate: "read", ValueRef: "article/rev-1",
		EvidenceRef: "rtw/event/missing-predecessor", EvidenceHash: strings.Repeat("a", 64),
		OccurredAt: now.Add(-time.Minute), ObservedAt: now, SourcePartition: "rtw-fixture", ItemID: "article-1"}
	if _, err := store.Append(context.Background(), predecessor); err != nil {
		t.Fatalf("admit late predecessor: %v", err)
	}
	currentReceipt, err := store.CurrentReceipt(context.Background(), subject, usermodel.EventKey{Producer: "rtw.product", EventID: "event-2"})
	if err != nil || currentReceipt.Status != "accepted" || currentReceipt.StateVersion != 3 {
		t.Fatalf("pending event did not gain current accepted Outbox: %+v %v", currentReceipt, err)
	}
	resumed, err := pendingWorker.RunOnce(context.Background())
	if err != nil || resumed.Count != 1 || resumed.Replayed != 1 || resumed.AckedOffset != 2 || pendingSource.ackCount != 1 {
		t.Fatalf("promoted pending event did not ACK exactly once: %+v %v ack=%d", resumed, err, pendingSource.ackCount)
	}
	outbox, err = store.OutboxAfter(context.Background(), subject, 0, 10)
	if err != nil || len(outbox) != 3 {
		t.Fatalf("pending replay duplicated Outbox: %+v %v", outbox, err)
	}
	metrics := httptest.NewRecorder()
	observed.MetricsHandler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	metricBody := metrics.Body.String()
	if metrics.Code != http.StatusOK ||
		!strings.Contains(metricBody, `sea_btw_operations_total{component="usermodel",outcome="succeeded"}`) ||
		!strings.Contains(metricBody, `sea_btw_operations_total{component="usermodel",outcome="failed"}`) ||
		!strings.Contains(metricBody, "trpc_agent_go_agent_") ||
		strings.Contains(metricBody, subject.SubjectID) || strings.Contains(metricBody, "event-1") {
		var selected []string
		for _, line := range strings.Split(metricBody, "\n") {
			if strings.HasPrefix(line, "sea_btw_operations_total{") || strings.HasPrefix(line, "trpc_") || strings.HasPrefix(line, "# HELP trpc_") {
				selected = append(selected, line)
			}
		}
		t.Fatalf("application/native metrics missing or high-cardinality identity leaked: status=%d selected=%v", metrics.Code, selected)
	}
	if err := graph.Close(); err != nil {
		t.Fatal(err)
	}
	if err := observed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exporter.Snapshot()
	names := map[string]bool{}
	byID := map[trace.SpanID]sdktrace.ReadOnlySpan{}
	for _, span := range spans {
		names[span.Name()] = true
		byID[span.SpanContext().SpanID()] = span
	}
	for _, name := range []string{"usermodel.worker.batch", "usermodel.worker.event", "runtime.run",
		"invoke_agent usermodel_fact", "workflow execute_graph usermodel_fact",
		"workflow execute_function_node commit_fact", "usermodel.fact.append"} {
		if !names[name] {
			t.Fatalf("missing native/application span %q from %+v", name, names)
		}
	}
	for _, native := range []string{"invoke_agent usermodel_fact", "workflow execute_graph usermodel_fact",
		"workflow execute_function_node commit_fact"} {
		foundChild := false
		for _, span := range spans {
			if span.Name() != native || span.InstrumentationScope().Name != "trpc.agent.go" {
				continue
			}
			for parentID := span.Parent().SpanID(); parentID.IsValid(); {
				parent, exists := byID[parentID]
				if !exists || parent.SpanContext().TraceID() != span.SpanContext().TraceID() {
					break
				}
				if parent.Name() == "usermodel.worker.event" {
					foundChild = true
					break
				}
				parentID = parent.Parent().SpanID()
			}
		}
		if !foundChild {
			t.Fatalf("native span %q is not beneath the worker event in one trace", native)
		}
	}
	var linkedTerminal, failedACKTerminal, pendingTerminal bool
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("non-JSON log: %s: %v", line, err)
		}
		if record["event"] == "usermodel.worker.event.finished" && record["event_id"] == "event-1" &&
			record["run_id"] == "dc-fact:rtw.product:1" && record["trace_id"] != nil && record["span_id"] != nil {
			linkedTerminal = true
		}
		if record["event"] == "usermodel.worker.batch.finished" && record["outcome"] == "failed" &&
			record["error_code"] == "UPSTREAM_OR_STORE_ERROR" {
			failedACKTerminal = true
		}
		if record["event"] == "usermodel.worker.event.finished" && record["event_id"] == "event-2" &&
			record["outcome"] == "rejected" && record["error_code"] == "FACT_PENDING" {
			pendingTerminal = true
		}
	}
	if !linkedTerminal || !failedACKTerminal || !pendingTerminal {
		t.Fatalf("worker JSON terminal missing linked/failed/pending outcome: %v %v %v",
			linkedTerminal, failedACKTerminal, pendingTerminal)
	}
}
