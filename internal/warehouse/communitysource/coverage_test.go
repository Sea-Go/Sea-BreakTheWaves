package communitysource

import (
	"encoding/json"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
)

func TestCoverageKeepsProducerPrefixesAndSubjectSlicesSeparate(t *testing.T) {
	rows := make([]frozenCoverageRow, 0, 2)
	for index, subject := range []string{"1001", "1002"} {
		event := eventing.Event{EventID: "rtw.like." + subject, EventType: "community.target.interaction", SchemaVersion: 1,
			Producer: LikeProducer, AggregateID: "like-state/" + subject + "/article/a", AggregateVersion: 1,
			OperationID: subject, OccurredAt: sourceTime, Payload: json.RawMessage(`{"fixture":true}`)}
		spec, _ := canonicalJSON(event)
		hash := contentHash(spec)
		receipt := eventing.Receipt{EventID: event.EventID, Producer: LikeProducer, TechnicalStatus: "accepted",
			ReceiptID: "receipt-" + subject, InputHash: hash, Offset: int64(index + 1), ReceivedAt: sourceTime}
		rows = append(rows, frozenCoverageRow{Offset: int64(index + 1), Event: event, Hash: hash, Receipt: receipt,
			Subject: CoverageSubject{Issuer: "rtw.identity", SubjectID: subject}})
	}
	items := []eventing.Item{{Offset: 1, InputHash: rows[0].Hash, Event: rows[0].Event},
		{Offset: 2, InputHash: rows[1].Hash, Event: rows[1].Event}}
	batchBody, _ := canonicalJSON(items)
	batches := []CoverageBatch{{FromOffset: "1", ToOffset: "2", BatchHash: contentHash(batchBody)}}
	index, indexHash, err := coverageEventJSONL(LikeProducer, 2, rows)
	if err != nil || len(index) == 0 || !digest.MatchString(indexHash) {
		t.Fatalf("complete prefix: hash=%s err=%v", indexHash, err)
	}
	if _, _, err := coverageBatchJSONL(rows, batches, 2); err != nil {
		t.Fatal(err)
	}
	for _, subject := range []CoverageSubject{{Issuer: "rtw.identity", SubjectID: "1001"},
		{Issuer: "rtw.identity", SubjectID: "1002"}} {
		body, count, err := coverageSubjectJSONL(subject, rows)
		if err != nil || count != 1 || len(body) == 0 {
			t.Fatalf("subject slice %+v: count=%d err=%v", subject, count, err)
		}
	}
	mixed := append([]frozenCoverageRow(nil), rows...)
	mixed[1].Event.Producer = CommentProducer
	if _, _, err := coverageEventJSONL(LikeProducer, 2, mixed); err == nil {
		t.Fatal("two producer offsets merged into one prefix")
	}
	if _, _, err := coverageEventJSONL(LikeProducer, 3, rows); err == nil {
		t.Fatal("incomplete prefix frozen")
	}
}
