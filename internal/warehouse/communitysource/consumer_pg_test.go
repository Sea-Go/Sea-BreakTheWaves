package communitysource

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind/communityauthority"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeSource struct {
	batch    eventing.Batch
	receipts map[string]eventing.Receipt
	failACK  bool
	acked    int64
}

func (s *fakeSource) ReadEvents(_ context.Context, consumer, producer string, _ int) (eventing.Batch, error) {
	if s.acked >= s.batch.ToOffset {
		return eventing.Batch{Consumer: consumer, Producer: producer, FromOffset: s.acked + 1, ToOffset: s.acked}, nil
	}
	return s.batch, nil
}
func (s *fakeSource) EventReceipt(_ context.Context, _, eventID string) (eventing.Receipt, error) {
	receipt, ok := s.receipts[eventID]
	if !ok {
		return eventing.Receipt{}, errors.New("receipt unavailable")
	}
	return receipt, nil
}
func (s *fakeSource) AcknowledgeEvents(_ context.Context, consumer string, ack eventing.Acknowledge) (eventing.DeliveryReceipt, error) {
	if s.failACK {
		s.failACK = false
		return eventing.DeliveryReceipt{}, errors.New("lost ACK response")
	}
	s.acked = ack.ToOffset
	return eventing.DeliveryReceipt{Consumer: consumer, Producer: ack.Producer,
		AcknowledgedOffset: ack.ToOffset, TechnicalStatus: "delivered"}, nil
}

type fakeAuthority struct {
	facts map[string]communityauthority.Fact
	err   error
}

func (a fakeAuthority) Lookup(_ context.Context, evidence communityauthority.Evidence) (communityauthority.Fact, error) {
	if a.err != nil {
		return communityauthority.Fact{}, a.err
	}
	fact, ok := a.facts[evidence.Event.EventID]
	if !ok {
		return communityauthority.Fact{}, errors.New("authority unavailable")
	}
	if err := communityauthority.Verify(evidence, fact); err != nil {
		return communityauthority.Fact{}, err
	}
	return fact, nil
}

func pgPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("COMMUNITY_WAREHOUSE_TEST_DSN")
	if dsn == "" {
		t.Skip("set COMMUNITY_WAREHOUSE_TEST_DSN for isolated PostgreSQL acceptance")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := Initialize(context.Background(), pool); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `TRUNCATE warehouse_community.ods_event,
		warehouse_community.read_batch_evidence,warehouse_community.consumer_cursor`); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func commentBatch(t *testing.T) (eventing.Batch, map[string]eventing.Receipt, map[string]communityauthority.Fact) {
	t.Helper()
	createdID := "rtw.comment.11.created"
	likeID := "rtw.comment.interaction.11111111-1111-4111-8111-111111111111"
	unlikeID := "rtw.comment.interaction.22222222-2222-4222-8222-222222222222"
	deletedID := "rtw.comment.11.deleted"
	definitions := []struct {
		eventType, eventID, operationID, subjectID, operation, sourceRef, predecessor string
		oldState, newState                                                            *int16
	}{
		{"community.comment.created", createdID, createdID, "1001", "create", "rtw.comment/11", "", nil, nil},
		{"community.comment.interaction", likeID, likeID, "1002", "like", likeID, "", int16Ptr(0), int16Ptr(1)},
		{"community.comment.interaction", unlikeID, unlikeID, "1002", "unlike", unlikeID, likeID, int16Ptr(1), int16Ptr(0)},
		{"community.comment.deleted", deletedID, deletedID, "1001", "retract", "rtw.comment/11", createdID, nil, nil},
	}
	items := make([]eventing.Item, 0, len(definitions))
	receipts := map[string]eventing.Receipt{}
	facts := map[string]communityauthority.Fact{}
	for index, definition := range definitions {
		fields := map[string]any{"aggregate_id": "11", "operation": definition.operation,
			"source_ref": definition.sourceRef, "comment_id": "11"}
		if definition.oldState != nil {
			fields["old_state"], fields["new_state"] = *definition.oldState, *definition.newState
		}
		item, receipt, fact, _ := sourceFixture(t, CommentProducer, definition.eventType, definition.eventID,
			"11", definition.operationID, definition.subjectID, int64(index+1), fields, definition.predecessor)
		items = append(items, item)
		receipts[item.Event.EventID], facts[item.Event.EventID] = receipt, fact
	}
	return makeBatch(t, CommentStream(), items), receipts, facts
}

