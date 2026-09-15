package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/usermodel"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

const communityTestTime = "2026-09-15T08:00:00.123456Z"

func communityEvidence(t *testing.T, producer, eventType, eventID, aggregateID, operationID string,
	version, offset int64, subject string, fields map[string]any, predecessor string) (FactSourceEvidence, communityAuthorityResponse) {
	t.Helper()
	payload := map[string]any{
		"schema_version": "rtw.community-fact.v1", "event_id": eventID, "event_type": eventType,
		"producer": producer, "aggregate_id": fields["aggregate_id"], "aggregate_version": nil,
		"operation_id": operationID, "subject_ref": "rtw.identity/platform/" + subject,
		"target_type": "article", "target_id": "article-community", "target_revision": nil,
		"revision_status": "unknown", "operation": fields["operation"], "source_ref": fields["source_ref"],
		"event_time": communityTestTime, "occurred_at": communityTestTime,
		"available_at": "2026-09-15T08:00:01.123456Z",
	}
	for key, value := range fields {
		if key != "aggregate_id" && key != "operation" && key != "source_ref" {
			payload[key] = value
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	event := eventing.Event{EventID: eventID, EventType: eventType, SchemaVersion: 1, Producer: producer,
		AggregateID: aggregateID, AggregateVersion: version, OperationID: operationID,
		OccurredAt: communityTestTime, Payload: raw}
	eventRaw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jsoncanonicalizer.Transform(eventRaw)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	receipt := eventing.Receipt{EventID: eventID, Producer: producer, TechnicalStatus: "accepted",
		ReceiptID: "receipt-" + eventID, InputHash: hash, Offset: offset, ReceivedAt: "2026-09-15T08:00:02.123456Z"}
	source := FactSourceEvidence{Event: event, InputHash: hash, Offset: offset, Receipt: receipt}
	authority := communityAuthorityResponse{Event: event,
		SubjectRef:         communitySourceSubjectRef{Issuer: "rtw-user-center", Realm: "platform", SubjectID: subject},
		PredecessorEventID: predecessor, TechnicalReceipt: receipt, SourceEventHash: hash}
	return source, authority
}

func TestBindCommunityAuthorityMapsSourceTransitions(t *testing.T) {
	createdID := "rtw.comment/9101/created"
	commentLikeID := "rtw.comment.interaction/11111111-1111-4111-8111-111111111111"
	commentUnlikeID := "rtw.comment.interaction/22222222-2222-4222-8222-222222222222"
	commentFields := func(operation, source string, oldState, newState any) map[string]any {
		fields := map[string]any{"aggregate_id": "9101", "operation": operation, "source_ref": source,
			"comment_id": "9101", "visibility_state": 0, "search_evidence": false}
		if oldState != nil {
			fields["old_state"], fields["new_state"] = oldState, newState
		}
		return fields
	}
	likeFields := func(operation, source string, oldState, newState int32) map[string]any {
		return map[string]any{"aggregate_id": "article/article-community", "operation": operation,
			"source_ref": source, "old_state": oldState, "new_state": newState}
	}
	tests := []struct {
		name        string
		source      FactSourceEvidence
		authority   communityAuthorityResponse
		action      usermodel.Action
		predicate   string
		predecessor string
	}{
		func() (result struct {
			name        string
			source      FactSourceEvidence
			authority   communityAuthorityResponse
			action      usermodel.Action
			predicate   string
			predecessor string
		}) {
			result.name, result.action, result.predicate = "comment-create", usermodel.Assert, "comment"
			result.source, result.authority = communityEvidence(t, commentFactProducer, "community.comment.created", createdID,
				"9101", createdID, 1, 1, "1101", commentFields("create", "rtw.comment/9101", nil, nil), "")
			return
		}(),
		func() (result struct {
			name        string
			source      FactSourceEvidence
			authority   communityAuthorityResponse
			action      usermodel.Action
			predicate   string
			predecessor string
		}) {
			result.name, result.action, result.predicate = "comment-like", usermodel.Assert, "comment_like"
			result.source, result.authority = communityEvidence(t, commentFactProducer, "community.comment.interaction", commentLikeID,
				"9101", commentLikeID, 2, 2, "1301", commentFields("like", commentLikeID, int32(0), int32(1)), "")
			return
		}(),
		func() (result struct {
			name        string
			source      FactSourceEvidence
			authority   communityAuthorityResponse
			action      usermodel.Action
			predicate   string
			predecessor string
		}) {
			result.name, result.action, result.predicate, result.predecessor = "comment-unlike", usermodel.Retract, "comment_like", commentLikeID
			result.source, result.authority = communityEvidence(t, commentFactProducer, "community.comment.interaction", commentUnlikeID,
				"9101", commentUnlikeID, 3, 3, "1301", commentFields("unlike", commentUnlikeID, int32(1), int32(0)), commentLikeID)
			return
		}(),
		func() (result struct {
			name        string
			source      FactSourceEvidence
			authority   communityAuthorityResponse
			action      usermodel.Action
			predicate   string
			predecessor string
		}) {
			deletedID := "rtw.comment/9101/deleted"
			result.name, result.action, result.predicate, result.predecessor = "comment-delete", usermodel.Retract, "comment", createdID
			result.source, result.authority = communityEvidence(t, commentFactProducer, "community.comment.deleted", deletedID,
				"9101", deletedID, 4, 4, "1101", commentFields("retract", "rtw.comment/9101", nil, nil), createdID)
			return
		}(),
		func() (result struct {
			name        string
			source      FactSourceEvidence
			authority   communityAuthorityResponse
			action      usermodel.Action
			predicate   string
			predecessor string
		}) {
			id := "rtw.like/9201"
			result.name, result.action, result.predicate = "target-like", usermodel.Assert, "like"
			result.source, result.authority = communityEvidence(t, likeFactProducer, "community.target.interaction", id,
				"like-state/1401/article/article-community", "9201", 1, 1, "1401", likeFields("like", id, 0, 1), "")
			return
		}(),
		func() (result struct {
			name        string
			source      FactSourceEvidence
			authority   communityAuthorityResponse
			action      usermodel.Action
			predicate   string
			predecessor string
		}) {
			id, prior := "rtw.like/9202", "rtw.like/9201"
			result.name, result.action, result.predicate, result.predecessor = "target-unlike", usermodel.Retract, "like", prior
			result.source, result.authority = communityEvidence(t, likeFactProducer, "community.target.interaction", id,
				"like-state/1401/article/article-community", "9202", 2, 2, "1401", likeFields("unlike", id, 1, 0), prior)
			return
		}(),
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fact, err := bindCommunityAuthority(test.source, test.authority)
			if err != nil {
				t.Fatal(err)
			}
			expectedSubject := usermodel.SubjectRef{AuthorityID: "rtw-user-center", TenantID: "platform",
				SubjectID: test.authority.SubjectRef.SubjectID}
			if fact.Action != test.action || fact.Predicate != test.predicate || fact.Kind != usermodel.ProductAction ||
				fact.Subject != expectedSubject || fact.ImpressionID != "" || fact.VisibilityEvidenceRef != "" {
				t.Fatalf("semantic fact: %+v", fact)
			}
			if test.predecessor == "" && fact.Supersedes != nil || test.predecessor != "" &&
				(fact.Supersedes == nil || fact.Supersedes.EventID != test.predecessor || fact.Supersedes.Producer != test.source.Event.Producer) {
				t.Fatalf("semantic predecessor: %+v", fact.Supersedes)
			}
		})
	}
}

