package wikiqualitysource

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/jackc/pgx/v5/pgxpool"
)

type realFactSetFixture struct {
	RTWURL                 string   `json:"rtw_url"`
	RTWToken               string   `json:"rtw_token"`
	DCURL                  string   `json:"dc_url"`
	DCToken                string   `json:"dc_token"`
	ExpectedEvents         int      `json:"expected_events"`
	CatalogEventID         string   `json:"catalog_event_id"`
	QualityEventIDs        []string `json:"quality_event_ids"`
	CatalogQualityEventIDs []string `json:"catalog_quality_event_ids"`
	ResultPath             string   `json:"result_path"`
	FactSetRevisionID      string   `json:"fact_set_revision_id"`
	SourceScopeRevision    string   `json:"source_scope_revision"`
}

var realFactSetFixtureKeys = map[string]literalKind{
	"rtw_url": stringKind, "rtw_token": stringKind,
	"dc_url": stringKind, "dc_token": stringKind,
	"expected_events": numberKind, "catalog_event_id": stringKind,
	"quality_event_ids": arrayKind, "catalog_quality_event_ids": arrayKind,
	"result_path": stringKind, "fact_set_revision_id": stringKind,
	"source_scope_revision": stringKind,
}

func readPrivateFactSetFixture(t *testing.T, path string) realFactSetFixture {
	t.Helper()
	var fixture realFactSetFixture
	if !filepath.IsAbs(path) {
		t.Fatal("real FactSet source requires task-owned absolute private fixture")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("real FactSet source fixture must be ordinary 0600 file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 2 || len(raw) > 32<<10 {
		t.Fatal("real FactSet source fixture unavailable or unbounded")
	}
	if exactObjectSized(raw, 32<<10, realFactSetFixtureKeys) != nil {
		t.Fatal("real FactSet source fixture omits, duplicates or invents literal keys")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fixture) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("real FactSet source fixture has unknown, duplicate or trailing fields")
	}
	realLoopback(t, fixture.RTWURL)
	realLoopback(t, fixture.DCURL)
	if fixture.RTWToken == "" || fixture.DCToken == "" ||
		fixture.ExpectedEvents < 5 || fixture.ExpectedEvents > 128 ||
		!eventIDPattern.MatchString(fixture.CatalogEventID) ||
		!idPattern.MatchString(fixture.FactSetRevisionID) ||
		len(fixture.SourceScopeRevision) != len("scope_")+64 ||
		fixture.SourceScopeRevision[:len("scope_")] != "scope_" ||
		!shaPattern.MatchString(fixture.SourceScopeRevision[len("scope_"):]) ||
		len(fixture.QualityEventIDs) != 3 || len(fixture.CatalogQualityEventIDs) != 2 ||
		!filepath.IsAbs(fixture.ResultPath) ||
		filepath.Dir(fixture.ResultPath) != filepath.Dir(path) {
		t.Fatal("real FactSet and three Judgment source handoff is incomplete")
	}
	all := map[string]bool{}
	for _, id := range fixture.QualityEventIDs {
		if !eventIDPattern.MatchString(id) || id == fixture.CatalogEventID || all[id] {
			t.Fatal("real FactSet quality EventIDs are invalid or duplicates")
		}
		all[id] = true
	}
	target := map[string]bool{}
	for _, id := range fixture.CatalogQualityEventIDs {
		if !all[id] || target[id] {
			t.Fatal("catalog target quality EventID is absent or duplicated")
		}
		target[id] = true
	}
	return fixture
}

func TestRealFactSetFixtureCannotDuplicateCatalogOrInventTenant(t *testing.T) {
	encoded, err := json.Marshal(map[string]any{
		"rtw_url": "http://127.0.0.1:1", "rtw_token": "fixture-only",
		"dc_url": "http://127.0.0.1:2", "dc_token": "fixture-only",
		"expected_events": 5, "catalog_event_id": "catalog-fixture",
		"quality_event_ids":         []string{"quality-1", "quality-2", "quality-3"},
		"catalog_quality_event_ids": []string{"quality-2", "quality-3"},
		"result_path":               "/tmp/task-only-result", "fact_set_revision_id": "fact-set-revision",
		"source_scope_revision": "scope-fixture",
	})
	if err != nil || exactObjectSized(encoded, 32<<10, realFactSetFixtureKeys) != nil {
		t.Fatalf("valid private fixture envelope rejected: %v", err)
	}
	duplicate := bytes.Replace(encoded, []byte(`"catalog_event_id":"catalog-fixture"`),
		[]byte(`"catalog_event_id":"catalog-fixture","catalog_event_id":"catalog-fixture"`), 1)
	if bytes.Equal(duplicate, encoded) || exactObjectSized(duplicate, 32<<10, realFactSetFixtureKeys) == nil {
		t.Fatal("duplicate source Catalog EventID was accepted")
	}
	unknown := bytes.Replace(encoded, []byte(`}`), []byte(`,"tenant_id":"invented"}`), 1)
	if exactObjectSized(unknown, 32<<10, realFactSetFixtureKeys) == nil {
		t.Fatal("invented product tenant appeared in RTW/BTW private fixture")
	}
}

