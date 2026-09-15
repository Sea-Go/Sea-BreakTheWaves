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

func catalogQualityBatch(t *testing.T) (eventing.Batch, map[string]eventing.Receipt,
	eventing.Event, []byte, eventing.Event, []byte) {
	t.Helper()
	batch, receipts, _ := testBatch(t)
	quality, qualityOriginal := v1Golden(t)
	catalog, catalogOriginal := staticFactSetGolden(t)
	for index, event := range map[int]eventing.Event{1: quality, 2: catalog} {
		delete(receipts, batch.Events[index].Event.EventID)
		batch.Events[index].Event = event
		encoded, err := canonical(jsonEvent(t, event))
		if err != nil {
			t.Fatal(err)
		}
		batch.Events[index].InputHash = digest(encoded)
		receipts[event.EventID] = eventing.Receipt{EventID: event.EventID, Producer: Producer,
			TechnicalStatus: "accepted", ReceiptID: "receipt-" + event.EventID,
			InputHash: batch.Events[index].InputHash, Offset: int64(index + 1),
			ReceivedAt: "2026-09-16T00:05:00Z"}
	}
	resetBatchHash(t, &batch)
	return batch, receipts, quality, qualityOriginal, catalog, catalogOriginal
}

func TestRealPGIndependentCursorCatalogAndJudgmentWithLostACK(t *testing.T) {
	dsn := os.Getenv("WIKI_QUALITY_SOURCE_TEST_DSN")
	if dsn == "" {
		t.Skip("run Wiki quality acceptance.sh for isolated PostgreSQL v2 ODS")
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
	if _, err := db.Exec(ctx, `TRUNCATE warehouse_wiki_quality.ods_fact_set,
 warehouse_wiki_quality.ods_event,warehouse_wiki_quality.consumer_cursor`); err != nil {
		t.Fatal(err)
	}
	batch, receipts, quality, qualityRaw, catalog, catalogRaw := catalogQualityBatch(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture-worker-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/internal/v1/knowledge/wiki-quality/events/" + quality.EventID:
			canonicalEvent, _ := canonical(qualityRaw)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]string{
				"event_id":         quality.EventID,
				"event_json":       string(qualityRaw),
				"event_raw_sha256": digest(qualityRaw),
				"event_jcs_sha256": digest(canonicalEvent),
			}})
		case "/internal/v1/knowledge/wiki-fact-sets/events/" + catalog.EventID:
			wholeJCS, _ := canonical(catalogRaw)
			payloadJCS, _ := canonical(catalog.Payload)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]string{
				"event_id":            catalog.EventID,
				"event_json":          string(catalogRaw),
				"event_raw_sha256":    digest(catalogRaw),
				"event_jcs_sha256":    digest(wholeJCS),
				"fact_set_jcs_sha256": digest(payloadJCS),
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	authority, err := NewHTTPAuthority(server.URL, "fixture-worker-token")
	if err != nil {
		t.Fatal(err)
	}
	source := &testSource{batch: batch, receipts: receipts, ackLost: true}
	old := &Consumer{DB: db, Source: source, Authority: authority,
		Verifier: V1Verifier{}, Consumer: DefaultConsumer}
	if _, err := old.RunOnce(ctx); !errors.Is(err, ErrContract) || source.ackOffset != 0 {
		t.Fatalf("old Wiki quality mode skipped complete FactSet offset: %v ack=%d", err, source.ackOffset)
	}
	partial := &Consumer{DB: db, Source: source, Authority: authority,
		Verifier: V1Verifier{}, FactSetAuthority: authority, Consumer: DefaultConsumer}
	if _, err := partial.RunOnce(ctx); !errors.Is(err, ErrFactSetUnconfigured) || source.ackOffset != 0 {
		t.Fatalf("FactSet authority without typed verifier advanced DC cursor: %v ack=%d", err, source.ackOffset)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM warehouse_wiki_quality.ods_event`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unconfigured FactSet wrote PG source rows: %d %v", count, err)
	}
	consumer := &Consumer{DB: db, Source: source, Authority: authority,
		Verifier: V1Verifier{}, FactSetAuthority: authority,
		FactSetVerifier: FactSetV1Verifier{}, Consumer: DefaultConsumer}
	first, err := consumer.RunOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), "ACK") || first.Read != 3 ||
		first.Catalogs != 1 || first.QualityVerified != 1 || first.TechnicalSkips != 1 ||
		first.CommittedOffset != 3 || first.AcknowledgedOffset != 0 {
		t.Fatalf("RTW static FactSet+quality full PG batch commit/lost ACK: %+v %v", first, err)
	}
	var catalogs, judgments, skips, cursor int
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='fact_set_verified'),
 count(*) FILTER (WHERE status='quality_verified'),
 count(*) FILTER (WHERE status='technical_skip') FROM warehouse_wiki_quality.ods_event`).Scan(
		&catalogs, &judgments, &skips); err != nil || catalogs != 1 || judgments != 1 || skips != 1 {
		t.Fatalf("shared producer Catalog/Judgment/technical rows: %d/%d/%d %v", catalogs, judgments, skips, err)
	}
	var original []byte
	var originalSHA, payloadSHA, sidecarID, sidecarScope string
	var sidecarJCS []byte
	if err := db.QueryRow(ctx, `SELECT e.authority_event_json,e.authority_event_sha256,
 e.fact_set_payload_jcs_sha256,f.revision_id,f.source_scope_revision,f.payload_jcs
 FROM warehouse_wiki_quality.ods_event e JOIN warehouse_wiki_quality.ods_fact_set f
 ON e.producer=f.producer AND e.source_offset=f.source_offset
 WHERE e.event_id=$1`, catalog.EventID).Scan(&original, &originalSHA, &payloadSHA,
		&sidecarID, &sidecarScope, &sidecarJCS); err != nil ||
		!bytes.Equal(original, catalogRaw) || originalSHA != factSetGoldenRawSHA ||
		payloadSHA != factSetGoldenPayloadJCS || digest(sidecarJCS) != payloadSHA ||
		sidecarID == "" || sidecarScope == "" {
		t.Fatalf("FactSet original Event/JCS/typed sidecar domains changed: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT committed_offset FROM warehouse_wiki_quality.consumer_cursor
 WHERE consumer=$1`, DefaultConsumer).Scan(&cursor); err != nil || cursor != 3 {
		t.Fatalf("independent FactSet ODS committed cursor: %d %v", cursor, err)
	}
	second, err := consumer.RunOnce(ctx)
	if err != nil || second.Read != 3 || second.CommittedOffset != 3 || second.AcknowledgedOffset != 3 {
		t.Fatalf("mixed Catalog/Judgment lost ACK replay duplicated PG: %+v %v", second, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM warehouse_wiki_quality.ods_fact_set`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("FactSet sidecar replay added another directory revision: %d %v", count, err)
	}
	if _, err := db.Exec(ctx, `TRUNCATE warehouse_wiki_quality.ods_fact_set,
 warehouse_wiki_quality.ods_event,warehouse_wiki_quality.consumer_cursor`); err != nil {
		t.Fatal(err)
	}
}
