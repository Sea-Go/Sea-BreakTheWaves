package communitysource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind/communityauthority"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

const sourceTime = "2026-09-15T00:00:00Z"

func sourceFixture(t *testing.T, producer, eventType, eventID, aggregateID, operationID, subjectID string,
	version int64, fields map[string]any, predecessor string) (eventing.Item, eventing.Receipt, communityauthority.Fact, sourcePayload) {
	t.Helper()
	payload := sourcePayload{SchemaVersion: "rtw.community-fact.v1", EventID: eventID, EventType: eventType,
		Producer: producer, AggregateID: fields["aggregate_id"].(string), OperationID: operationID,
		SubjectRef: "rtw.identity/platform/" + subjectID, TargetType: "article", TargetID: "article-1",
		RevisionStatus: "unknown", Operation: fields["operation"].(string), SourceRef: fields["source_ref"].(string),
		EventTime: sourceTime, OccurredAt: sourceTime, AvailableAt: sourceTime}
	if value, ok := fields["comment_id"].(string); ok {
		payload.CommentID = value
		visibility, search := int16(0), false
		payload.VisibilityState, payload.SearchEvidence = &visibility, &search
	}
	if value, ok := fields["old_state"].(int16); ok {
		payload.OldState = &value
		newValue := fields["new_state"].(int16)
		payload.NewState = &newValue
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	event := eventing.Event{EventID: eventID, EventType: eventType, SchemaVersion: 1, Producer: producer,
		AggregateID: aggregateID, AggregateVersion: version, OperationID: operationID, OccurredAt: sourceTime, Payload: raw}
	eventRaw, _ := json.Marshal(event)
	canonical, _ := jsoncanonicalizer.Transform(eventRaw)
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	receipt := eventing.Receipt{EventID: eventID, Producer: producer, TechnicalStatus: "accepted", ReceiptID: "receipt-" + operationID,
		InputHash: hash, Offset: version, ReceivedAt: sourceTime}
	fact := communityauthority.Fact{Event: event, SubjectRef: communityauthority.SubjectRef{Issuer: "rtw.identity", SubjectID: subjectID},
		PredecessorEventID: predecessor, TechnicalReceipt: receipt, SourceEventHash: hash}
	return eventing.Item{Offset: version, InputHash: hash, Event: event}, receipt, fact, payload
}

func TestSourceRowAdmitsOnlyPreciseCommunityTransitions(t *testing.T) {
	created, createdReceipt, createdFact, createdPayload := sourceFixture(t, CommentProducer, "community.comment.created",
		"rtw.comment.11.created", "11", "rtw.comment.11.created", "1001", 1,
		map[string]any{"aggregate_id": "11", "operation": "create", "source_ref": "rtw.comment/11", "comment_id": "11"}, "")
	if row, err := sourceRow(created, createdReceipt, createdFact, createdPayload); err != nil || row.Issuer != "rtw.identity" ||
		row.SubjectID != "1001" || row.TargetRevision != nil || row.RevisionStatus != "unknown" || row.SearchEvidence == nil || *row.SearchEvidence {
		t.Fatalf("comment create: %+v %v", row, err)
	}
	likedID := "rtw.comment.interaction.11111111-1111-4111-8111-111111111111"
	liked, likedReceipt, likedFact, likedPayload := sourceFixture(t, CommentProducer, "community.comment.interaction",
		likedID, "11", likedID, "1002", 2,
		map[string]any{"aggregate_id": "11", "operation": "like", "source_ref": likedID, "comment_id": "11", "old_state": int16(0), "new_state": int16(1)}, "")
	if _, err := sourceRow(liked, likedReceipt, likedFact, likedPayload); err != nil {
		t.Fatal(err)
	}
	unlikedID := "rtw.comment.interaction.22222222-2222-4222-8222-222222222222"
	unliked, unlikeReceipt, unlikeFact, unlikePayload := sourceFixture(t, CommentProducer, "community.comment.interaction",
		unlikedID, "11", unlikedID, "1002", 3,
		map[string]any{"aggregate_id": "11", "operation": "unlike", "source_ref": unlikedID, "comment_id": "11", "old_state": int16(1), "new_state": int16(0)}, likedID)
	if _, err := sourceRow(unliked, unlikeReceipt, unlikeFact, unlikePayload); err != nil {
		t.Fatal(err)
	}
	bad := unlikePayload
	bad.TargetRevision = ptr("article-1:r1")
	bad.RevisionStatus = "resolved"
	if _, err := sourceRow(unliked, unlikeReceipt, unlikeFact, bad); err == nil {
		t.Fatal("guessed target revision accepted")
	}
	wrongPredecessor := unlikeFact
	wrongPredecessor.PredecessorEventID = "rtw.comment.interaction.other"
	row, err := sourceRow(unliked, unlikeReceipt, wrongPredecessor, unlikePayload)
	if err != nil || row.Predecessor == nil || *row.Predecessor != wrongPredecessor.PredecessorEventID {
		t.Fatalf("neutral source parse changed predecessor before PG validation: %+v %v", row, err)
	}
}

func TestLikeSourceHasIndependentProducerSequence(t *testing.T) {
	liked, receipt, fact, payload := sourceFixture(t, LikeProducer, "community.target.interaction", "rtw.like.21",
		"like-state/1003/article/article-1", "21", "1003", 1,
		map[string]any{"aggregate_id": "article/article-1", "operation": "like", "source_ref": "rtw.like.21", "old_state": int16(0), "new_state": int16(1)}, "")
	row, err := sourceRow(liked, receipt, fact, payload)
	if err != nil || row.Item.Offset != 1 || row.Item.Event.Producer != LikeProducer || row.CommentID != nil || row.SearchEvidence != nil {
		t.Fatalf("target like: %+v %v", row, err)
	}
}

func TestSourceRequiresExplicitNullRevisionAndBusinessVersion(t *testing.T) {
	item, _, _, _ := sourceFixture(t, LikeProducer, "community.target.interaction", "rtw.like.30",
		"like-state/1003/article/article-1", "30", "1003", 1,
		map[string]any{"aggregate_id": "article/article-1", "operation": "like", "source_ref": "rtw.like.30",
			"old_state": int16(0), "new_state": int16(1)}, "")
	if !explicitNullSourceFields(item.Event.Payload) {
		t.Fatal("valid explicit null fields rejected")
	}
	var fields map[string]any
	if err := json.Unmarshal(item.Event.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"target_revision", "aggregate_version"} {
		changed := make(map[string]any, len(fields))
		for name, value := range fields {
			changed[name] = value
		}
		delete(changed, key)
		raw, _ := json.Marshal(changed)
		if explicitNullSourceFields(raw) {
			t.Fatalf("missing %s accepted as explicit null", key)
		}
	}
}

func ptr(value string) *string { return &value }

func TestValidateBatchRejectsMissingAndMixedOffsets(t *testing.T) {
	item, _, _, _ := sourceFixture(t, LikeProducer, "community.target.interaction", "rtw.like.31",
		"like-state/1003/article/article-1", "31", "1003", 1,
		map[string]any{"aggregate_id": "article/article-1", "operation": "like", "source_ref": "rtw.like.31", "old_state": int16(0), "new_state": int16(1)}, "")
	items := []eventing.Item{item}
	canonical, _ := jsoncanonicalizer.Transform(mustMarshal(t, items))
	batch := eventing.Batch{Consumer: LikeConsumer, Producer: LikeProducer, FromOffset: 1, ToOffset: 1,
		BatchHash: testContentHash(canonical), Events: items}
	if err := validateBatch(batch, LikeStream(), 128); err != nil {
		t.Fatal(err)
	}
	bad := batch
	bad.Events[0].Offset = 2
	if err := validateBatch(bad, LikeStream(), 128); err == nil {
		t.Fatal("missing producer offset accepted")
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testCanonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jsoncanonicalizer.Transform(raw)
}

func testContentHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
