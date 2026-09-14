package searchsource

import (
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

type fixtureSource struct {
	batch     eventing.Batch
	receipts  map[string]eventing.Receipt
	ackLost   bool
	ackOffset int64
}

func (s *fixtureSource) ReadEvents(_ context.Context, consumer, producer string, _ int) (eventing.Batch, error) {
	if consumer != DefaultConsumer || producer != Producer {
		return eventing.Batch{}, ErrContract
	}
	if s.ackOffset != 0 {
		return eventing.Batch{Consumer: consumer, Producer: producer}, nil
	}
	return s.batch, nil
}

func (s *fixtureSource) EventReceipt(_ context.Context, producer, eventID string) (eventing.Receipt, error) {
	value, ok := s.receipts[eventID]
	if !ok || producer != Producer {
		return eventing.Receipt{}, ErrContract
	}
	return value, nil
}

func (s *fixtureSource) AcknowledgeEvents(_ context.Context, consumer string, ack eventing.Acknowledge) (eventing.DeliveryReceipt, error) {
	if s.ackLost {
		s.ackLost = false
		return eventing.DeliveryReceipt{}, errors.New("isolated ACK receipt loss")
	}
	if consumer != DefaultConsumer || ack.Producer != Producer || ack.FromOffset != 1 || ack.ToOffset != s.batch.ToOffset ||
		ack.BatchHash != s.batch.BatchHash {
		return eventing.DeliveryReceipt{}, ErrContract
	}
	s.ackOffset = ack.ToOffset
	return eventing.DeliveryReceipt{Consumer: consumer, Producer: Producer,
		AcknowledgedOffset: ack.ToOffset, TechnicalStatus: "delivered"}, nil
}

type fixtureAuthority map[string][]byte

func (a fixtureAuthority) ReadJudgmentEvent(_ context.Context, eventID string) ([]byte, string, error) {
	raw, ok := a[eventID]
	if !ok {
		return nil, "", ErrContract
	}
	return raw, digest(raw), nil
}

func fixtureBatch(t *testing.T) (eventing.Batch, map[string]eventing.Receipt, fixtureAuthority) {
	t.Helper()
	first := fixtureJudgment()
	firstEvent, firstRaw, _, _ := fixtureEvent(t, first)
	other := firstEvent
	other.EventID, other.EventType, other.AggregateVersion = "event-2", "knowledge.module.created.v1", 2
	last := first
	last.RevisionID, last.JudgmentRevision, last.BaseRevisionID = "revision-2", 2, first.RevisionID
	last.Grade, last.State, last.JudgedAt = nil, "withdrawn", "2026-09-15T00:20:00Z"
	lastPayload, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	lastEvent := firstEvent
	lastEvent.EventID, lastEvent.EventType, lastEvent.AggregateVersion = "event-3", Withdrawn, 3
	lastEvent.Payload, lastEvent.OccurredAt = lastPayload, "2026-09-15T00:21:00Z"
	events := []eventing.Event{firstEvent, other, lastEvent}
	authority := fixtureAuthority{"event-1": firstRaw}
	items := make([]eventing.Item, 0, len(events))
	receipts := make(map[string]eventing.Receipt)
	for i, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := canonical(raw)
		if err != nil {
			t.Fatal(err)
		}
		hash := digest(encoded)
		offset := int64(i + 1)
		items = append(items, eventing.Item{Offset: offset, InputHash: hash, Event: event})
		received := "2026-09-15T00:12:00Z"
		if i == 2 {
			received = "2026-09-15T00:22:00Z"
			authority[event.EventID] = raw
		}
		receipts[event.EventID] = eventing.Receipt{EventID: event.EventID, Producer: Producer,
			TechnicalStatus: "accepted", ReceiptID: "receipt-" + event.EventID,
			InputHash: hash, Offset: offset, ReceivedAt: received}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	return eventing.Batch{Consumer: DefaultConsumer, Producer: Producer,
		FromOffset: 1, ToOffset: int64(len(items)), BatchHash: digest(encoded), Events: items}, receipts, authority
}

func TestRealPGIndependentQrelCursorWithSharedProducerAndLostACK(t *testing.T) {
	dsn := os.Getenv("SEARCH_QREL_SOURCE_TEST_DSN")
	if dsn == "" {
		t.Skip("set an isolated PostgreSQL DSN for qrel source transaction acceptance")
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
	if _, err := pool.Exec(ctx, `TRUNCATE warehouse_search_source.ods_event,warehouse_search_source.consumer_cursor`); err != nil {
		t.Fatal(err)
	}
	batch, receipts, authority := fixtureBatch(t)
	source := &fixtureSource{batch: batch, receipts: receipts, ackLost: true}
	consumer := &Consumer{DB: pool, Source: source, Authority: authority, Consumer: DefaultConsumer}
	first, err := consumer.RunOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), "ACK") || first.Read != 3 || first.Judgments != 2 ||
		first.TechnicalSkips != 1 || first.CommittedOffset != 3 || first.AcknowledgedOffset != 0 {
		t.Fatalf("PG commit and lost DC ACK: %+v %v", first, err)
	}
	var count, skip, cursor int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE status='technical_skip')
 FROM warehouse_search_source.ods_event`).Scan(&count, &skip); err != nil || count != 3 || skip != 1 {
		t.Fatalf("mixed producer ODS rows: %d skipped=%d %v", count, skip, err)
	}
	if err := pool.QueryRow(ctx, `SELECT committed_offset FROM warehouse_search_source.consumer_cursor
 WHERE consumer=$1`, DefaultConsumer).Scan(&cursor); err != nil || cursor != 3 {
		t.Fatalf("independent committed cursor: %d %v", cursor, err)
	}
	second, err := consumer.RunOnce(ctx)
	if err != nil || second.Read != 3 || second.CommittedOffset != 3 || second.AcknowledgedOffset != 3 {
		t.Fatalf("lost ACK replay was not idempotent: %+v %v", second, err)
	}
	third, err := consumer.RunOnce(ctx)
	if err != nil || third.Read != 0 {
		t.Fatalf("ACKed producer redelivered: %+v %v", third, err)
	}
	var currentState string
	var grade *int
	if err := pool.QueryRow(ctx, `SELECT judgment_state,(judgment_payload->>'grade')::int
 FROM warehouse_search_source.ods_event WHERE judgment_revision_id='revision-2'`).Scan(&currentState, &grade); err != nil ||
		currentState != "withdrawn" || grade != nil {
		t.Fatalf("withdrawal revision not frozen as null grade: %s %v %v", currentState, grade, err)
	}
}
