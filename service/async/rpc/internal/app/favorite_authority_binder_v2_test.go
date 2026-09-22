package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

func TestFavoriteAuthorityBinderV2SourceAndStrictSubject(t *testing.T) {
	const favoriteID = "9007199254743993"
	const eventID = "favorite." + favoriteID + ".v1"
	const token = "v2-source-service-token-at-least-32-bytes"
	const at = "2026-09-15T00:00:00Z"
	payload := json.RawMessage(`{"schema_version":2,"event_id":"` + eventID + `","subject_ref":{"issuer":"rtw.identity","subject_id":"1001"},"target_type":"article","target_id":"article-v2","target_revision":"article-v2:r1","operation":"assert","source_ref":"rtw.favorite/` + favoriteID + `","event_time":"` + at + `","available_at":"` + at + `","favorite_id":"` + favoriteID + `","folder_id":"9007199254743991"}`)
	event := eventing.Event{EventID: eventID, EventType: "rtw.favorite.assert", SchemaVersion: 2,
		Producer: favoriteFactProducer, AggregateID: favoriteID, AggregateVersion: 1,
		OperationID: eventID, OccurredAt: at, Payload: payload}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	receipt := eventing.Receipt{EventID: eventID, Producer: favoriteFactProducer, TechnicalStatus: "accepted",
		ReceiptID: "v2-accepted-receipt", InputHash: hash, Offset: 1, ReceivedAt: "2026-09-15T00:00:01Z"}
	evidence := FactSourceEvidence{Event: event, InputHash: hash, Offset: 1, Receipt: receipt}
	authority := favoriteAuthorityResponse{Event: raw,
		SubjectRef:       json.RawMessage(`{"issuer":"rtw.identity","subject_id":"1001"}`),
		TechnicalReceipt: receipt, SourceEventHash: hash}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v2/favorite/facts/"+favoriteFactProducer+"/"+eventID ||
			r.Header.Get("Authorization") != "Bearer "+token {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(authority)
	}))
	defer server.Close()
	binder, err := NewFavoriteAuthorityBinder(FavoriteAuthorityBinderConfig{
		BaseURL: server.URL, Token: token, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := binder.BindFactWithEvidence(context.Background(), evidence)
	wantSubject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	if err != nil || fact.Subject != wantSubject || fact.Action != usermodel.Assert ||
		fact.ValueRef != "article/article-v2/revision/article-v2:r1" || fact.Supersedes != nil {
		t.Fatalf("v2 RTW authority not normalized exactly once: %+v %v", fact, err)
	}
	for _, bad := range []json.RawMessage{
		json.RawMessage(`{"issuer":"old.identity","subject_id":"1001"}`),
		json.RawMessage(`{"issuer":"rtw.identity","subject_id":"0"}`),
		json.RawMessage(`{"issuer":"rtw.identity","subject_id":"01"}`),
		json.RawMessage(`{"issuer":"rtw.identity","subject_id":"1001","tenant_id":"platform"}`),
		json.RawMessage(`{"authority_id":"rtw.identity","tenant_id":"platform","subject_id":"1001"}`),
		json.RawMessage(`{"issuer":"rtw.identity"}`),
	} {
		if _, err := parseFavoriteAuthoritySubject(2, bad); !errors.Is(err, ErrFactDeliveryContract) {
			t.Fatalf("v2 accepted illegal source subject %s: %v", bad, err)
		}
	}
	if _, err := parseFavoriteAuthoritySubject(1, authority.SubjectRef); !errors.Is(err, ErrFactDeliveryContract) {
		t.Fatal("v2 shape crossed into v1 schema")
	}
	authority.SubjectRef = json.RawMessage(`{"issuer":"rtw.identity","subject_id":"1002"}`)
	if _, err := binder.BindFactWithEvidence(context.Background(), evidence); !errors.Is(err, ErrFactDeliveryContract) {
		t.Fatalf("RTW response subject displaced frozen v2 source: %v", err)
	}
	authority.SubjectRef = json.RawMessage(`{"issuer":"rtw.identity","subject_id":"1001"}`)
	evidence.Event.SchemaVersion = 1
	if _, err := binder.BindFactWithEvidence(context.Background(), evidence); !errors.Is(err, ErrFactAuthorityUnavailable) {
		t.Fatalf("v2 source was obtained through v1 private route: %v", err)
	}
}
