package communitysource

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind/communityauthority"
	"github.com/jackc/pgx/v5/pgxpool"
)

type windowSource struct {
	mu          sync.Mutex
	stream      Stream
	items       []eventing.Item
	receipts    map[string]eventing.Receipt
	cursor      int64
	failACK     bool
	deliveries  map[int64]eventing.Acknowledge
	readBarrier chan struct{}
	readCount   int
}

func newWindowSource(stream Stream, batch eventing.Batch, receipts map[string]eventing.Receipt) *windowSource {
	return &windowSource{stream: stream, items: append([]eventing.Item(nil), batch.Events...), receipts: receipts,
		deliveries: map[int64]eventing.Acknowledge{}}
}

func (s *windowSource) ReadEvents(_ context.Context, consumer, producer string, limit int) (eventing.Batch, error) {
	s.mu.Lock()
	if consumer != s.stream.Consumer || producer != s.stream.Producer || limit < 1 || limit > 128 {
		s.mu.Unlock()
		return eventing.Batch{}, ErrContract
	}
	from := s.cursor + 1
	batch := eventing.Batch{Consumer: consumer, Producer: producer, FromOffset: from, ToOffset: from - 1,
		Events: []eventing.Item{}}
	for _, item := range s.items {
		if item.Offset < from {
			continue
		}
		if len(batch.Events) == limit {
			break
		}
		batch.Events = append(batch.Events, item)
		batch.ToOffset = item.Offset
	}
	canonical, err := testCanonicalJSON(batch.Events)
	if err == nil {
		batch.BatchHash = testContentHash(canonical)
	}
	barrier := s.readBarrier
	if barrier != nil {
		s.readCount++
		if s.readCount == 2 {
			close(barrier)
		}
	}
	s.mu.Unlock()
	if barrier != nil {
		<-barrier
	}
	return batch, err
}

func (s *windowSource) EventReceipt(_ context.Context, producer, eventID string) (eventing.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if producer != s.stream.Producer {
		return eventing.Receipt{}, ErrContract
	}
	receipt, exists := s.receipts[eventID]
	if !exists {
		return eventing.Receipt{}, ErrContract
	}
	return receipt, nil
}

func (s *windowSource) AcknowledgeEvents(_ context.Context, consumer string, ack eventing.Acknowledge) (eventing.DeliveryReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if consumer != s.stream.Consumer || ack.Producer != s.stream.Producer || ack.ToOffset < ack.FromOffset {
		return eventing.DeliveryReceipt{}, ErrContract
	}
	if s.failACK {
		s.failACK = false
		return eventing.DeliveryReceipt{}, errors.New("lost ACK response")
	}
	if ack.FromOffset <= s.cursor {
		previous, exists := s.deliveries[ack.FromOffset]
		if !exists || previous != ack {
			return eventing.DeliveryReceipt{}, ErrContract
		}
	} else {
		if ack.FromOffset != s.cursor+1 {
			return eventing.DeliveryReceipt{}, ErrContract
		}
		items := make([]eventing.Item, 0, ack.ToOffset-ack.FromOffset+1)
		for _, item := range s.items {
			if item.Offset >= ack.FromOffset && item.Offset <= ack.ToOffset {
				items = append(items, item)
			}
		}
		canonical, err := testCanonicalJSON(items)
		if err != nil || int64(len(items)) != ack.ToOffset-ack.FromOffset+1 || testContentHash(canonical) != ack.BatchHash {
			return eventing.DeliveryReceipt{}, ErrContract
		}
		s.deliveries[ack.FromOffset] = ack
		s.cursor = ack.ToOffset
	}
	return eventing.DeliveryReceipt{Consumer: consumer, Producer: ack.Producer,
		AcknowledgedOffset: ack.ToOffset, TechnicalStatus: "delivered"}, nil
}

func (s *windowSource) append(items []eventing.Item, receipts map[string]eventing.Receipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, items...)
	for id, receipt := range receipts {
		s.receipts[id] = receipt
	}
}

