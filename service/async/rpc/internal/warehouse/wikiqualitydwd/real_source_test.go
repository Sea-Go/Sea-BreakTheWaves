package wikiqualitydwd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/evaluation/wiki_quality/sourceproof"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/warehouse/wikiqualitysource"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The preceding two packages own these fixtures and receipts. The DWD bridge
// re-reads their exact literal envelopes, but does not change either contract.
type dwdRealFixture struct {
	RTWURL                 string   `json:"rtw_url"`
	RTWToken               string   `json:"rtw_token"`
	AdminToken             string   `json:"admin_token"`
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

type dwdWarehouseReceipt struct {
	Committed              int64    `json:"committed_offset"`
	Acknowledged           int64    `json:"acknowledged_offset"`
	Catalogs               int      `json:"catalogs"`
	Judgments              int      `json:"judgments"`
	TechnicalSkips         int      `json:"technical_skips"`
	CatalogEventID         string   `json:"catalog_event_id"`
	QualityEventIDs        []string `json:"quality_event_ids"`
	CatalogQualityEventIDs []string `json:"catalog_quality_event_ids"`
}

type dwdReaderReceipt struct {
	SchemaVersion        string   `json:"schema_version"`
	CatalogEventID       string   `json:"catalog_event_id"`
	FactSetRevisionID    string   `json:"fact_set_revision_id"`
	SourceScopeRevision  string   `json:"source_scope_revision"`
	WikiRevisionID       string   `json:"wiki_revision_id"`
	CutoffOffset         int64    `json:"cutoff_offset"`
	CommittedOffset      int64    `json:"committed_offset"`
	AcknowledgedAtLeast  int64    `json:"acknowledged_at_least"`
	RequiredSourceCount  int      `json:"required_source_count"`
	TransportedJudgments int      `json:"transported_judgments"`
	SelectedEventIDs     []string `json:"selected_event_ids"`
	ODSPrefixSHA256      string   `json:"ods_prefix_sha256"`
	ODSEvidenceSHA256    string   `json:"ods_evidence_sha256"`
	DCIndexSHA256        string   `json:"dc_index_sha256"`
	DCBatchReceiptCount  int      `json:"dc_batch_receipt_count"`
	QualityState         string   `json:"quality_state"`
	HumanCatalogVerified bool     `json:"human_catalog_verified"`
	D07Evaluable         bool     `json:"d07_evaluable"`
	ProductionVerified   bool     `json:"production_verified"`
}

var dwdRealFixtureKeys = map[string]byte{
	"rtw_url": '"', "rtw_token": '"', "admin_token": '"',
	"dc_url": '"', "dc_token": '"', "expected_events": 'n',
	"catalog_event_id": '"', "quality_event_ids": '[',
	"catalog_quality_event_ids": '[', "result_path": '"',
	"fact_set_revision_id": '"', "source_scope_revision": '"',
}

var dwdWarehouseReceiptKeys = map[string]byte{
	"committed_offset": 'n', "acknowledged_offset": 'n',
	"catalogs": 'n', "judgments": 'n', "technical_skips": 'n',
	"catalog_event_id": '"', "quality_event_ids": '[',
	"catalog_quality_event_ids": '[',
}

var dwdReaderReceiptKeys = map[string]byte{
	"schema_version": '"', "catalog_event_id": '"',
	"fact_set_revision_id": '"', "source_scope_revision": '"',
	"wiki_revision_id": '"', "cutoff_offset": 'n',
	"committed_offset": 'n', "acknowledged_at_least": 'n',
	"required_source_count": 'n', "transported_judgments": 'n',
	"selected_event_ids": '[', "ods_prefix_sha256": '"',
	"ods_evidence_sha256": '"', "dc_index_sha256": '"',
	"dc_batch_receipt_count": 'n', "quality_state": '"',
	"human_catalog_verified": 'b', "d07_evaluable": 'b',
	"production_verified": 'b',
}

func dwdOldFixtureKeys() map[string]byte {
	keys := make(map[string]byte, len(dwdRealFixtureKeys)-1)
	for key, kind := range dwdRealFixtureKeys {
		if key != "admin_token" {
			keys[key] = kind
		}
	}
	return keys
}

func dwdExactObject(raw []byte, keys map[string]byte) error {
	if len(raw) < 2 || len(raw) > 32<<10 {
		return ErrSource
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return ErrSource
	}
	seen := map[string]bool{}
	for decoder.More() {
		name, err := decoder.Token()
		key, ok := name.(string)
		kind, known := keys[key]
		if err != nil || !ok || !known || seen[key] {
			return ErrSource
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return ErrSource
		}
		value = bytes.TrimSpace(value)
		if len(value) == 0 || (kind == 'n' && (value[0] < '0' || value[0] > '9')) ||
			(kind == 'b' && value[0] != 't' && value[0] != 'f') ||
			(kind != 'n' && kind != 'b' && value[0] != kind) {
			return ErrSource
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(seen) != len(keys) {
		return ErrSource
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrSource
	}
	return nil
}

func dwdPrivateFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrSource
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, ErrSource
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 32<<10 {
		return nil, ErrSource
	}
	return raw, nil
}

func dwdReadPrivateExact[T any](path string, keys map[string]byte) (T, error) {
	var out T
	raw, err := dwdPrivateFile(path)
	if err != nil || dwdExactObject(raw, keys) != nil {
		return out, ErrSource
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF {
		return out, ErrSource
	}
	return out, nil
}

func dwdLoopback(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil || parsed.Scheme != "http" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return false
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	ip := net.ParseIP(host)
	number, numberErr := strconv.Atoi(port)
	return err == nil && numberErr == nil && number > 0 && number <= 65535 &&
		ip != nil && ip.IsLoopback()
}

func dwdSameParentFixture(t *testing.T) (dwdRealFixture, dwdReaderReceipt) {
	t.Helper()
	path := os.Getenv("SEA_BTW_SOURCEPROOF_REAL_FIXTURE")
	oldPath := os.Getenv("SEA_RTW_REAL_WIKI_FACT_SET_FIXTURE")
	fixture, err := dwdReadPrivateExact[dwdRealFixture](path, dwdRealFixtureKeys)
	if err != nil || !dwdLoopback(fixture.RTWURL) || !dwdLoopback(fixture.DCURL) ||
		fixture.RTWToken == "" || fixture.AdminToken == "" || fixture.DCToken == "" ||
		strings.ContainsAny(fixture.RTWToken+fixture.AdminToken+fixture.DCToken, "\r\n") ||
		fixture.ExpectedEvents != 14 || len(fixture.QualityEventIDs) != 3 ||
		len(fixture.CatalogQualityEventIDs) != 2 ||
		!filepath.IsAbs(fixture.ResultPath) ||
		filepath.Dir(fixture.ResultPath) != filepath.Dir(path) {
		t.Fatal("DWD real fixture is not the preceding strict twelve-key ACK14 source")
	}
	old, err := dwdReadPrivateExact[dwdRealFixture](oldPath, dwdOldFixtureKeys())
	if err != nil || old.AdminToken != "" ||
		filepath.Dir(oldPath) != filepath.Dir(path) ||
		old.RTWURL != fixture.RTWURL || old.RTWToken != fixture.RTWToken ||
		old.DCURL != fixture.DCURL || old.DCToken != fixture.DCToken ||
		old.ExpectedEvents != fixture.ExpectedEvents ||
		old.CatalogEventID != fixture.CatalogEventID ||
		old.FactSetRevisionID != fixture.FactSetRevisionID ||
		old.SourceScopeRevision != fixture.SourceScopeRevision ||
		!slices.Equal(old.QualityEventIDs, fixture.QualityEventIDs) ||
		!slices.Equal(old.CatalogQualityEventIDs, fixture.CatalogQualityEventIDs) ||
		!filepath.IsAbs(old.ResultPath) ||
		filepath.Dir(old.ResultPath) != filepath.Dir(path) ||
		old.ResultPath == fixture.ResultPath {
		t.Fatal("DWD fixture cannot identify the exact old warehouse source")
	}
	warehouse, err := dwdReadPrivateExact[dwdWarehouseReceipt](
		old.ResultPath, dwdWarehouseReceiptKeys)
	if err != nil || warehouse.Committed != 14 || warehouse.Acknowledged != 14 ||
		warehouse.Catalogs != 1 || warehouse.Judgments != 3 ||
		warehouse.TechnicalSkips != 10 ||
		warehouse.CatalogEventID != fixture.CatalogEventID ||
		!slices.Equal(warehouse.QualityEventIDs, fixture.QualityEventIDs) ||
		!slices.Equal(warehouse.CatalogQualityEventIDs, fixture.CatalogQualityEventIDs) {
		t.Fatal("preceding warehouse ODS/ACK receipt is missing or contradicted")
	}
	reader, err := dwdReadPrivateExact[dwdReaderReceipt](
		fixture.ResultPath, dwdReaderReceiptKeys)
	if err != nil || reader.SchemaVersion != "sea.wiki.fact-set-sourceproof-real.v1" ||
		reader.CatalogEventID != fixture.CatalogEventID ||
		reader.FactSetRevisionID != fixture.FactSetRevisionID ||
		reader.SourceScopeRevision != fixture.SourceScopeRevision ||
		reader.CutoffOffset != 14 || reader.CommittedOffset != 14 ||
		reader.AcknowledgedAtLeast < 14 || reader.RequiredSourceCount != 2 ||
		reader.TransportedJudgments != 2 || len(reader.SelectedEventIDs) != 3 ||
		reader.DCBatchReceiptCount < 1 ||
		len(reader.ODSPrefixSHA256) != 64 || len(reader.ODSEvidenceSHA256) != 64 ||
		len(reader.DCIndexSHA256) != 64 || reader.QualityState != NotEvaluable ||
		reader.HumanCatalogVerified || reader.D07Evaluable || reader.ProductionVerified {
		t.Fatal("preceding historical/Worker/DC SourceProof receipt did not qualify ACK14")
	}
	selected := map[string]bool{}
	for _, id := range reader.SelectedEventIDs {
		if selected[id] {
			t.Fatal("preceding SourceProof selection repeated an Event")
		}
		selected[id] = true
	}
	if !selected[fixture.CatalogEventID] ||
		!selected[fixture.CatalogQualityEventIDs[0]] ||
		!selected[fixture.CatalogQualityEventIDs[1]] {
		t.Fatal("preceding SourceProof selection omitted a required Catalog/Judgment")
	}
	return fixture, reader
}

func dwdCallerOwnedDirectory(t *testing.T, path string) {
	t.Helper()
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatal("DWD source bundle requires an absolute caller-owned directory")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("DWD source bundle requires a fresh ordinary private 0700 directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatal("DWD source bundle requires a precreated empty directory")
	}
}

func TestDWDRealFixtureRejectsDuplicateAndUnknownLiterals(t *testing.T) {
	fixture := dwdRealFixture{RTWURL: "http://127.0.0.1:1", RTWToken: "worker",
		AdminToken: "admin", DCURL: "http://127.0.0.1:2", DCToken: "service",
		ExpectedEvents: 14, CatalogEventID: "catalog-1",
		QualityEventIDs:        []string{"quality-1", "quality-2", "quality-3"},
		CatalogQualityEventIDs: []string{"quality-2", "quality-3"},
		ResultPath:             "/tmp/reader-result", FactSetRevisionID: "fact-set-1",
		SourceScopeRevision: "scope_" + strings.Repeat("a", 64)}
	raw, err := json.Marshal(fixture)
	if err != nil || dwdExactObject(raw, dwdRealFixtureKeys) != nil {
		t.Fatalf("exact preceding twelve-key fixture rejected: %v", err)
	}
	for name, bad := range map[string][]byte{
		"duplicate_admin": bytes.Replace(raw, []byte(`"admin_token":"admin"`),
			[]byte(`"admin_token":"admin","admin_token":"admin"`), 1),
		"invented_tenant": bytes.Replace(raw, []byte(`}`),
			[]byte(`,"tenant_id":"invented"}`), 1),
		"missing_admin": bytes.Replace(raw, []byte(`"admin_token":"admin",`),
			nil, 1),
	} {
		if bytes.Equal(raw, bad) || !errors.Is(dwdExactObject(bad,
			dwdRealFixtureKeys), ErrSource) {
			t.Fatalf("%s crossed DWD same-parent fixture contract", name)
		}
	}
}

// The preceding two real-source packages must leave their private receipts
// and the exact PG cursor live. This third package only READS that PG and the
// RTW/Admin/Worker/DC historical surfaces, then writes four caller-owned
// original evidence files. It cannot mark a human review or D07 evaluable.
func TestRTWRealFactSetDWDSourceBundle(t *testing.T) {
	directory := os.Getenv("SEA_BTW_WIKI_DWD_EVIDENCE_DIR")
	if directory == "" {
		t.Skip("RTW Holder must explicitly precreate a persistent DWD source directory")
	}
	dsn := os.Getenv("WIKI_QUALITY_SOURCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("run the third source bridge inside real-source-acceptance.sh same-PG parent")
	}
	dwdCallerOwnedDirectory(t, directory)
	fixture, receipt := dwdSameParentFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("read-only DWD ODS PG is unavailable")
	}
	defer db.Close()
	if err := wikiqualitysource.CheckFactSetSchema(ctx, db); err != nil {
		t.Fatalf("same-parent FactSet ODS schema v2 missing: %v", err)
	}
	rtw, err := sourceproof.NewRTWHTTPReader(fixture.RTWURL,
		fixture.RTWToken, fixture.AdminToken)
	if err != nil {
		t.Fatal("DWD RTW historical/Worker source not configured")
	}
	original, err := rtw.ReadFactSetEvent(ctx, fixture.CatalogEventID)
	if err != nil {
		t.Fatalf("DWD cannot read actual RTW Catalog original: %v", err)
	}
	var event eventing.Event
	if json.Unmarshal(original.EventJSON, &event) != nil {
		t.Fatal("DWD Catalog original is not a typed Event")
	}
	catalog, err := wikiqualitysource.ParseFactSetV1(event, original.EventJSON,
		original.FactSetJCSSHA256)
	if err != nil || catalog.FactSetRevisionID != fixture.FactSetRevisionID ||
		catalog.SourceScopeRevision != fixture.SourceScopeRevision ||
		catalog.WikiRevisionID != receipt.WikiRevisionID ||
		len(catalog.SourceRevisions) != 2 || len(catalog.Facts) != 2 ||
		!catalog.FactsComplete {
		t.Fatalf("DWD original frozen Catalog differs from reader's source: %v", err)
	}
	for _, fact := range catalog.Facts {
		if !fact.Required {
			t.Fatal("DWD real two-Fact source is not the required Catalog inventory")
		}
	}
	dc, err := sourceproof.NewHTTPDCAckReader(fixture.DCURL, fixture.DCToken)
	if err != nil {
		t.Fatal("DWD real DC ACK reader not configured")
	}
	reader := sourceproof.AuthorityReader{ODS: sourceproof.PGODSReader{DB: db},
		RTW: rtw}
	req := sourceproof.PinnedRequest{ModuleID: catalog.ModuleID,
		PageID: catalog.PageID, WikiRevisionID: catalog.WikiRevisionID,
		FactSetRevisionID:   catalog.FactSetRevisionID,
		SourceScopeRevision: catalog.SourceScopeRevision, CutoffOffset: 14}
	frozen, err := Freeze(ctx, reader, req, dc)
	if err != nil {
		t.Fatalf("DWD true RTW/Admin/Worker/DC/ODS ACK14 Freeze failed: %v", err)
	}
	manifest := frozen.Manifest
	if manifest.Schema != "sea.wiki.fact-set-offline-source.v1" ||
		manifest.Producer != sourceproof.Producer ||
		manifest.Consumer != wikiqualitysource.DefaultConsumer ||
		manifest.ModuleID != req.ModuleID || manifest.PageID != req.PageID ||
		manifest.WikiRevisionID != req.WikiRevisionID ||
		manifest.FactSetRevisionID != req.FactSetRevisionID ||
		manifest.SourceScopeRevision != req.SourceScopeRevision ||
		manifest.CutoffOffset != 14 || manifest.AcknowledgedAtLeast < 14 ||
		manifest.CommittedAtLeast < 14 || manifest.PrefixRows != 14 ||
		manifest.CatalogFacts != 2 || manifest.JudgmentRevisions != 2 ||
		manifest.TechnicalSkips != 10 || manifest.TransportedFactCount != 2 ||
		manifest.ODSEvidenceSHA256 != receipt.ODSEvidenceSHA256 ||
		manifest.DCIndexSHA256 != receipt.DCIndexSHA256 ||
		manifest.EvidenceLevel != SourceOnly ||
		manifest.QualityState != NotEvaluable || manifest.Activation != "none" ||
		len(frozen.PrefixRows) != 14 || len(frozen.CatalogFacts) != 2 ||
		len(frozen.Judgments) != 2 || len(frozen.ManifestSHA) != 64 {
		t.Fatalf("DWD ACK14 offline manifest promoted or lost provenance: %+v", manifest)
	}
	for i, row := range frozen.PrefixRows {
		if row.SourceOffset != int64(i)+1 ||
			row.AcknowledgedCutoffOffset != 14 ||
			row.ODSEvidenceSHA256 != receipt.ODSEvidenceSHA256 ||
			row.DCIndexSHA256 != receipt.DCIndexSHA256 {
			t.Fatal("DWD complete prefix row differs from same-parent source receipts")
		}
	}
	for _, row := range frozen.CatalogFacts {
		if row.CatalogEventID != fixture.CatalogEventID ||
			row.FactSetRevisionID != fixture.FactSetRevisionID ||
			row.SourceScopeRevision != fixture.SourceScopeRevision ||
			row.CatalogEventRawSHA256 != original.EventRawSHA256 ||
			row.CatalogEventJCSSHA256 != original.EventJCSSHA256 ||
			row.FactSetPayloadJCSSHA256 != original.FactSetJCSSHA256 ||
			!row.Required || !row.FactsCompleteDeclared ||
			row.QualityState != NotEvaluable {
			t.Fatal("DWD FactSet row lost original event/declared-only provenance")
		}
	}
	judged := map[string]bool{}
	for _, row := range frozen.Judgments {
		if row.FactSetRevisionID != fixture.FactSetRevisionID ||
			row.SourceScopeRevision != fixture.SourceScopeRevision ||
			row.QualityState != NotEvaluable {
			t.Fatal("DWD transported Admin judgment was promoted beyond source-only")
		}
		judged[row.EventID] = true
	}
	if len(judged) != 2 || !judged[fixture.CatalogQualityEventIDs[0]] ||
		!judged[fixture.CatalogQualityEventIDs[1]] {
		t.Fatal("DWD omitted one of two independent Catalog judgment events")
	}
	if err := frozen.Write(directory); err != nil {
		t.Fatalf("caller-owned four-file DWD source bundle failed: %v", err)
	}
	artifacts := map[string][]byte{
		"prefix.jsonl":        frozen.PrefixJSONL,
		"catalog-facts.jsonl": frozen.CatalogJSONL,
		"judgments.jsonl":     frozen.JudgmentJSONL,
		"manifest.json":       frozen.ManifestJCS,
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != len(artifacts) {
		t.Fatal("DWD source directory omitted or invented a bundle file")
	}
	for name, expected := range artifacts {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatalf("DWD source bundle file %s is not ordinary 0600", name)
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, expected) || hash(actual) != hash(expected) {
			t.Fatalf("DWD source bundle file %s is not exact original 0600 bytes", name)
		}
	}
	if hash(frozen.ManifestJCS) != frozen.ManifestSHA {
		t.Fatal("DWD source manifest SHA differs from its original bytes")
	}
	t.Logf("real DWD source-only ACK14 persisted four files; manifest_sha256=%s", frozen.ManifestSHA)
}
