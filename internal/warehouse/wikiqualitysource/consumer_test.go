package wikiqualitysource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testSource struct {
	batch     eventing.Batch
	receipts  map[string]eventing.Receipt
	ackLost   bool
	ackOffset int64
}

func (s *testSource) ReadEvents(_ context.Context, consumer, producer string, _ int) (eventing.Batch, error) {
	if consumer != DefaultConsumer || producer != Producer {
		return eventing.Batch{}, ErrContract
	}
	if s.ackOffset != 0 {
		return eventing.Batch{Consumer: consumer, Producer: producer}, nil
	}
	return s.batch, nil
}

func (s *testSource) EventReceipt(_ context.Context, producer, eventID string) (eventing.Receipt, error) {
	if producer != Producer {
		return eventing.Receipt{}, ErrContract
	}
	value, ok := s.receipts[eventID]
	if !ok {
		return eventing.Receipt{}, ErrContract
	}
	return value, nil
}

func (s *testSource) AcknowledgeEvents(_ context.Context, consumer string, ack eventing.Acknowledge) (eventing.DeliveryReceipt, error) {
	if consumer != DefaultConsumer || ack.Producer != Producer || ack.FromOffset != 1 ||
		ack.ToOffset != s.batch.ToOffset || ack.BatchHash != s.batch.BatchHash {
		return eventing.DeliveryReceipt{}, ErrContract
	}
	if s.ackLost {
		s.ackLost = false
		return eventing.DeliveryReceipt{}, errors.New("fixture ACK receipt lost")
	}
	s.ackOffset = ack.ToOffset
	return eventing.DeliveryReceipt{Consumer: consumer, Producer: Producer,
		AcknowledgedOffset: ack.ToOffset, TechnicalStatus: "delivered"}, nil
}

type testAuthority struct {
	raw []byte
	sha string
}

func (a testAuthority) ReadQualityEvent(_ context.Context, eventID string) ([]byte, string, error) {
	if eventID != "quality-event-2" {
		return nil, "", ErrContract
	}
	return a.raw, a.sha, nil
}

// This verifier belongs only to synthetic transaction tests. The production
// rubric verifier must come from RTW's frozen payload/source contract.
type syntheticVerifier struct{}

func (syntheticVerifier) VerifyQualityEvent(_ context.Context, event eventing.Event, original []byte) error {
	if event.EventType != Judged || event.EventID != "quality-event-2" || len(original) == 0 {
		return ErrContract
	}
	return nil
}

func testBatch(t *testing.T) (eventing.Batch, map[string]eventing.Receipt, testAuthority) {
	t.Helper()
	events := []eventing.Event{
		{EventID: "other-event-1", EventType: "knowledge.module.created.v1", SchemaVersion: 1,
			Producer: Producer, AggregateID: "module-1", AggregateVersion: 1,
			OperationID: "operation-1", OccurredAt: "2026-09-16T00:00:00Z", Payload: json.RawMessage(`{"kind":"source"}`)},
		{EventID: "quality-event-2", EventType: Judged, SchemaVersion: 1,
			Producer: Producer, AggregateID: "module-1", AggregateVersion: 2,
			OperationID: "operation-2", OccurredAt: "2026-09-16T00:01:00Z", Payload: json.RawMessage(`{"fixture_only":true}`)},
		{EventID: "other-event-3", EventType: "knowledge.search.judgment.revised.v1", SchemaVersion: 1,
			Producer: Producer, AggregateID: "module-1", AggregateVersion: 3,
			OperationID: "operation-3", OccurredAt: "2026-09-16T00:02:00Z", Payload: json.RawMessage(`{"kind":"qrel"}`)},
	}
	items := make([]eventing.Item, 0, len(events))
	receipts := make(map[string]eventing.Receipt, len(events))
	for i, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		canonicalEvent, err := canonical(raw)
		if err != nil {
			t.Fatal(err)
		}
		offset := int64(i + 1)
		items = append(items, eventing.Item{Offset: offset, InputHash: digest(canonicalEvent), Event: event})
		receipts[event.EventID] = eventing.Receipt{EventID: event.EventID, Producer: Producer,
			TechnicalStatus: "accepted", ReceiptID: "receipt-" + event.EventID,
			InputHash: digest(canonicalEvent), Offset: offset, ReceivedAt: "2026-09-16T00:05:00Z"}
	}
	original, err := json.MarshalIndent(events[1], "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	batch := eventing.Batch{Consumer: DefaultConsumer, Producer: Producer,
		FromOffset: 1, ToOffset: int64(len(items)), Events: items}
	resetBatchHash(t, &batch)
	return batch, receipts, testAuthority{raw: original, sha: digest(original)}
}

func resetBatchHash(t *testing.T, batch *eventing.Batch) {
	t.Helper()
	raw, err := json.Marshal(batch.Events)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	batch.BatchHash = digest(encoded)
}