func newCommentEvents(t *testing.T) ([]eventing.Item, map[string]eventing.Receipt,
	map[string]communityauthority.Fact) {
	t.Helper()
	items := []eventing.Item{}
	receipts := map[string]eventing.Receipt{}
	facts := map[string]communityauthority.Fact{}
	for index, id := range []string{"12", "13"} {
		eventID := "rtw.comment." + id + ".created"
		item, receipt, fact, _ := sourceFixture(t, CommentProducer, "community.comment.created", eventID,
			id, eventID, "100"+id, int64(index+5), map[string]any{"aggregate_id": id,
				"operation": "create", "source_ref": "rtw.comment/" + id, "comment_id": id}, "")
		// A new comment aggregate starts at source version 1 while its DC
		// producer position can be 5/6. The two positions are unrelated.
		item.Event.AggregateVersion = 1
		canonical, err := testCanonicalJSON(item.Event)
		if err != nil {
			t.Fatal(err)
		}
		item.InputHash = testContentHash(canonical)
		receipt.InputHash = item.InputHash
		fact.Event, fact.SourceEventHash, fact.TechnicalReceipt = item.Event, item.InputHash, receipt
		items = append(items, item)
		receipts[eventID], facts[eventID] = receipt, fact
	}
	return items, receipts, facts
}

func windowConsumer(db *pgxpool.Pool, source *windowSource, facts map[string]communityauthority.Fact, limit int) *Consumer {
	return &Consumer{DB: db, Source: source, Authority: fakeAuthority{facts: facts}, Stream: CommentStream(),
		Logger: slog.New(slog.NewJSONHandler(ioDiscard{}, nil)), Limit: limit}
}

func countWindowRows(t *testing.T, db *pgxpool.Pool) (int, int) {
	t.Helper()
	var ods, windows int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM warehouse_community.ods_event`).Scan(&ods); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM warehouse_community.read_batch_evidence`).Scan(&windows); err != nil {
		t.Fatal(err)
	}
	return ods, windows
}

func TestLostACKAllowsLongerReadAfterNewEvents(t *testing.T) {
	db := pgPool(t)
	batch, receipts, facts := commentBatch(t)
	source := newWindowSource(CommentStream(), batch, receipts)
	source.failACK = true
	first, err := windowConsumer(db, source, facts, 128).RunOnce(context.Background())
	if err == nil || first.CommittedOffset != 4 || first.NewRows != 4 || first.AcknowledgedOffset != 0 {
		t.Fatalf("initial lost ACK: %+v %v", first, err)
	}
	newItems, newReceipts, newFacts := newCommentEvents(t)
	source.append(newItems, newReceipts)
	for id, fact := range newFacts {
		facts[id] = fact
	}
	second, err := windowConsumer(db, source, facts, 128).RunOnce(context.Background())
	if err != nil || second.CommittedOffset != 6 || second.ReplayedRows != 4 || second.NewRows != 2 || second.AcknowledgedOffset != 6 {
		t.Fatalf("longer recovery window: %+v %v", second, err)
	}
	if ods, windows := countWindowRows(t, db); ods != 6 || windows != 2 {
		t.Fatalf("longer recovery duplicated or lost evidence: ods=%d windows=%d", ods, windows)
	}
	assertWindowChain(t, db, 6, []CoverageBatch{{FromOffset: "1", ToOffset: "6"}})
}

