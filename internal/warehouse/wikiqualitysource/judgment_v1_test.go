package wikiqualitysource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/jackc/pgx/v5/pgxpool"
)

func v1Golden(t *testing.T) (eventing.Event, []byte) {
	t.Helper()
	file, err := os.ReadFile("testdata/wiki-quality-event-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if digest(file) != "6b54655324aadfea697e0cfe6fdd3a690be9f396b55563142cc8df428bab1f32" ||
		!bytes.HasSuffix(file, []byte("\n")) {
		t.Fatal("RTW static v1 Golden file changed before source contract was rechecked")
	}
	original := bytes.TrimSuffix(file, []byte("\n"))
	canonicalEvent, err := canonical(original)
	if err != nil || digest(original) != "b8a7f76b4abd10555f2b3f450963bbf518b9919491b714cc909ecb9508b3ac5f" ||
		digest(canonicalEvent) != "90e12c15843c7a1ad848da83edf97d4bce218f95eaa16d90afc2f26af02cd7a6" {
		t.Fatalf("RTW original Event raw bytes and JCS SHA were conflated: %v", err)
	}
	var event eventing.Event
	if err := json.Unmarshal(original, &event); err != nil {
		t.Fatal(err)
	}
	return event, original
}

func TestPinnedRTWSingleFactV1GoldenAndOriginalQuoteSpan(t *testing.T) {
	event, original := v1Golden(t)
	value, err := ParseJudgmentV1(event, original)
	if err != nil || value.JudgeRevision != "1" || value.FactID == "" ||
		value.WikiOriginKind != "manual_revision" || value.OriginCompileID != "" ||
		value.SourceByteStart != "21" || value.SourceByteEnd != "31" ||
		value.SourceQuote != "short fact" || value.Grade == nil || *value.Grade != 3 ||
		value.ActorID != "42" || !value.CitationPresent {
		t.Fatalf("RTW v1 Golden lost original source quote or human grade: %+v %v", value, err)
	}
	if err := (V1Verifier{}).VerifyQualityEvent(t.Context(), event, original); err != nil {
		t.Fatalf("exact v1 verifier rejected RTW Golden: %v", err)
	}
}

func TestPinnedRTWQualityV1RejectsDuplicateUnknownAndForgedFactAndGrade(t *testing.T) {
	event, original := v1Golden(t)
	change := func(t *testing.T, modify func(*JudgmentV1)) {
		t.Helper()
		var value JudgmentV1
		if err := json.Unmarshal(event.Payload, &value); err != nil {
			t.Fatal(err)
		}
		modify(&value)
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		modified := event
		modified.Payload = payload
		modifiedOriginal, err := json.Marshal(modified)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseJudgmentV1(modified, modifiedOriginal); !errors.Is(err, ErrContract) {
			t.Fatalf("forged RTW v1 human label accepted: %v", err)
		}
	}
	change(t, func(v *JudgmentV1) { v.SourceByteStart = "20" })
	change(t, func(v *JudgmentV1) { v.SourceByteStart = "021" })
	change(t, func(v *JudgmentV1) { v.SourceQuoteSHA256 = strings.Repeat("0", 64) })
	change(t, func(v *JudgmentV1) { grade := 4; v.Grade = &grade })
	change(t, func(v *JudgmentV1) { v.CitationPresent = false })
	change(t, func(v *JudgmentV1) { v.Assessment = "undetermined" })
	change(t, func(v *JudgmentV1) { v.WikiOriginKind = "ai_accepted" })
	change(t, func(v *JudgmentV1) { v.ActorID = "" })
	change(t, func(v *JudgmentV1) { v.ActorID = "control\ncharacter" })
	// An allowlisted Admin Auth claim may have a name rather than a canonical
	// UserCenter UID. Event provenance must preserve that exact source literal.
	var validClaim JudgmentV1
	if err := json.Unmarshal(event.Payload, &validClaim); err != nil {
		t.Fatal(err)
	}
	validClaim.ActorID = "管理员 test-admin"
	claimPayload, err := json.Marshal(validClaim)
	if err != nil {
		t.Fatal(err)
	}
	validActorEvent := event
	validActorEvent.Payload = claimPayload
	if _, err := ParseJudgmentV1(validActorEvent, jsonEvent(t, validActorEvent)); err != nil {
		t.Fatalf("source-admitted RTW admin userId was forged into numeric UID: %v", err)
	}
	modified := event
	modified.Payload = bytes.Replace(event.Payload, []byte(`"grade":3`), []byte(`"grade":3,"grade":3`), 1)
	duplicateOuter := bytes.Replace(original, []byte(`"event_id":`), []byte(`"event_id":"duplicate", "event_id":`), 1)
	for name, raw := range map[string][]byte{
		"duplicate grade":    jsonEvent(t, modified),
		"duplicate outer ID": duplicateOuter,
	} {
		var parsed eventing.Event
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("%s fixture is malformed: %v", name, err)
		}
		if _, err := ParseJudgmentV1(parsed, raw); !errors.Is(err, ErrContract) {
			t.Fatalf("%s literal keys accepted: %v", name, err)
		}
	}
	unknown := event
	unknown.Payload = bytes.Replace(event.Payload, []byte(`"grade":3`), []byte(`"grade":3,"tenant_id":"invented"`), 1)
	if _, err := ParseJudgmentV1(unknown, jsonEvent(t, unknown)); !errors.Is(err, ErrContract) {
		t.Fatalf("unknown RTW product tenant appeared in quality Event: %v", err)
	}
	// ParseJudgmentV1 cannot be called on a mutable DC copy with an unrelated
	// original source: the whole original/DC Event JCS must still agree.
	modified = event
	modified.EventID = "forged-event"
	if _, err := ParseJudgmentV1(modified, original); !errors.Is(err, ErrContract) {
		t.Fatalf("DC Event identity differed from RTW original bytes: %v", err)
	}
}