func TestQualityProofRejectsForgedHashAndUnknownVersion(t *testing.T) {
	batch, _, authority := testBatch(t)
	if err := validateBatch(batch); err != nil {
		t.Fatalf("valid shared Knowledge prefix rejected: %v", err)
	}
	if err := proveOriginalEvent(batch.Events[1].Event, authority.raw, authority.sha,
		batch.Events[1].InputHash); err != nil {
		t.Fatalf("RTW whitespace differs but JCS agrees: %v", err)
	}
	dcRaw, _ := json.Marshal(batch.Events[1].Event)
	if bytes.Equal(dcRaw, authority.raw) || digest(dcRaw) == authority.sha {
		t.Fatal("fixture did not preserve distinct RTW original bytes")
	}
	if !errors.Is(proveOriginalEvent(batch.Events[1].Event, authority.raw, strings.Repeat("0", 64),
		batch.Events[1].InputHash), ErrContract) ||
		!errors.Is(proveOriginalEvent(batch.Events[1].Event, authority.raw, authority.sha,
			strings.Repeat("0", 64)), ErrContract) {
		t.Fatal("forged RTW/DC SHA was accepted")
	}
	batch.Events[1].Event.EventType = "knowledge.wiki.quality.judged.v2"
	raw, _ := json.Marshal(batch.Events[1].Event)
	encoded, _ := canonical(raw)
	batch.Events[1].InputHash = digest(encoded)
	resetBatchHash(t, &batch)
	if !errors.Is(validateBatch(batch), ErrContract) {
		t.Fatal("unknown Wiki quality version became a technical skip")
	}
	batch.Events[1].Offset = 4
	if !errors.Is(validateBatch(batch), ErrContract) {
		t.Fatal("shared producer offset gap was accepted")
	}
}

func TestIndependentRealPGQualityCursorStopsWithoutRubricAndReplaysAfterLostACK(t *testing.T) {
	dsn := os.Getenv("WIKI_QUALITY_SOURCE_TEST_DSN")
	if dsn == "" {
		t.Skip("use isolated PostgreSQL DSN for Wiki quality ODS transaction acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Initialize(ctx, pool); err != nil {
		t.Fatal(err)
	}
	reset := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `TRUNCATE warehouse_wiki_quality.ods_fact_set,
 warehouse_wiki_quality.ods_event,
 warehouse_wiki_quality.consumer_cursor`); err != nil {
			t.Fatal(err)
		}
	}
	reset()
	batch, receipts, authority := testBatch(t)
	source := &testSource{batch: batch, receipts: receipts, ackLost: true}
	consumer := &Consumer{DB: pool, Source: source, Consumer: DefaultConsumer}
	if _, err := consumer.RunOnce(ctx); !errors.Is(err, ErrQualityUnconfigured) {
		t.Fatalf("unfrozen quality contract progressed: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM warehouse_wiki_quality.ods_event`).Scan(&count); err != nil || count != 0 || source.ackOffset != 0 {
		t.Fatalf("unconfigured rubric committed/ACKed: rows=%d ack=%d %v", count, source.ackOffset, err)
	}
	consumer.Authority, consumer.Verifier = authority, syntheticVerifier{}
	badAuthority := authority
	badAuthority.sha = strings.Repeat("0", 64)
	consumer.Authority = badAuthority
	if _, err := consumer.RunOnce(ctx); !errors.Is(err, ErrContract) {
		t.Fatalf("forged RTW raw SHA accepted: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM warehouse_wiki_quality.ods_event`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("forged original committed: rows=%d %v", count, err)
	}
	consumer.Authority = authority
	first, err := consumer.RunOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), "ACK") || first.Read != 3 ||
		first.QualityVerified != 1 || first.TechnicalSkips != 2 ||
		first.CommittedOffset != 3 || first.AcknowledgedOffset != 0 {
		t.Fatalf("PG commit followed by lost ACK: %+v %v", first, err)
	}
	var skip, cursor int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE status='technical_skip')
 FROM warehouse_wiki_quality.ods_event`).Scan(&count, &skip); err != nil || count != 3 || skip != 2 {
		t.Fatalf("mixed Knowledge prefix ODS: total=%d skip=%d %v", count, skip, err)
	}
	var recordedOriginal []byte
	var recordedSHA string
	if err := pool.QueryRow(ctx, `SELECT authority_event_json,authority_event_sha256
 FROM warehouse_wiki_quality.ods_event WHERE source_offset=2`).Scan(&recordedOriginal, &recordedSHA); err != nil ||
		!bytes.Equal(recordedOriginal, authority.raw) || recordedSHA != authority.sha {
		t.Fatalf("RTW original bytes changed after ODS insert: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT committed_offset FROM warehouse_wiki_quality.consumer_cursor
 WHERE consumer=$1`, DefaultConsumer).Scan(&cursor); err != nil || cursor != 3 {
		t.Fatalf("independent committed cursor=%d %v", cursor, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE warehouse_wiki_quality.ods_event
 SET event_type='knowledge.wiki.quality.judged.v2' WHERE source_offset=2`); err == nil {
		t.Fatal("frozen ODS quality source was rewritten")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM warehouse_wiki_quality.ods_event
 WHERE source_offset=2`); err == nil {
		t.Fatal("frozen ODS quality source was deleted")
	}
	modified := source.receipts[batch.Events[1].Event.EventID]
	modified.ReceiptID = "forged-receipt"
	source.receipts[modified.EventID] = modified
	if _, err := consumer.RunOnce(ctx); !errors.Is(err, ErrContract) || source.ackOffset != 0 {
		t.Fatalf("changed DC technical receipt replay ACKed: %v ack=%d", err, source.ackOffset)
	}
	modified.ReceiptID = "receipt-" + modified.EventID
	source.receipts[modified.EventID] = modified
	second, err := consumer.RunOnce(ctx)
	if err != nil || second.Read != 3 || second.CommittedOffset != 3 || second.AcknowledgedOffset != 3 {
		t.Fatalf("lost ACK replay was not idempotent: %+v %v", second, err)
	}
	third, err := consumer.RunOnce(ctx)
	if err != nil || third.Read != 0 {
		t.Fatalf("ACKed prefix redelivered: %+v %v", third, err)
	}
	reset()
}