// RTW signs business Catalog/Judgment revisions, DC owns one shared producer
// prefix, BTW alone proves original Events before committing its v2 ODS/ACK.
func TestRTWRealWikiFactSetSource(t *testing.T) {
	path := os.Getenv("SEA_RTW_REAL_WIKI_FACT_SET_FIXTURE")
	if path == "" {
		t.Skip("RTW FactSet Holder must explicitly supply real RTW/DC services")
	}
	dsn := os.Getenv("WIKI_QUALITY_SOURCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("run through acceptance.sh for disposable BTW v2 ODS PostgreSQL")
	}
	fixture := readPrivateFactSetFixture(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	dc, err := datacenter.New(httpclient.Config{BaseURL: fixture.DCURL,
		Token: fixture.DCToken, HTTPClient: &http.Client{Timeout: 15 * time.Second}})
	if err != nil {
		t.Fatal("DC real FactSet event client not configured")
	}
	authority, err := NewHTTPAuthority(fixture.RTWURL, fixture.RTWToken)
	if err != nil {
		t.Fatal("RTW real FactSet private Event authority not configured")
	}
	proof, err := authority.ReadFactSetEvent(ctx, fixture.CatalogEventID)
	if err != nil || digest(proof.EventJSON) != proof.EventRawSHA256 {
		t.Fatalf("RTW actual Catalog source original unavailable: %v", err)
	}
	var catalogEvent eventing.Event
	if json.Unmarshal(proof.EventJSON, &catalogEvent) != nil ||
		catalogEvent.EventID != fixture.CatalogEventID {
		t.Fatal("RTW actual Catalog source Event ID changed")
	}
	catalog, err := ParseFactSetV1(catalogEvent, proof.EventJSON, proof.FactSetJCSSHA256)
	if err != nil || catalog.FactSetRevisionID != fixture.FactSetRevisionID ||
		catalog.SourceScopeRevision != fixture.SourceScopeRevision || !catalog.FactsComplete {
		t.Fatalf("RTW real complete SourceScope/Catalog revision is not fixed: %v", err)
	}
	required := map[string]FactSetFactV1{}
	for _, fact := range catalog.Facts {
		if fact.Required {
			required[fact.FactID] = fact
		}
	}
	if len(required) != 2 {
		t.Fatal("RTW Holder must pin two independently judged required Source Facts")
	}
	target := map[string]bool{}
	for _, id := range fixture.CatalogQualityEventIDs {
		target[id] = true
	}
	judgmentIDs := map[string]bool{}
	for _, id := range fixture.QualityEventIDs {
		raw, sha, err := authority.ReadQualityEvent(ctx, id)
		if err != nil || digest(raw) != sha {
			t.Fatalf("RTW real original single-Fact Judgment missing: event=%s err=%v", id, err)
		}
		var event eventing.Event
		if json.Unmarshal(raw, &event) != nil || event.EventID != id {
			t.Fatalf("RTW actual Judgment Event changed identity: event=%s", id)
		}
		judgment, err := ParseJudgmentV1(event, raw)
		if err != nil {
			t.Fatalf("RTW actual Judgment v1 source differs: event=%s err=%v", id, err)
		}
		if target[id] {
			fact, ok := required[judgment.FactID]
			if !ok || judgmentIDs[judgment.FactID] ||
				judgment.WikiRevisionID != catalog.WikiRevisionID ||
				judgment.SourceRevisionID != fact.SourceRevisionID ||
				judgment.SourceContentSHA256 != fact.SourceContentSHA256 ||
				judgment.SourceQuoteSHA256 != fact.SourceQuoteSHA256 ||
				judgment.SourceByteStart != fact.SourceByteStart ||
				judgment.SourceByteEnd != fact.SourceByteEnd ||
				judgment.SourceQuote != fact.SourceQuote ||
				judgment.WikiContentSHA256 != catalog.WikiContentSHA256 {
				t.Fatalf("quality Event %s does not label its exact Catalog target/source Fact", id)
			}
			judgmentIDs[judgment.FactID] = true
		} else if judgment.WikiRevisionID == catalog.WikiRevisionID {
			t.Fatalf("baseline quality Event %s impersonates Catalog target Wiki", id)
		}
	}
	if len(judgmentIDs) != len(required) {
		t.Fatal("one required Fact lacks independently signed target Judgment")
	}
	consumer := &Consumer{DB: db, Source: dc, Authority: authority,
		Verifier: V1Verifier{}, FactSetAuthority: authority,
		FactSetVerifier: FactSetV1Verifier{}, Consumer: DefaultConsumer}
	first, err := consumer.RunOnce(ctx)
	if err != nil || first.Read != fixture.ExpectedEvents || first.Catalogs != 1 ||
		first.QualityVerified != 3 || first.TechnicalSkips != fixture.ExpectedEvents-4 ||
		first.CommittedOffset != int64(fixture.ExpectedEvents) ||
		first.AcknowledgedOffset != int64(fixture.ExpectedEvents) {
		t.Fatalf("RTW/DC complete shared producer Catalog+Judgments ODS/ACK mismatch: %+v %v", first, err)
	}
	var original, payloadJCS []byte
	var originalSHA, payloadSHA, revisionID, scope string
	var catalogOffset int64
	if err := db.QueryRow(ctx, `SELECT e.source_offset,e.authority_event_json,
 e.authority_event_sha256,e.fact_set_payload_jcs_sha256,
 f.revision_id,f.source_scope_revision,f.payload_jcs
 FROM warehouse_wiki_quality.ods_event e JOIN warehouse_wiki_quality.ods_fact_set f
 ON e.producer=f.producer AND e.source_offset=f.source_offset
 WHERE e.producer=$1 AND e.event_id=$2 AND e.status='fact_set_verified'`,
		Producer, fixture.CatalogEventID).Scan(&catalogOffset, &original, &originalSHA,
		&payloadSHA, &revisionID, &scope, &payloadJCS); err != nil || catalogOffset < 1 ||
		!bytes.Equal(original, proof.EventJSON) || originalSHA != proof.EventRawSHA256 ||
		payloadSHA != proof.FactSetJCSSHA256 || digest(payloadJCS) != payloadSHA ||
		revisionID != fixture.FactSetRevisionID || scope != fixture.SourceScopeRevision {
		t.Fatalf("actual Catalog PG Original/FactSet payload JCS and sidecar lost: %v", err)
	}
	for _, id := range fixture.QualityEventIDs {
		var offset int64
		if err := db.QueryRow(ctx, `SELECT source_offset FROM warehouse_wiki_quality.ods_event
 WHERE producer=$1 AND event_id=$2 AND status='quality_verified'`, Producer, id).Scan(&offset); err != nil || offset < 1 {
			t.Fatalf("actual quality Event %s was not accepted in its real DC position: %v", id, err)
		}
		if target[id] && offset <= catalogOffset || !target[id] && offset >= catalogOffset {
			t.Fatalf("target/baseline quality Event %s has wrong order around Catalog", id)
		}
	}
	replay, err := consumer.RunOnce(ctx)
	if err != nil || replay.Read != 0 {
		t.Fatalf("DC ACKed shared Catalog/Judgment prefix was redelivered: %+v %v", replay, err)
	}
	report, err := json.Marshal(struct {
		Committed              int64    `json:"committed_offset"`
		Acknowledged           int64    `json:"acknowledged_offset"`
		Catalogs               int      `json:"catalogs"`
		Judgments              int      `json:"judgments"`
		TechnicalSkips         int      `json:"technical_skips"`
		CatalogEventID         string   `json:"catalog_event_id"`
		QualityEventIDs        []string `json:"quality_event_ids"`
		CatalogQualityEventIDs []string `json:"catalog_quality_event_ids"`
	}{first.CommittedOffset, first.AcknowledgedOffset, first.Catalogs,
		first.QualityVerified, first.TechnicalSkips, fixture.CatalogEventID,
		fixture.QualityEventIDs, fixture.CatalogQualityEventIDs})
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(fixture.ResultPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("new private BTW FactSet ODS receipt unavailable")
	}
	if _, err := output.Write(report); err != nil {
		output.Close()
		t.Fatal("write private BTW FactSet ODS receipt")
	}
	if err := output.Close(); err != nil {
		t.Fatal("close private BTW FactSet ODS receipt")
	}
}
