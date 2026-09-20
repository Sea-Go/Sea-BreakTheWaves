package grounding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func authorityFixture(t *testing.T, value Case, caseRaw []byte, kind string) (RTWReviewReceipt, RTWReviewerKey) {
	t.Helper()
	claim := Claim{StartByte: 0, EndByte: len(value.AcceptedAnswer), Text: value.AcceptedAnswer,
		EvidenceIDs: []string{value.Evidence[0].ID}, Label: "unsupported",
		Reason: "The cited quote does not support the asserted detail."}
	reviewRaw, trust := signedFixture(t, value, caseRaw, claim, false, kind)
	var review Review
	if err := json.Unmarshal(reviewRaw, &review); err != nil {
		t.Fatal(err)
	}
	receipt := RTWReviewReceipt{SchemaVersion: RTWReceiptSchema, CaseSHA256: Digest(caseRaw),
		CaseJSON: string(caseRaw), ReviewJSON: string(reviewRaw), ReviewSHA256: Digest(reviewRaw),
		KeyID: "key-test-1", ReviewerAuthority: trust.Authority, ReviewerID: trust.ReviewerID,
		DataKind: kind, RegistryRevisionAtCommit: 1,
		AnswerID: value.AnswerID, SearchID: value.SearchID,
		AcceptedAnswerSHA256: value.AcceptedAnswerSHA256, TurnSHA256: Digest([]byte("RTW turn")),
		CitationPackRef:      value.CitationPackRef,
		CitationPackSHA256:   Digest([]byte("actual RTW pack JSON")),
		TraceAuthorityStatus: "external_case_unverified", ReviewedAt: review.Payload.ReviewedAt,
		EventID: "event-review-1", EventSHA256: Digest([]byte("RTW outbox event"))}
	key := RTWReviewerKey{SchemaVersion: RTWKeySchema, KeyID: receipt.KeyID,
		ReviewerAuthority: trust.Authority, ReviewerID: trust.ReviewerID, DataKind: kind,
		PublicKeyEd25519Hex: trust.PublicKey, RegisteredAt: "2026-09-14T00:00:00Z",
		Status: "active", RegistryRevision: 1, RegistrationEventID: "event-key-1"}
	return receipt, key
}

func authorityServer(t *testing.T, receipt RTWReviewReceipt, key RTWReviewerKey, noReview, noKey bool,
	citationEdits ...func(*RTWCitationRecord)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer worker-test-token" || r.Method != http.MethodGet {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var data any
		switch r.URL.Path {
		case "/internal/v1/knowledge/answer-grounding/reviews/" + receipt.CaseSHA256:
			if noReview {
				http.NotFound(w, r)
				return
			}
			data = receipt
		case "/internal/v1/knowledge/reviewer-keys/" + key.KeyID:
			if noKey {
				http.NotFound(w, r)
				return
			}
			data = key
		case "/internal/v1/knowledge/search-citations/" + receipt.SearchID:
			var value Case
			if json.Unmarshal([]byte(receipt.CaseJSON), &value) != nil {
				http.Error(w, "bad fixture", 500)
				return
			}
			evidence := make([]RTWCitationEvidence, len(value.Evidence))
			for i, item := range value.Evidence {
				evidence[i] = RTWCitationEvidence{ID: item.ID, SourceKind: "source", ContentID: "book",
					RevisionID: "revision", ChunkID: "chunk", Original: json.RawMessage(`{"key":"object"}`),
					Locator: json.RawMessage(`{"locator":"paragraph:1"}`), QuoteHash: item.QuoteSHA256,
					State: "available"}
			}
			record := RTWCitationRecord{SearchID: receipt.SearchID, PackHash: receipt.CitationPackSHA256,
				DurableRef: receipt.CitationPackRef, ModuleID: "module-test", ReleaseID: "release-test",
				Generation: 1, PublicationRevision: "1", Evidence: evidence}
			for _, edit := range citationEdits {
				edit(&record)
			}
			data = record
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "success", "data": data})
	}))
}