func TestLostACKAllowsShorterLimitAndLaterWindow(t *testing.T) {
	db := pgPool(t)
	batch, receipts, facts := commentBatch(t)
	source := newWindowSource(CommentStream(), batch, receipts)
	source.failACK = true
	first, err := windowConsumer(db, source, facts, 4).RunOnce(context.Background())
	if err == nil || first.CommittedOffset != 4 {
		t.Fatalf("initial four-row commit: %+v %v", first, err)
	}
	second, err := windowConsumer(db, source, facts, 2).RunOnce(context.Background())
	if err != nil || second.ReplayedRows != 2 || second.NewRows != 0 || second.AcknowledgedOffset != 2 {
		t.Fatalf("shorter recovery window: %+v %v", second, err)
	}
	third, err := windowConsumer(db, source, facts, 2).RunOnce(context.Background())
	if err != nil || third.ReplayedRows != 2 || third.NewRows != 0 || third.AcknowledgedOffset != 4 {
		t.Fatalf("later replay window: %+v %v", third, err)
	}
	if ods, windows := countWindowRows(t, db); ods != 4 || windows != 3 {
		t.Fatalf("shorter recovery duplicated or lost evidence: ods=%d windows=%d", ods, windows)
	}
	assertWindowChain(t, db, 4, []CoverageBatch{{FromOffset: "1", ToOffset: "4"}})
	assertWindowChain(t, db, 2, []CoverageBatch{{FromOffset: "1", ToOffset: "2"}})
}

func TestConcurrentRestartWindowsDoNotDuplicateODS(t *testing.T) {
	db := pgPool(t)
	batch, receipts, facts := commentBatch(t)
	source := newWindowSource(CommentStream(), batch, receipts)
	source.readBarrier = make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan Result, 2)
	errorsCh := make(chan error, 2)
	for _, limit := range []int{4, 2} {
		wg.Add(1)
		go func(limit int) {
			defer wg.Done()
			result, err := windowConsumer(db, source, facts, limit).RunOnce(context.Background())
			results <- result
			errorsCh <- err
		}(limit)
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	var successful int
	for err := range errorsCh {
		if err == nil {
			successful++
		}
	}
	if successful != 1 {
		t.Fatalf("concurrent DC windows should yield one ACK before retry, got %d", successful)
	}
	for result := range results {
		if result.CommittedOffset < 2 || result.CommittedOffset > 4 {
			t.Fatalf("concurrent PG cursor outside prefix: %+v", result)
		}
	}
	if ods, windows := countWindowRows(t, db); ods != 4 || windows != 2 {
		t.Fatalf("concurrent windows duplicated or lost source: ods=%d windows=%d", ods, windows)
	}
	source.mu.Lock()
	source.readBarrier = nil
	source.mu.Unlock()
	if source.cursor < 4 {
		if final, err := windowConsumer(db, source, facts, 2).RunOnce(context.Background()); err != nil || final.AcknowledgedOffset != 4 {
			t.Fatalf("concurrent restart did not drain remaining window: %+v %v", final, err)
		}
	}
	if ods, _ := countWindowRows(t, db); ods != 4 {
		t.Fatalf("final ODS duplicated: %d", ods)
	}
	assertWindowChain(t, db, 4, []CoverageBatch{{FromOffset: "1", ToOffset: "4"}})
}

func assertWindowChain(t *testing.T, db *pgxpool.Pool, through int64, expected []CoverageBatch) {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(ioDiscard{}, nil))
	publisher := &CoveragePublisher{DB: db, Source: &fakeSource{}, Authority: fakeAuthority{},
		Stream: CommentStream(), S3Prefix: "http://127.0.0.1:1", Logger: logger}
	rows, batches, err := publisher.readFrozen(context.Background(), through)
	if err != nil || len(rows) != int(through) || len(batches) != len(expected) {
		t.Fatalf("read full prefix %d: rows=%d batches=%v err=%v", through, len(rows), batches, err)
	}
	for index, wanted := range expected {
		if batches[index].FromOffset != wanted.FromOffset || batches[index].ToOffset != wanted.ToOffset {
			t.Fatalf("selected batch chain %d: %+v wanted %+v", index, batches[index], wanted)
		}
	}
	if _, _, err := coverageBatchJSONL(rows, batches, through); err != nil {
		t.Fatalf("selected chain hash proof: %v", err)
	}
}