func jsonEvent(t *testing.T, event eventing.Event) []byte {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPinnedV1GoldenThroughRealPGIndependentODSAndACK(t *testing.T) {
	dsn := os.Getenv("WIKI_QUALITY_SOURCE_TEST_DSN")
	if dsn == "" {
		t.Skip("run acceptance.sh through disposable PostgreSQL for RTW static v1 Golden")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Initialize(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `TRUNCATE warehouse_wiki_quality.ods_event,
 warehouse_wiki_quality.consumer_cursor`); err != nil {
		t.Fatal(err)
	}
	batch, receipts, _ := testBatch(t)
	event, original := v1Golden(t)
	previousID := batch.Events[1].Event.EventID
	batch.Events[1].Event = event
	encoded, err := canonical(jsonEvent(t, event))
	if err != nil {
		t.Fatal(err)
	}
	batch.Events[1].InputHash = digest(encoded)
	delete(receipts, previousID)
	receipts[event.EventID] = eventing.Receipt{EventID: event.EventID, Producer: Producer,
		TechnicalStatus: "accepted", ReceiptID: "receipt-" + event.EventID,
		InputHash: batch.Events[1].InputHash, Offset: 2, ReceivedAt: "2026-09-16T00:05:00Z"}
	resetBatchHash(t, &batch)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet ||
			r.URL.Path != "/internal/v1/knowledge/wiki-quality/events/"+event.EventID ||
			r.Header.Get("Authorization") != "Bearer fixture-worker-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]string{
			"event_id":         event.EventID,
			"event_json":       string(original),
			"event_raw_sha256": digest(original),
			"event_jcs_sha256": batch.Events[1].InputHash,
		}})
	}))
	defer server.Close()
	authority, err := NewHTTPAuthority(server.URL, "fixture-worker-token")
	if err != nil {
		t.Fatal(err)
	}
	source := &testSource{batch: batch, receipts: receipts}
	consumer := &Consumer{DB: db, Source: source,
		Authority: authority,
		Verifier:  V1Verifier{}, Consumer: DefaultConsumer}
	result, err := consumer.RunOnce(ctx)
	if err != nil || result.Read != 3 || result.QualityVerified != 1 ||
		result.TechnicalSkips != 2 || result.CommittedOffset != 3 || result.AcknowledgedOffset != 3 {
		t.Fatalf("RTW static v1 Golden through independent PG ODS/ACK: %+v %v", result, err)
	}
	var storedRaw []byte
	var originalSHA, dcSHA string
	if err := db.QueryRow(ctx, `SELECT authority_event_json,authority_event_sha256,dc_input_hash
 FROM warehouse_wiki_quality.ods_event WHERE event_id=$1`, event.EventID).Scan(
		&storedRaw, &originalSHA, &dcSHA); err != nil ||
		!bytes.Equal(storedRaw, original) || originalSHA != digest(original) ||
		dcSHA != batch.Events[1].InputHash || originalSHA == dcSHA {
		t.Fatalf("RTW original/JCS/DC hash domains changed in ODS: %v", err)
	}
	if _, err := db.Exec(ctx, `TRUNCATE warehouse_wiki_quality.ods_event,
 warehouse_wiki_quality.consumer_cursor`); err != nil {
		t.Fatal(err)
	}
}
