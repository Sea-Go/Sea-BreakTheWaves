package usermodel

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	usermodelmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/usermodel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type retainingExporter struct{ *tracetest.InMemoryExporter }

func (e retainingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	return e.InMemoryExporter.ExportSpans(ctx, spans)
}
func (e retainingExporter) Shutdown(context.Context) error { return nil }

func testStore(t *testing.T, bundle *telemetry.Bundle) *Store {
	t.Helper()
	dsn := os.Getenv("USERMODEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("USERMODEL_TEST_POSTGRES_DSN unset; run with an isolated PostgreSQL instance")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "usermodel_test_" + hex.EncodeToString(nonce[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
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
	if _, err := pool.Exec(ctx, usermodelmigration.SQL); err != nil {
		t.Fatal(err)
	}
	return NewStore(pool, bundle)
}

func fixture(id string, sequence int64) Event {
	sum := sha256.Sum256([]byte("source:" + id))
	at := time.Date(2026, 9, 14, 5, 0, 0, 0, time.UTC)
	return Event{Subject: SubjectRef{"rtw.identity", "tenant-a", "user-1"},
		EventKey: EventKey{"rtw.product", id}, Action: Assert, Kind: Reading,
		Predicate: "read", ValueRef: "article/rev-1", EvidenceRef: "rtw/event/" + id,
		EvidenceHash: hex.EncodeToString(sum[:]), OccurredAt: at.Add(time.Duration(sequence) * time.Minute),
		ObservedAt: at.Add(time.Duration(sequence) * time.Minute), SourcePartition: "product-1",
		SourceSequence: sequence, ItemID: "article-1"}
}

func requireAppend(t *testing.T, s *Store, e Event) Receipt {
	t.Helper()
	r, err := s.Append(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPostgresReplayConflictIsolationAndAtomicOutbox(t *testing.T) {
	s := testStore(t, nil)
	ctx := context.Background()
	e := fixture("e1", 1)
	first := requireAppend(t, s, e)
	if first.StateVersion != 1 || first.Status != "accepted" || first.Replay {
		t.Fatalf("first receipt %+v", first)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Append(ctx, e)
			if err != nil || !r.Replay || r.StateVersion != 1 || r.NormalizedHash != first.NormalizedHash {
				t.Errorf("replay %+v %v", r, err)
			}
		}()
	}
	wg.Wait()
	changed := e
	changed.ValueRef = "article/rev-2"
	if _, err := s.Append(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed payload %v", err)
	}
	p, err := s.Current(ctx, e.Subject)
	if err != nil || p.StateVersion != 1 || len(p.Active) != 1 || p.Active[0].ImpressionLinked {
		t.Fatalf("projection %+v %v", p, err)
	}
	out, err := s.OutboxAfter(ctx, e.Subject, 0, 10)
	if err != nil || len(out) != 1 || out[0].StateVersion != 1 || out[0].EventType != "usermodel.fact.accepted" {
		t.Fatalf("outbox %+v %v", out, err)
	}
	other := e.Subject
	other.TenantID = "tenant-b"
	if _, err := s.Current(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross tenant projection %v", err)
	}
	e.Subject = other
	if r := requireAppend(t, s, e); r.StateVersion != 1 || r.Replay {
		t.Fatalf("same event ID in other tenant %+v", r)
	}
	var rows int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM usermodel_events WHERE authority_id='rtw.identity'`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("fact count %d %v", rows, err)
	}
}

func TestPostgresOutOfOrderCorrectionRetractionAndHistory(t *testing.T) {
	s := testStore(t, nil)
	ctx := context.Background()
	three, one, two := fixture("e3", 3), fixture("e1", 1), fixture("e2", 2)
	requireAppend(t, s, three)
	p, err := s.Current(ctx, one.Subject)
	if err != nil || len(p.Watermarks) != 1 || p.Watermarks[0].ContiguousSequence != 0 || p.Watermarks[0].MaxSeenSequence != 3 || p.Watermarks[0].Complete {
		t.Fatalf("first gap %+v %v", p.Watermarks, err)
	}
	requireAppend(t, s, one)
	requireAppend(t, s, two)
	p, err = s.Current(ctx, one.Subject)
	if err != nil || p.StateVersion != 3 || p.Watermarks[0].ContiguousSequence != 3 || len(p.Active) != 3 {
		t.Fatalf("filled gap %+v %v", p, err)
	}
	correct := fixture("e4", 4)
	correct.Action, correct.Supersedes, correct.ValueRef = Correct, &one.EventKey, "article/rev-2"
	requireAppend(t, s, correct)
	retract := fixture("e5", 5)
	retract.Action, retract.Supersedes = Retract, &correct.EventKey
	requireAppend(t, s, retract)
	p, err = s.Current(ctx, one.Subject)
	if err != nil || p.StateVersion != 5 || len(p.Active) != 2 {
		t.Fatalf("after retraction %+v %v", p, err)
	}
	history, err := s.History(ctx, one.Subject, 10)
	if err != nil || len(history) != 5 {
		t.Fatalf("history %+v %v", history, err)
	}
	if history[0].ValueRef != one.ValueRef {
		t.Fatalf("original overwritten %+v", history[0])
	}
	page1, cursor, err := s.HistoryAfter(ctx, one.Subject, HistoryCursor{}, 2)
	if err != nil || len(page1) != 2 {
		t.Fatalf("history page 1 %+v %v", page1, err)
	}
	page2, _, err := s.HistoryAfter(ctx, one.Subject, cursor, 10)
	if err != nil || len(page2) != 3 || page2[0].EventID == page1[1].EventID {
		t.Fatalf("history page 2 %+v %v", page2, err)
	}
	out, err := s.OutboxAfter(ctx, one.Subject, 0, 10)
	if err != nil || len(out) != 5 {
		t.Fatalf("outbox versions %+v %v", out, err)
	}
}

func TestPostgresPendingDependencyAndIdentityBackfill(t *testing.T) {
	s := testStore(t, nil)
	ctx := context.Background()
	original := fixture("target", 1)
	correction := fixture("replacement", 2)
	correction.Action, correction.Supersedes = Correct, &original.EventKey
	pending := requireAppend(t, s, correction)
	if pending.Status != "pending_dependency" || pending.StateVersion != 0 {
		t.Fatalf("pending %+v", pending)
	}
	p, err := s.Current(ctx, original.Subject)
	if err != nil || p.Pending != 1 || p.StateVersion != 0 || len(p.Active) != 0 || p.Watermarks[0].MaxSeenSequence != 2 {
		t.Fatalf("pending projection %+v %v", p, err)
	}
	requireAppend(t, s, original)
	p, err = s.Current(ctx, original.Subject)
	if err != nil || p.Pending != 0 || p.StateVersion != 2 || len(p.Active) != 1 || p.Active[0].EventID != correction.EventID || p.Watermarks[0].ContiguousSequence != 2 {
		t.Fatalf("resolved projection %+v %v", p, err)
	}
	replay := requireAppend(t, s, correction)
	if !replay.Replay || replay.Status != "pending_dependency" || replay.StateVersion != 0 {
		t.Fatalf("original receipt changed %+v", replay)
	}
	if n, err := s.ReconcilePending(ctx, original.Subject); err != nil || n != 0 {
		t.Fatalf("reconcile %d %v", n, err)
	}

	unmapped := fixture("unmapped", 3)
	unmapped.Subject.SubjectID = ""
	ref := UnmappedRef{"rtw.identity", "tenant-a", "legacy-user-4"}
	parked, err := s.ParkUnmapped(ctx, ref, unmapped)
	if err != nil || parked.Status != "pending_subject" {
		t.Fatalf("park %+v %v", parked, err)
	}
	bound, err := s.BindUnmapped(ctx, ref, unmapped.EventKey, original.Subject)
	if err != nil || bound.StateVersion != 3 || bound.Subject != original.Subject {
		t.Fatalf("bind %+v %v", bound, err)
	}
	boundAgain, err := s.BindUnmapped(ctx, ref, unmapped.EventKey, original.Subject)
	if err != nil || !boundAgain.Replay || boundAgain.StateVersion != 3 {
		t.Fatalf("bind replay %+v %v", boundAgain, err)
	}
	parkAgain, err := s.ParkUnmapped(ctx, ref, unmapped)
	if err != nil || !parkAgain.Replay || parkAgain.Status != "pending_subject" || parkAgain.StateVersion != 0 {
		t.Fatalf("original parking receipt changed %+v %v", parkAgain, err)
	}
	wrong := original.Subject
	wrong.SubjectID = "other-user"
	if _, err := s.BindUnmapped(ctx, ref, unmapped.EventKey, wrong); !errors.Is(err, ErrConflict) {
		t.Fatalf("rival binding %v", err)
	}
	second := fixture("unmapped-second", 4)
	second.Subject.SubjectID = ""
	if _, err := s.ParkUnmapped(ctx, ref, second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindUnmapped(ctx, ref, second.EventKey, wrong); !errors.Is(err, ErrConflict) {
		t.Fatalf("same source alias split across events: %v", err)
	}
}

func TestPostgresReadingDoesNotInventImpression(t *testing.T) {
	s := testStore(t, nil)
	ctx := context.Background()
	reading := fixture("read-1", 1)
	forged := reading
	forged.ImpressionID = "imp-1"
	if _, err := s.Append(ctx, forged); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reading carried forged impression ID: %v", err)
	}
	requireAppend(t, s, reading)
	if _, err := s.LinkImpression(ctx, reading.Subject, reading.EventKey, "imp-1", "visible/imp-1"); !errors.Is(err, ErrPending) {
		t.Fatalf("link without actual impression %v", err)
	}
	gaps, err := s.CountEvidenceGaps(ctx, reading.Subject)
	if err != nil || gaps["unattributed_reading_or_action"] != 1 {
		t.Fatalf("gaps %+v %v", gaps, err)
	}
	impression := fixture("display-1", 2)
	impression.Kind, impression.Predicate, impression.ValueRef = Impression, "display", "article/rev-1"
	impression.ImpressionID, impression.VisibilityEvidenceRef = "imp-1", "visible/imp-1"
	requireAppend(t, s, impression)
	version, err := s.LinkImpression(ctx, reading.Subject, reading.EventKey, "imp-1", "visible/imp-1")
	if err != nil || version != 3 {
		t.Fatalf("real link %d %v", version, err)
	}
	p, err := s.Current(ctx, reading.Subject)
	if err != nil || len(p.Active) != 2 || !p.Active[0].ImpressionLinked || p.Active[0].LinkedImpression != "imp-1" {
		t.Fatalf("linked projection %+v %v", p, err)
	}
	gaps, err = s.CountEvidenceGaps(ctx, reading.Subject)
	if err != nil || gaps["unattributed_reading_or_action"] != 0 {
		t.Fatalf("linked gaps %+v %v", gaps, err)
	}
	withdrawal := fixture("display-withdrawn", 3)
	withdrawal.Action, withdrawal.Kind, withdrawal.Supersedes = Retract, Impression, &impression.EventKey
	requireAppend(t, s, withdrawal)
	p, err = s.Current(ctx, reading.Subject)
	if err != nil || len(p.Active) != 1 || p.Active[0].ImpressionLinked {
		t.Fatalf("withdrawn impression remained linked %+v %v", p, err)
	}
	gaps, err = s.CountEvidenceGaps(ctx, reading.Subject)
	if err != nil || gaps["unattributed_reading_or_action"] != 1 {
		t.Fatalf("withdrawn link gap %+v %v", gaps, err)
	}
	if _, err := s.LinkImpression(ctx, reading.Subject, reading.EventKey, "imp-1", "visible/imp-1"); !errors.Is(err, ErrPending) {
		t.Fatalf("withdrawn impression linked again %v", err)
	}
}

func TestPostgresAttributionRequiresSameItemAndRequest(t *testing.T) {
	s := testStore(t, nil)
	ctx := context.Background()
	reading := fixture("read", 1)
	reading.RequestID = "request-a"
	requireAppend(t, s, reading)
	display := fixture("display", 2)
	display.Kind, display.Predicate = Impression, "display"
	display.ImpressionID, display.VisibilityEvidenceRef = "impression-a", "visible/a"
	display.ItemID, display.RequestID = "other-item", "request-a"
	requireAppend(t, s, display)
	if _, err := s.LinkImpression(ctx, reading.Subject, reading.EventKey, "impression-a", "visible/a"); !errors.Is(err, ErrPending) {
		t.Fatalf("wrong item attributed: %v", err)
	}
	corrected := fixture("display-corrected", 3)
	corrected.Action, corrected.Kind, corrected.Predicate = Correct, Impression, "display"
	corrected.Supersedes = &display.EventKey
	corrected.ImpressionID, corrected.VisibilityEvidenceRef = "impression-a", "visible/a"
	corrected.RequestID = "request-b"
	requireAppend(t, s, corrected)
	if _, err := s.LinkImpression(ctx, reading.Subject, reading.EventKey, "impression-a", "visible/a"); !errors.Is(err, ErrPending) {
		t.Fatalf("wrong request attributed: %v", err)
	}
}

func TestPostgresOutboxFailureRollsBackFactAndVersion(t *testing.T) {
	s := testStore(t, nil)
	ctx := context.Background()
	if _, err := s.db.Exec(ctx, "DROP TABLE usermodel_outbox"); err != nil {
		t.Fatal(err)
	}
	e := fixture("rollback", 1)
	if _, err := s.Append(ctx, e); err == nil {
		t.Fatal("expected outbox failure")
	}
	var count int
	if err := s.db.QueryRow(ctx, "SELECT COUNT(*) FROM usermodel_events").Scan(&count); err != nil || count != 0 {
		t.Fatalf("fact committed without outbox: %d %v", count, err)
	}
}

func TestPackageObservabilityWithInjectedProcessBundle(t *testing.T) {
	var logs bytes.Buffer
	exporter := retainingExporter{tracetest.NewInMemoryExporter()}
	bundle, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-usermodel-test", Environment: "test",
		Version: "fixture-sha", InstanceID: "isolated", Output: &logs, TraceExporter: exporter, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	s := testStore(t, bundle)
	accepted := fixture("logged", 1)
	requireAppend(t, s, accepted)
	pending := fixture("pending-logged", 2)
	pending.Action, pending.Supersedes = Correct, &EventKey{"rtw.product", "missing-target"}
	if r := requireAppend(t, s, pending); r.Status != "pending_dependency" {
		t.Fatalf("expected partial receipt %+v", r)
	}
	changed := accepted
	changed.ValueRef = "article/different-revision"
	if _, err := s.Append(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected observable conflict: %v", err)
	}
	if err := bundle.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
	if len(lines) != 6 {
		t.Fatalf("stage records %d: %s", len(lines), logs.String())
	}
	for _, line := range lines {
		var fields map[string]any
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"timestamp", "level", "service", "service_version", "component", "log_source", "event", "message"} {
			if fields[key] == nil {
				t.Fatalf("missing %s: %s", key, line)
			}
		}
	}
	var pendingEnd, conflictEnd map[string]any
	if err := json.Unmarshal(lines[3], &pendingEnd); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[5], &conflictEnd); err != nil {
		t.Fatal(err)
	}
	if pendingEnd["outcome"] != "partial" || pendingEnd["receipt_status"] != "pending_dependency" ||
		conflictEnd["outcome"] != "rejected" || conflictEnd["error_code"] != "FACT_CONFLICT" {
		t.Fatalf("terminal stage classification: %s", logs.String())
	}
	if len(exporter.GetSpans()) != 3 {
		t.Fatalf("domain spans exported: %d", len(exporter.GetSpans()))
	}
}

func TestReplayOrderHonorsSourceSequenceBeforeCrossSourceTime(t *testing.T) {
	a := fixture("a", 1)
	b := fixture("b", 2)
	b.OccurredAt = a.OccurredAt.Add(-time.Hour) // source clock drift cannot invert producer sequence
	c := fixture("c", 1)
	c.Producer, c.SourcePartition = "whalehall", "device-1"
	c.OccurredAt = a.OccurredAt.Add(-time.Minute)
	ordered := ReplayOrder([]Fact{{Event: b}, {Event: a}, {Event: c}})
	if len(ordered) != 3 || ordered[0].EventID != "c" || ordered[1].EventID != "a" || ordered[2].EventID != "b" {
		t.Fatalf("deterministic replay order: %+v", ordered)
	}
}