func likeBatch(t *testing.T) (eventing.Batch, map[string]eventing.Receipt, map[string]communityauthority.Fact) {
	t.Helper()
	likeID, unlikeID := "rtw.like.21", "rtw.like.22"
	items := make([]eventing.Item, 0, 2)
	receipts := map[string]eventing.Receipt{}
	facts := map[string]communityauthority.Fact{}
	for index, definition := range []struct {
		id, operation, predecessor string
		oldState, newState         int16
	}{{likeID, "like", "", 0, 1}, {unlikeID, "unlike", likeID, 1, 0}} {
		item, receipt, fact, _ := sourceFixture(t, LikeProducer, "community.target.interaction", definition.id,
			"like-state/1003/article/article-1", definition.id[len("rtw.like."):], "1003", int64(index+1),
			map[string]any{"aggregate_id": "article/article-1", "operation": definition.operation,
				"source_ref": definition.id, "old_state": definition.oldState, "new_state": definition.newState}, definition.predecessor)
		items = append(items, item)
		receipts[item.Event.EventID], facts[item.Event.EventID] = receipt, fact
	}
	return makeBatch(t, LikeStream(), items), receipts, facts
}

func makeBatch(t *testing.T, stream Stream, items []eventing.Item) eventing.Batch {
	t.Helper()
	canonical, err := testCanonicalJSON(items)
	if err != nil {
		t.Fatal(err)
	}
	return eventing.Batch{Consumer: stream.Consumer, Producer: stream.Producer,
		FromOffset: items[0].Offset, ToOffset: items[len(items)-1].Offset, BatchHash: testContentHash(canonical), Events: items}
}

func int16Ptr(value int16) *int16 { return &value }

