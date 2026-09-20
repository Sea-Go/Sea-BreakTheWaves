package communityauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

func authorityFixture(t *testing.T) (Evidence, Fact) {
	t.Helper()
	event := eventing.Event{EventID: "rtw.like.1", EventType: "community.target.interaction", SchemaVersion: 1,
		Producer: "rtw.like-mq", AggregateID: "like-state/1001/article/a1", AggregateVersion: 1,
		OperationID: "1", OccurredAt: "2026-09-15T00:00:00Z", Payload: json.RawMessage(`{"source":"fixture"}`)}
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
	receipt := eventing.Receipt{EventID: event.EventID, Producer: event.Producer, TechnicalStatus: "accepted",
		ReceiptID: "receipt-1", InputHash: hash, Offset: 1, ReceivedAt: "2026-09-15T00:00:01Z"}
	evidence := Evidence{Event: event, InputHash: hash, Offset: 1, Receipt: receipt}
	fact := Fact{Event: event, SubjectRef: SubjectRef{Issuer: "rtw.identity", SubjectID: "1001"},
		TechnicalReceipt: receipt, SourceEventHash: hash}
	return evidence, fact
}

func TestClientVerifiesProjectionNeutralAuthority(t *testing.T) {
	evidence, expected := authorityFixture(t)
	response := expected
	const token = "community-authority-client-token-at-least-32-bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/community/facts" || r.URL.Query().Get("producer") != evidence.Event.Producer ||
			r.URL.Query().Get("event_id") != evidence.Event.EventID || r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: token, Producer: evidence.Event.Producer, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Lookup(context.Background(), evidence)
	if err != nil || got.SubjectRef != expected.SubjectRef || got.Event.EventID != expected.Event.EventID {
		t.Fatalf("verified authority: %+v %v", got, err)
	}
	for name, mutate := range map[string]func(*Fact){
		"wrong-issuer":  func(f *Fact) { f.SubjectRef.Issuer = "other" },
		"invalid-id":    func(f *Fact) { f.SubjectRef.SubjectID = "01" },
		"wrong-hash":    func(f *Fact) { f.SourceEventHash = strings.Repeat("a", 64) },
		"wrong-receipt": func(f *Fact) { f.TechnicalReceipt.Offset = 2 },
		"changed-event": func(f *Fact) { f.Event.OperationID = "changed" },
	} {
		t.Run(name, func(t *testing.T) {
			response = expected
			mutate(&response)
			if _, err := client.Lookup(context.Background(), evidence); err == nil {
				t.Fatal("mismatched authority accepted")
			}
		})
	}
	response = expected
}

func TestSubjectWireRejectsRealmAndTenant(t *testing.T) {
	raw, err := json.Marshal(SubjectRef{Issuer: "rtw.identity", SubjectID: "1001"})
	if err != nil || string(raw) != `{"issuer":"rtw.identity","subject_id":"1001"}` {
		t.Fatalf("v2 subject wire: %s %v", raw, err)
	}
	var subject SubjectRef
	if err := strictJSON(strings.NewReader(`{"issuer":"rtw.identity","subject_id":"1001","realm":"platform"}`), &subject); err == nil {
		t.Fatal("realm field accepted by v2 authority wire")
	}
	if err := strictJSON(strings.NewReader(`{"issuer":"rtw.identity","subject_id":"1001","tenant_id":"platform"}`), &subject); err == nil {
		t.Fatal("tenant field accepted by v2 authority wire")
	}
}