func TestBatchWindowMigrationPreservesLegacyEvidence(t *testing.T) {
	db := pgPool(t)
	if _, err := db.Exec(context.Background(), `ALTER TABLE warehouse_community.read_batch_evidence
		DROP CONSTRAINT read_batch_evidence_pkey`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), `ALTER TABLE warehouse_community.read_batch_evidence
		ADD CONSTRAINT read_batch_evidence_pkey PRIMARY KEY (consumer,producer,from_offset)`); err != nil {
		t.Fatal(err)
	}
	oldHash := testContentHash([]byte("legacy-window"))
	if _, err := db.Exec(context.Background(), `INSERT INTO warehouse_community.read_batch_evidence
		(consumer,producer,from_offset,to_offset,batch_hash) VALUES($1,$2,1,4,$3)`, CommentConsumer,
		CommentProducer, oldHash); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(context.Background(), db); err != nil {
		t.Fatalf("migrate legacy batch-window key: %v", err)
	}
	newHash := testContentHash([]byte("shorter-window"))
	if _, err := db.Exec(context.Background(), `INSERT INTO warehouse_community.read_batch_evidence
		(consumer,producer,from_offset,to_offset,batch_hash) VALUES($1,$2,1,2,$3)`, CommentConsumer,
		CommentProducer, newHash); err != nil {
		t.Fatalf("migrated key still blocked overlapping window: %v", err)
	}
	if _, windows := countWindowRows(t, db); windows != 2 {
		t.Fatalf("migration lost old window: %d", windows)
	}
}

func TestSelectBatchChainAuditsOverlapsAndFindsCompletePath(t *testing.T) {
	rows := make([]frozenCoverageRow, 0, 4)
	for offset := int64(1); offset <= 4; offset++ {
		event := eventing.Event{EventID: "rtw.comment." + strconv.FormatInt(offset, 10) + ".created",
			EventType: "community.comment.created", SchemaVersion: 1, Producer: CommentProducer,
			AggregateID: strconv.FormatInt(offset, 10), AggregateVersion: 1,
			OperationID: strconv.FormatInt(offset, 10), OccurredAt: sourceTime,
			Payload: json.RawMessage(`{"fixture":true}`)}
		spec, _ := testCanonicalJSON(event)
		hash := testContentHash(spec)
		receipt := eventing.Receipt{EventID: event.EventID, Producer: CommentProducer, TechnicalStatus: "accepted",
			ReceiptID: "receipt-" + strconv.FormatInt(offset, 10), InputHash: hash, Offset: offset, ReceivedAt: sourceTime}
		rows = append(rows, frozenCoverageRow{Offset: offset, Event: event, Hash: hash, Receipt: receipt,
			Subject: CoverageSubject{Issuer: "rtw.identity", SubjectID: "1001"}})
	}
	window := func(from, to int64) CoverageBatch {
		items := make([]eventing.Item, 0, to-from+1)
		for _, row := range rows[from-1 : to] {
			items = append(items, eventing.Item{Offset: row.Offset, InputHash: row.Hash, Event: row.Event})
		}
		body, _ := testCanonicalJSON(items)
		return CoverageBatch{FromOffset: strconv.FormatInt(from, 10), ToOffset: strconv.FormatInt(to, 10),
			BatchHash: testContentHash(body)}
	}
	observed := []CoverageBatch{window(1, 3), window(1, 2), window(3, 4)}
	selected, err := selectBatchChain(rows, observed, 4)
	if err != nil || len(selected) != 2 || selected[0].ToOffset != "2" || selected[1].FromOffset != "3" {
		t.Fatalf("dead-end longest window hid valid path: %+v %v", selected, err)
	}
	observed = append(observed, window(1, 4))
	selected, err = selectBatchChain(rows, observed, 4)
	if err != nil || len(selected) != 1 || selected[0].ToOffset != "4" {
		t.Fatalf("complete single-window proof not preferred: %+v %v", selected, err)
	}
	observed[0].BatchHash = testContentHash([]byte("tampered-batch"))
	if _, err := selectBatchChain(rows, observed, 4); !errors.Is(err, ErrCoverageConflict) {
		t.Fatalf("tampered overlapping window escaped audit: %v", err)
	}
}