func TestConsumerCommitsBeforeACKAndReplaysWithoutDuplicates(t *testing.T) {
	db := pgPool(t)
	batch, receipts, facts := commentBatch(t)
	source := &fakeSource{batch: batch, receipts: receipts, failACK: true}
	var logs bytes.Buffer
	consumer := &Consumer{DB: db, Source: source, Authority: fakeAuthority{facts: facts}, Stream: CommentStream(),
		Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	first, err := consumer.RunOnce(context.Background())
	if err == nil || first.NewRows != 4 || first.CommittedOffset != 4 || first.AcknowledgedOffset != 0 {
		t.Fatalf("lost ACK boundary: %+v %v", first, err)
	}
	second, err := consumer.RunOnce(context.Background())
	if err != nil || second.ReplayedRows != 4 || second.NewRows != 0 || second.AcknowledgedOffset != 4 {
		t.Fatalf("restart replay: %+v %v", second, err)
	}
	var rows, batches int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM warehouse_community.ods_event`).Scan(&rows); err != nil || rows != 4 {
		t.Fatalf("ODS rows=%d err=%v", rows, err)
	}
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM warehouse_community.read_batch_evidence`).Scan(&batches); err != nil || batches != 1 {
		t.Fatalf("batch evidence=%d err=%v", batches, err)
	}
	if !bytes.Contains(logs.Bytes(), []byte(`"event":"warehouse.community.batch.finished"`)) ||
		!bytes.Contains(logs.Bytes(), []byte(`"duration_ms":`)) {
		t.Fatalf("structured log missing: %s", logs.String())
	}
	var forbiddenColumns int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='warehouse_community' AND column_name IN ('tenant_id','realm')`).Scan(&forbiddenColumns); err != nil || forbiddenColumns != 0 {
		t.Fatalf("new warehouse schema exposed forbidden identity columns: %d %v", forbiddenColumns, err)
	}
	if _, err := db.Exec(context.Background(), `UPDATE warehouse_community.ods_event SET issuer='other' WHERE producer=$1 AND source_offset=1`, CommentProducer); err == nil {
		t.Fatal("non-RTW issuer passed PG constraint")
	}
	if _, err := db.Exec(context.Background(), `UPDATE warehouse_community.ods_event SET subject_id='01' WHERE producer=$1 AND source_offset=1`, CommentProducer); err == nil {
		t.Fatal("noncanonical subject ID passed PG constraint")
	}
	if _, err := db.Exec(context.Background(), `DELETE FROM warehouse_community.ods_event WHERE producer=$1 AND source_offset=1`, CommentProducer); err == nil {
		t.Fatal("immutable ODS row was deleted")
	}
	if _, err := db.Exec(context.Background(), `UPDATE warehouse_community.read_batch_evidence SET batch_hash=$1 WHERE producer=$2`,
		testContentHash([]byte("changed")), CommentProducer); err == nil {
		t.Fatal("immutable batch evidence was changed")
	}
}

func TestConsumersKeepProducerOffsetsSeparate(t *testing.T) {
	db := pgPool(t)
	comment, commentReceipts, commentFacts := commentBatch(t)
	like, likeReceipts, likeFacts := likeBatch(t)
	logger := slog.New(slog.NewJSONHandler(ioDiscard{}, nil))
	for _, fixture := range []struct {
		stream Stream
		batch  eventing.Batch
		r      map[string]eventing.Receipt
		f      map[string]communityauthority.Fact
	}{{CommentStream(), comment, commentReceipts, commentFacts}, {LikeStream(), like, likeReceipts, likeFacts}} {
		source := &fakeSource{batch: fixture.batch, receipts: fixture.r}
		consumer := &Consumer{DB: db, Source: source, Authority: fakeAuthority{facts: fixture.f}, Stream: fixture.stream, Logger: logger}
		if result, err := consumer.RunOnce(context.Background()); err != nil || result.AcknowledgedOffset != fixture.batch.ToOffset {
			t.Fatalf("producer %s: %+v %v", fixture.stream.Producer, result, err)
		}
	}
	rows, err := db.Query(context.Background(), `SELECT producer,committed_offset FROM warehouse_community.consumer_cursor ORDER BY producer`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var producer string
		var offset int64
		if err := rows.Scan(&producer, &offset); err != nil {
			t.Fatal(err)
		}
		got[producer] = offset
	}
	if got[CommentProducer] != 4 || got[LikeProducer] != 2 {
		t.Fatalf("producer cursors were mixed: %v", got)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(body []byte) (int, error) { return len(body), nil }

func TestConsumerRejectsAuthorityHashPredecessorAndOffsetFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(eventing.Batch, map[string]eventing.Receipt, map[string]communityauthority.Fact) (eventing.Batch, map[string]eventing.Receipt, map[string]communityauthority.Fact, error)
	}{
		{"authority", func(b eventing.Batch, r map[string]eventing.Receipt, f map[string]communityauthority.Fact) (eventing.Batch, map[string]eventing.Receipt, map[string]communityauthority.Fact, error) {
			return b, r, f, errors.New("wrong authority")
		}},
		{"hash", func(b eventing.Batch, r map[string]eventing.Receipt, f map[string]communityauthority.Fact) (eventing.Batch, map[string]eventing.Receipt, map[string]communityauthority.Fact, error) {
			b.Events[0].InputHash = testContentHash([]byte("wrong"))
			return b, r, f, nil
		}},
		{"predecessor", func(b eventing.Batch, r map[string]eventing.Receipt, f map[string]communityauthority.Fact) (eventing.Batch, map[string]eventing.Receipt, map[string]communityauthority.Fact, error) {
			last := b.Events[2].Event.EventID
			changed := f[last]
			changed.PredecessorEventID = "rtw.comment.interaction.missing"
			f[last] = changed
			return b, r, f, nil
		}},
		{"offset", func(b eventing.Batch, r map[string]eventing.Receipt, f map[string]communityauthority.Fact) (eventing.Batch, map[string]eventing.Receipt, map[string]communityauthority.Fact, error) {
			b.Events, b.FromOffset = b.Events[1:], 2
			canonical, _ := testCanonicalJSON(b.Events)
			b.BatchHash = testContentHash(canonical)
			return b, r, f, nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := pgPool(t)
			batch, receipts, facts := commentBatch(t)
			batch, receipts, facts, authorityErr := test.mutate(batch, receipts, facts)
			authority := fakeAuthority{facts: facts, err: authorityErr}
			consumer := &Consumer{DB: db, Source: &fakeSource{batch: batch, receipts: receipts}, Authority: authority,
				Stream: CommentStream(), Logger: slog.New(slog.NewJSONHandler(ioDiscard{}, nil))}
			if _, err := consumer.RunOnce(context.Background()); err == nil {
				t.Fatal("invalid source admitted")
			}
			var count int
			if err := db.QueryRow(context.Background(), `SELECT count(*) FROM warehouse_community.ods_event`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rejected batch changed ODS: count=%d err=%v", count, err)
			}
		})
	}
}