func TestBindCommunityAuthorityRejectsGuessesAndMismatches(t *testing.T) {
	id := "rtw.like/9301"
	fields := map[string]any{"aggregate_id": "article/article-community", "operation": "like", "source_ref": id,
		"old_state": int32(0), "new_state": int32(1)}
	source, authority := communityEvidence(t, likeFactProducer, "community.target.interaction", id,
		"like-state/1401/article/article-community", "9301", 1, 1, "1401", fields, "")
	tests := map[string]func(*FactSourceEvidence, *communityAuthorityResponse){
		"forged-subject": func(_ *FactSourceEvidence, a *communityAuthorityResponse) { a.SubjectRef.SubjectID = "9999" },
		"receipt-offset": func(_ *FactSourceEvidence, a *communityAuthorityResponse) { a.TechnicalReceipt.Offset++ },
		"event-body":     func(_ *FactSourceEvidence, a *communityAuthorityResponse) { a.Event.OperationID = "changed" },
		"guessed-revision": func(s *FactSourceEvidence, a *communityAuthorityResponse) {
			var payload map[string]any
			_ = json.Unmarshal(a.Event.Payload, &payload)
			payload["target_revision"], payload["revision_status"] = "article-community:r1", "resolved"
			a.Event.Payload, _ = json.Marshal(payload)
			s.Event = a.Event
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changedSource, changedAuthority := source, authority
			mutate(&changedSource, &changedAuthority)
			if _, err := bindCommunityAuthority(changedSource, changedAuthority); err == nil {
				t.Fatal("mismatched authority accepted")
			}
		})
	}
}

func TestDynamicFactBindingRequiresBoundedUniqueActions(t *testing.T) {
	if !validFactBindingActions("", []usermodel.Action{usermodel.Assert, usermodel.Correct, usermodel.Retract}) {
		t.Fatal("valid dynamic transition actions rejected")
	}
	for _, invalid := range [][]usermodel.Action{{usermodel.Assert}, {usermodel.Assert, usermodel.Assert}, {usermodel.Assert, "guess"}} {
		if validFactBindingActions("", invalid) {
			t.Fatalf("invalid dynamic actions accepted: %v", invalid)
		}
	}
}