func TestRTWWorkerRegistryBindsSignedReviewAndRevocation(t *testing.T) {
	value := fixtureCase(t, "The room is quiet.", "The room is quiet, and the lights are blue.")
	caseRaw := caseBytes(t, value)
	receipt, key := authorityFixture(t, value, caseRaw, "synthetic_fixture")
	if receipt.CitationPackRef != citationRef(value.SearchID) ||
		receipt.CitationPackSHA256 == strings.TrimPrefix(receipt.CitationPackRef, citationRefPrefix) {
		t.Fatal("fixture conflated RTW durable search identity with evidence-pack hash")
	}
	server := authorityServer(t, receipt, key, false, false)
	client, err := NewHTTPAuthority(server.URL, "worker-test-token")
	if err != nil {
		t.Fatal(err)
	}
	result, err := EvaluateFromRTW(context.Background(), value, caseRaw, client)
	wrongToken, _ := NewHTTPAuthority(server.URL, "not-the-worker-token")
	if _, err := EvaluateFromRTW(context.Background(), value, caseRaw, wrongToken); err == nil {
		t.Fatal("unauthorized Worker response was accepted or fell back to local key")
	}
	server.Close()
	if err != nil || result.Decision != "synthetic_rejected_unsupported" ||
		result.EvidenceLevel != "rtw_registry_synthetic_fixture" || result.RuntimeBlocking {
		t.Fatalf("active RTW synthetic key did not support offline rejection: %+v %v", result, err)
	}
	key.Status, key.RegistryRevision = "revoked", 2
	key.RevokedAt, key.RevocationEventID = "2026-09-16T00:00:00Z", "event-key-revoke-1"
	server = authorityServer(t, receipt, key, false, false)
	client, _ = NewHTTPAuthority(server.URL, "worker-test-token")
	result, err = EvaluateFromRTW(context.Background(), value, caseRaw, client)
	server.Close()
	if err != nil || result.Decision != "pending_revoked_authority" ||
		result.EvidenceLevel != "rtw_registry_revoked" || result.RuntimeBlocking {
		t.Fatalf("revoked key retained present authority: %+v %v", result, err)
	}
	server = authorityServer(t, receipt, key, true, false)
	client, _ = NewHTTPAuthority(server.URL, "worker-test-token")
	result, err = EvaluateFromRTW(context.Background(), value, caseRaw, client)
	server.Close()
	if err != nil || result.Decision != "pending_human_review" {
		t.Fatalf("missing RTW review invented a decision: %+v %v", result, err)
	}
	key.Status, key.RegistryRevision = "active", 1
	key.RevokedAt, key.RevocationEventID = "", ""
	server = authorityServer(t, receipt, key, false, true)
	client, _ = NewHTTPAuthority(server.URL, "worker-test-token")
	result, err = EvaluateFromRTW(context.Background(), value, caseRaw, client)
	server.Close()
	if err != nil || result.Decision != "pending_authority_registry" ||
		result.EvidenceLevel != "rtw_registry_key_not_found" || result.RuntimeBlocking {
		t.Fatalf("missing current RTW key was not pending: %+v %v", result, err)
	}
}

func TestRTWWorkerRefusesForgedAuthorityAndCaseEvidence(t *testing.T) {
	value := fixtureCase(t, "A published passage.", "A published passage claims more.")
	caseRaw := caseBytes(t, value)
	receipt, key := authorityFixture(t, value, caseRaw, "synthetic_fixture")
	for name, edit := range map[string]func(*RTWReviewReceipt, *RTWReviewerKey){
		"case bytes":  func(r *RTWReviewReceipt, _ *RTWReviewerKey) { r.CaseJSON = "{}" },
		"review hash": func(r *RTWReviewReceipt, _ *RTWReviewerKey) { r.ReviewSHA256 = Digest([]byte("forged")) },
		"answer hash": func(r *RTWReviewReceipt, _ *RTWReviewerKey) { r.AcceptedAnswerSHA256 = Digest([]byte("other")) },
		"pack ref": func(r *RTWReviewReceipt, _ *RTWReviewerKey) {
			r.CitationPackRef = "search-citations/sha256/" + Digest([]byte("other"))
		},
		"pack hash is durable identity": func(r *RTWReviewReceipt, _ *RTWReviewerKey) {
			r.CitationPackSHA256 = strings.TrimPrefix(r.CitationPackRef, citationRefPrefix)
		},
		"review signature": func(r *RTWReviewReceipt, _ *RTWReviewerKey) {
			raw := []byte(strings.Replace(r.ReviewJSON, `"unsupported"`, `"supported"`, 1))
			r.ReviewJSON, r.ReviewSHA256 = string(raw), Digest(raw)
		},
		"trace claim":        func(r *RTWReviewReceipt, _ *RTWReviewerKey) { r.TraceAuthorityStatus = "RTW_verified" },
		"wrong registry key": func(_ *RTWReviewReceipt, k *RTWReviewerKey) { k.PublicKeyEd25519Hex = strings.Repeat("0", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changedReceipt, changedKey := receipt, key
			edit(&changedReceipt, &changedKey)
			server := authorityServer(t, changedReceipt, changedKey, false, false)
			defer server.Close()
			client, err := NewHTTPAuthority(server.URL, "worker-test-token")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := EvaluateFromRTW(context.Background(), value, caseRaw, client); err == nil {
				t.Fatal("forged RTW review/registry receipt passed")
			}
		})
	}
	for name, edit := range map[string]func(*RTWCitationRecord){
		"citation quote hash": func(c *RTWCitationRecord) {
			c.Evidence[0].QuoteHash = Digest([]byte("forged quote"))
		},
		"citation durable ref": func(c *RTWCitationRecord) {
			c.DurableRef = citationRef("another-search")
		},
		"citation pack hash": func(c *RTWCitationRecord) {
			c.PackHash = Digest([]byte("different evidence pack"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := authorityServer(t, receipt, key, false, false, edit)
			defer server.Close()
			client, err := NewHTTPAuthority(server.URL, "worker-test-token")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := EvaluateFromRTW(context.Background(), value, caseRaw, client); err == nil {
				t.Fatal("forged RTW citation record passed")
			}
		})
	}
	receipt, key = authorityFixture(t, value, caseRaw, "human_admin")
	server := authorityServer(t, receipt, key, false, false)
	client, _ := NewHTTPAuthority(server.URL, "worker-test-token")
	result, err := EvaluateFromRTW(context.Background(), value, caseRaw, client)
	server.Close()
	if err != nil || result.Decision != "pending_authority_service_identity" || result.RuntimeBlocking {
		t.Fatalf("caller-selected fake RTW server promoted human_admin: %+v %v", result, err)
	}
}
