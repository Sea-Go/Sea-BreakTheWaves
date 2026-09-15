package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/usermodel"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

func favoriteAuthorityFixture(t *testing.T) (FactSourceEvidence, favoriteAuthorityResponse) {
	t.Helper()
	const id = "9007199254740995"
	const at = "2026-09-15T00:00:00Z"
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	subjectRaw, err := json.Marshal(subject)
	if err != nil {
		t.Fatal(err)
	}
	event := eventing.Event{EventID: "favorite." + id + ".v2", EventType: "rtw.favorite.retract", SchemaVersion: 1,
		Producer: favoriteFactProducer, AggregateID: id, AggregateVersion: 2, OperationID: "favorite." + id + ".v2",
		OccurredAt: at, Payload: json.RawMessage(`{"schema_version":1,"event_id":"favorite.9007199254740995.v2","subject_ref":{"authority_id":"rtw.identity","tenant_id":"platform","subject_id":"1001"},"target_type":"article","target_id":"article-snowflake","target_revision":null,"operation":"retract","source_ref":"rtw.favorite/9007199254740995","event_time":"2026-09-15T00:00:00Z","available_at":"2026-09-15T00:00:00Z","favorite_id":"9007199254740995","folder_id":"9007199254740993"}`)}
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
	receipt := eventing.Receipt{EventID: event.EventID, Producer: favoriteFactProducer, TechnicalStatus: "accepted",
		ReceiptID: "rtw-dc-receipt-1", InputHash: hash, Offset: 2, ReceivedAt: "2026-09-15T00:00:01Z"}
	evidence := FactSourceEvidence{Event: event, InputHash: hash, Offset: 2, Receipt: receipt}
	authority := favoriteAuthorityResponse{Event: raw, SubjectRef: subjectRaw,
		PredecessorEventID: "favorite." + id + ".v1", TechnicalReceipt: receipt, SourceEventHash: hash}
	return evidence, authority
}

func TestFavoriteAuthorityBinderCrossChecksFrozenSource(t *testing.T) {
	if _, err := NewFavoriteAuthorityBinder(FavoriteAuthorityBinderConfig{}); !errors.Is(err, ErrFactDeliveryContract) {
		t.Fatalf("missing authority config activated binder: %v", err)
	}
	evidence, source := favoriteAuthorityFixture(t)
	const token = "fixture-service-token-at-least-32-bytes"
	response := source
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/favorite/facts/rtw.community.favorite/favorite.9007199254740995.v2" ||
			r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unavailable", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	binder, err := NewFavoriteAuthorityBinder(FavoriteAuthorityBinderConfig{BaseURL: server.URL, Token: token, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := binder.BindFactWithEvidence(context.Background(), evidence)
	if err != nil || fact.Subject != (usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}) || fact.Action != usermodel.Retract ||
		fact.Supersedes == nil || fact.Supersedes.EventID != source.PredecessorEventID ||
		fact.ValueRef != "article/article-snowflake" {
		t.Fatalf("high ID authoritative retract: %+v %v", fact, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := binder.BindFactWithEvidence(cancelled, evidence); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled RTW authority read lost context cause: %v", err)
	}
	for name, mutate := range map[string]func(*favoriteAuthorityResponse){
		"wrong-subject": func(f *favoriteAuthorityResponse) {
			f.SubjectRef = json.RawMessage(`{"authority_id":"rtw.identity","tenant_id":"platform","subject_id":"impostor"}`)
		},
		"wrong-hash":        func(f *favoriteAuthorityResponse) { f.SourceEventHash = strings.Repeat("b", 64) },
		"wrong-offset":      func(f *favoriteAuthorityResponse) { f.TechnicalReceipt.Offset++ },
		"wrong-receipt":     func(f *favoriteAuthorityResponse) { f.TechnicalReceipt.ReceiptID = "different" },
		"wrong-time":        func(f *favoriteAuthorityResponse) { f.TechnicalReceipt.ReceivedAt = "2026-09-15T00:00:02Z" },
		"wrong-predecessor": func(f *favoriteAuthorityResponse) { f.PredecessorEventID = "favorite.other.v1" },
		"different-event":   func(f *favoriteAuthorityResponse) { f.Event = json.RawMessage(`{"event_id":"other"}`) },
		"unsafe-high-number": func(f *favoriteAuthorityResponse) {
			f.Event = json.RawMessage(strings.ReplaceAll(string(f.Event), `"favorite_id":"9007199254740995"`, `"favorite_id":9007199254740995`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			response = source
			mutate(&response)
			if _, err := binder.BindFactWithEvidence(context.Background(), evidence); !errors.Is(err, ErrFactDeliveryContract) {
				t.Fatalf("untrusted RTW/DC divergence was admitted: %v", err)
			}
		})
	}
	response = source
	evidence.InputHash = strings.Repeat("c", 64)
	if _, err := binder.BindFactWithEvidence(context.Background(), evidence); !errors.Is(err, ErrFactDeliveryContract) {
		t.Fatalf("DC input hash mismatch admitted: %v", err)
	}
}
