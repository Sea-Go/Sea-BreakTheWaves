package sourceproof

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

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/warehouse/wikiqualitysource"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This test-only fixture is separate from the warehouse's original eleven
// keys. The administrator JWT is solely for an explicit historical revision
// GET; it cannot replace the Worker original-Event or DC service bearers.
type realSourceFixture struct {
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

var realSourceFixtureTypes = map[string]byte{
	"rtw_url": '"', "rtw_token": '"', "admin_token": '"',
	"dc_url": '"', "dc_token": '"', "expected_events": 'n',
	"catalog_event_id": '"', "quality_event_ids": '[',
	"catalog_quality_event_ids": '[', "result_path": '"',
	"fact_set_revision_id": '"', "source_scope_revision": '"',
}

func exactRealSourceFixtureLiterals(raw []byte, kinds map[string]byte) error {
	if len(raw) < 2 || len(raw) > 32<<10 {
		return ErrProof
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return ErrProof
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		kind, known := kinds[name]
		if err != nil || !ok || !known || seen[name] {
			return ErrProof
		}
		seen[name] = true
		var literal json.RawMessage
		if decoder.Decode(&literal) != nil {
			return ErrProof
		}
		literal = bytes.TrimSpace(literal)
		if len(literal) == 0 || (kind == 'n' && (literal[0] < '0' || literal[0] > '9')) ||
			(kind != 'n' && literal[0] != kind) {
			return ErrProof
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(seen) != len(kinds) {
		return ErrProof
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrProof
	}
	return nil
}

func decodeRealSourceFixture(raw []byte) (realSourceFixture, error) {
	var fixture realSourceFixture
	if err := exactRealSourceFixtureLiterals(raw, realSourceFixtureTypes); err != nil {
		return fixture, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fixture) != nil || decoder.Decode(new(any)) != io.EOF {
		return realSourceFixture{}, ErrProof
	}
	return fixture, nil
}

func oldFixtureTypes() map[string]byte {
	kinds := make(map[string]byte, len(realSourceFixtureTypes)-1)
	for key, kind := range realSourceFixtureTypes {
		if key != "admin_token" {
			kinds[key] = kind
		}
	}
	return kinds
}

var oldWarehouseResultTypes = map[string]byte{
	"committed_offset": 'n', "acknowledged_offset": 'n',
	"catalogs": 'n', "judgments": 'n', "technical_skips": 'n',
	"catalog_event_id": '"', "quality_event_ids": '[',
	"catalog_quality_event_ids": '[',
}

type oldWarehouseResult struct {
	Committed         int64    `json:"committed_offset"`
	Acknowledged      int64    `json:"acknowledged_offset"`
	Catalogs          int      `json:"catalogs"`
	Judgments         int      `json:"judgments"`
	TechnicalSkips    int      `json:"technical_skips"`
	CatalogEventID    string   `json:"catalog_event_id"`
	QualityEventIDs   []string `json:"quality_event_ids"`
	CatalogQualityIDs []string `json:"catalog_quality_event_ids"`
}

func verifyOldWarehouseResult(raw []byte, fixture realSourceFixture) error {
	if err := exactRealSourceFixtureLiterals(raw, oldWarehouseResultTypes); err != nil {
		return err
	}
	var report oldWarehouseResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&report) != nil || decoder.Decode(new(any)) != io.EOF ||
		report.Committed != int64(fixture.ExpectedEvents) ||
		report.Acknowledged != report.Committed ||
		report.Catalogs != 1 || report.Judgments != 3 ||
		report.TechnicalSkips != fixture.ExpectedEvents-4 ||
		report.CatalogEventID != fixture.CatalogEventID ||
		!slices.Equal(report.QualityEventIDs, fixture.QualityEventIDs) ||
		!slices.Equal(report.CatalogQualityIDs, fixture.CatalogQualityEventIDs) {
		return ErrProof
	}
	return nil
}

func realSourceLoopback(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil || parsed.Scheme != "http" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return false
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	ip := net.ParseIP(host)
	number, parseErr := strconv.Atoi(port)
	return err == nil && parseErr == nil && number > 0 && number <= 65535 &&
		ip != nil && ip.IsLoopback()
}

func ordinaryPrivate0600(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600
}

func readRealSourceFixture(t *testing.T, path string) realSourceFixture {
	t.Helper()
	if !filepath.IsAbs(path) {
		t.Fatal("real SourceProof fixture requires an absolute task-owned path")
	}
	if !ordinaryPrivate0600(path) {
		t.Fatal("real SourceProof fixture requires an ordinary private 0600 file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("real SourceProof fixture could not be read")
	}
	fixture, err := decodeRealSourceFixture(raw)
	if err != nil || !realSourceLoopback(fixture.RTWURL) ||
		!realSourceLoopback(fixture.DCURL) || fixture.RTWToken == "" ||
		fixture.AdminToken == "" || fixture.DCToken == "" ||
		strings.TrimSpace(fixture.RTWToken) != fixture.RTWToken ||
		strings.TrimSpace(fixture.AdminToken) != fixture.AdminToken ||
		strings.TrimSpace(fixture.DCToken) != fixture.DCToken ||
		strings.ContainsAny(fixture.RTWToken+fixture.AdminToken+fixture.DCToken, "\r\n") ||
		fixture.ExpectedEvents < 5 || fixture.ExpectedEvents > 128 ||
		!idPattern.MatchString(fixture.CatalogEventID) ||
		!idPattern.MatchString(fixture.FactSetRevisionID) ||
		!strings.HasPrefix(fixture.SourceScopeRevision, "scope_") ||
		!validSHA(strings.TrimPrefix(fixture.SourceScopeRevision, "scope_")) ||
		len(fixture.QualityEventIDs) != 3 ||
		len(fixture.CatalogQualityEventIDs) != 2 ||
		!filepath.IsAbs(fixture.ResultPath) ||
		filepath.Dir(fixture.ResultPath) != filepath.Dir(path) {
		t.Fatal("real SourceProof fixture has incomplete or substituted literal handoff")
	}
	all := map[string]bool{}
	for _, id := range fixture.QualityEventIDs {
		if !idPattern.MatchString(id) || id == fixture.CatalogEventID || all[id] {
			t.Fatal("real SourceProof judgment EventID repeats another Event")
		}
		all[id] = true
	}
	target := map[string]bool{}
	for _, id := range fixture.CatalogQualityEventIDs {
		if !all[id] || target[id] {
			t.Fatal("two independently judged Catalog EventIDs are absent")
		}
		target[id] = true
	}
	return fixture
}

func readSameParentWarehouseFixture(t *testing.T, path string,
	newFixture realSourceFixture) {
	t.Helper()
	if !filepath.IsAbs(path) || path == newFixture.ResultPath ||
		filepath.Dir(path) != filepath.Dir(newFixture.ResultPath) {
		t.Fatal("old warehouse and SourceProof fixtures require one private parent directory")
	}
	if !ordinaryPrivate0600(path) {
		t.Fatal("old eleven-key warehouse fixture is not an ordinary private file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || exactRealSourceFixtureLiterals(raw, oldFixtureTypes()) != nil {
		t.Fatal("old warehouse fixture no longer has its exact eleven literal keys")
	}
	var old realSourceFixture
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&old) != nil || decoder.Decode(new(any)) != io.EOF ||
		old.AdminToken != "" || old.RTWURL != newFixture.RTWURL ||
		old.RTWToken != newFixture.RTWToken || old.DCURL != newFixture.DCURL ||
		old.DCToken != newFixture.DCToken ||
		old.ExpectedEvents != newFixture.ExpectedEvents ||
		old.CatalogEventID != newFixture.CatalogEventID ||
		old.FactSetRevisionID != newFixture.FactSetRevisionID ||
		old.SourceScopeRevision != newFixture.SourceScopeRevision ||
		!slices.Equal(old.QualityEventIDs, newFixture.QualityEventIDs) ||
		!slices.Equal(old.CatalogQualityEventIDs, newFixture.CatalogQualityEventIDs) ||
		old.ResultPath == newFixture.ResultPath ||
		!filepath.IsAbs(old.ResultPath) ||
		filepath.Dir(old.ResultPath) != filepath.Dir(path) {
		t.Fatal("warehouse source and SourceProof reader fixtures do not name the same parent")
	}
	if !ordinaryPrivate0600(old.ResultPath) {
		t.Fatal("preceding warehouse source test did not write its private receipt")
	}
	reportRaw, err := os.ReadFile(old.ResultPath)
	if err != nil || len(reportRaw) < 2 || len(reportRaw) > 32<<10 {
		t.Fatal("preceding warehouse source receipt unavailable")
	}
	if verifyOldWarehouseResult(reportRaw, newFixture) != nil {
		t.Fatal("preceding warehouse source receipt disagrees with same-parent cutoff")
	}
}

func TestRealSourceFixtureRejectsDuplicateOrInventedTenant(t *testing.T) {
	fixture := realSourceFixture{RTWURL: "http://127.0.0.1:1", RTWToken: "worker",
		AdminToken: "admin", DCURL: "http://127.0.0.1:2", DCToken: "service",
		ExpectedEvents: 5, CatalogEventID: "catalog-1",
		QualityEventIDs:        []string{"quality-1", "quality-2", "quality-3"},
		CatalogQualityEventIDs: []string{"quality-2", "quality-3"},
		ResultPath:             "/tmp/private-result", FactSetRevisionID: "fact-set-1",
		SourceScopeRevision: "scope_" + digest([]byte("scope"))}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeRealSourceFixture(raw); err != nil {
		t.Fatalf("valid twelve-key test-only fixture rejected: %v", err)
	}
	duplicate := bytes.Replace(raw, []byte(`"admin_token":"admin"`),
		[]byte(`"admin_token":"admin","admin_token":"admin"`), 1)
	if bytes.Equal(raw, duplicate) {
		t.Fatal("duplicate fixture probe could not be constructed")
	}
	unknown := bytes.Replace(raw, []byte(`}`), []byte(`,"tenant_id":"invented"}`), 1)
	for name, candidate := range map[string][]byte{"duplicate_admin": duplicate,
		"invented_tenant": unknown, "missing_admin": bytes.Replace(raw,
			[]byte(`"admin_token":"admin",`), nil, 1)} {
		if _, err := decodeRealSourceFixture(candidate); !errors.Is(err, ErrProof) {
			t.Fatalf("%s crossed test-only fixture contract: %v", name, err)
		}
	}
}

func TestPrivateSourceFixtureAndReceiptRequireExact0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-source.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0600, 0400, 0700} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if ordinaryPrivate0600(path) != (mode == 0600) {
			t.Fatalf("private fixture/receipt accepted wrong exact mode %04o", mode)
		}
	}
}

func TestOldWarehouseReceiptRejectsDuplicateUnknownAndPartialQualityIDs(t *testing.T) {
	fixture := realSourceFixture{ExpectedEvents: 14, CatalogEventID: "catalog-1",
		QualityEventIDs:        []string{"quality-1", "quality-2", "quality-3"},
		CatalogQualityEventIDs: []string{"quality-2", "quality-3"}}
	report := oldWarehouseResult{Committed: 14, Acknowledged: 14,
		Catalogs: 1, Judgments: 3, TechnicalSkips: 10,
		CatalogEventID:    "catalog-1",
		QualityEventIDs:   []string{"quality-1", "quality-2", "quality-3"},
		CatalogQualityIDs: []string{"quality-2", "quality-3"}}
	raw, err := json.Marshal(report)
	if err != nil || verifyOldWarehouseResult(raw, fixture) != nil {
		t.Fatalf("exact old eight-key result rejected: %v", err)
	}
	duplicate := bytes.Replace(raw, []byte(`"catalog_event_id":"catalog-1"`),
		[]byte(`"catalog_event_id":"catalog-1","catalog_event_id":"catalog-1"`), 1)
	unknown := bytes.Replace(raw, []byte(`}`), []byte(`,"tenant_id":"invented"}`), 1)
	if bytes.Equal(duplicate, raw) || bytes.Equal(unknown, raw) {
		t.Fatal("old receipt literal negative probes could not be constructed")
	}
	for name, bad := range map[string][]byte{
		"duplicate_catalog_id": duplicate,
		"unknown_tenant":       unknown,
	} {
		if err := verifyOldWarehouseResult(bad, fixture); !errors.Is(err, ErrProof) {
			t.Fatalf("%s crossed old result contract: %v", name, err)
		}
	}
	for name, corrupt := range map[string]func(*oldWarehouseResult){
		"missing_baseline_quality": func(r *oldWarehouseResult) {
			r.QualityEventIDs = []string{"quality-2", "quality-3"}
		},
		"repeated_target_quality": func(r *oldWarehouseResult) {
			r.CatalogQualityIDs = []string{"quality-2", "quality-2"}
		},
		"substituted_target_quality": func(r *oldWarehouseResult) {
			r.CatalogQualityIDs = []string{"quality-1", "quality-3"}
		},
		"ack_less_than_cutoff": func(r *oldWarehouseResult) {
			r.Acknowledged = 13
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := report
			corrupt(&bad)
			encoded, err := json.Marshal(bad)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyOldWarehouseResult(encoded, fixture); !errors.Is(err, ErrProof) {
				t.Fatalf("old result claimed complete proof from partial EventIDs: %v", err)
			}
		})
	}
}

// The preceding warehouse test writes the actual RTW/DC events to the very
// same disposable PG. This reader is read-only; it must not initialize,
// truncate, advance ACK, or invent human/as-of judgment qualifications.
func TestRTWRealFactSetAcknowledgedSource(t *testing.T) {
	path := os.Getenv("SEA_BTW_SOURCEPROOF_REAL_FIXTURE")
	if path == "" {
		t.Skip("RTW Holder must explicitly supply private Admin/Worker/DC source fixture")
	}
	dsn := os.Getenv("WIKI_QUALITY_SOURCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("run through real-source-acceptance.sh for same-parent disposable ODS PG")
	}
	fixture := readRealSourceFixture(t, path)
	readSameParentWarehouseFixture(t,
		os.Getenv("SEA_RTW_REAL_WIKI_FACT_SET_FIXTURE"), fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := wikiqualitysource.CheckFactSetSchema(ctx, db); err != nil {
		t.Fatalf("real FactSet ODS schema v2 not installed by warehouse source: %v", err)
	}
	rtw, err := NewRTWHTTPReader(fixture.RTWURL, fixture.RTWToken,
		fixture.AdminToken)
	if err != nil {
		t.Fatal("real RTW Worker/Admin read surfaces not configured")
	}
	proof, err := rtw.ReadFactSetEvent(ctx, fixture.CatalogEventID)
	if err != nil {
		t.Fatalf("real RTW original Catalog Event cannot be read: %v", err)
	}
	var event eventing.Event
	if json.Unmarshal(proof.EventJSON, &event) != nil {
		t.Fatal("real RTW original Catalog Event is not typed")
	}
	frozen, err := wikiqualitysource.ParseFactSetV1(event, proof.EventJSON,
		proof.FactSetJCSSHA256)
	if err != nil || frozen.FactSetRevisionID != fixture.FactSetRevisionID ||
		frozen.SourceScopeRevision != fixture.SourceScopeRevision ||
		len(frozen.SourceRevisions) != 2 || len(frozen.Facts) != 2 {
		t.Fatalf("Holder's explicit two-source historical FactSet differs: %v", err)
	}
	ods := PGODSReader{DB: db}
	snapshot, err := ods.ReadPrefix(ctx, int64(fixture.ExpectedEvents))
	if err != nil {
		t.Fatalf("real ODS committed complete prefix unavailable: %v", err)
	}
	ids := map[string]int64{}
	for _, row := range snapshot.Rows {
		ids[row.EventID] = row.Offset
	}
	baseline := ""
	for _, id := range fixture.QualityEventIDs {
		if ids[id] < 1 {
			t.Fatalf("real ODS prefix omitted quality Event %s", id)
		}
		found := false
		for _, target := range fixture.CatalogQualityEventIDs {
			found = found || target == id
		}
		if !found {
			baseline = id
		}
	}
	if ids[fixture.CatalogEventID] < 1 || baseline == "" ||
		ids[baseline] >= ids[fixture.CatalogEventID] ||
		ids[fixture.CatalogQualityEventIDs[0]] <= ids[fixture.CatalogEventID] ||
		ids[fixture.CatalogQualityEventIDs[1]] <= ids[fixture.CatalogEventID] {
		t.Fatal("real baseline/Catalog/two target judgments are not in DC order")
	}
	dc, err := NewHTTPDCAckReader(fixture.DCURL, fixture.DCToken)
	if err != nil {
		t.Fatal("real DC acknowledged-prefix read surface not configured")
	}
	reader := AuthorityReader{ODS: ods, RTW: rtw}
	ack, err := reader.ReadAcknowledgedPinnedSource(ctx, PinnedRequest{
		ModuleID: frozen.ModuleID, PageID: frozen.PageID,
		WikiRevisionID:      frozen.WikiRevisionID,
		SourceScopeRevision: frozen.SourceScopeRevision,
		FactSetRevisionID:   frozen.FactSetRevisionID,
		CutoffOffset:        int64(fixture.ExpectedEvents)}, dc)
	if err != nil {
		t.Fatalf("real RTW historical/Worker, ODS PG and DC ACK prefix disagree: %v", err)
	}
	selected := map[string]bool{}
	for _, item := range ack.Selected {
		selected[item.EventID] = true
	}
	if ack.AcknowledgedAtLeast < int64(fixture.ExpectedEvents) ||
		ack.CommittedOffset < int64(fixture.ExpectedEvents) ||
		ack.CutoffOffset != int64(fixture.ExpectedEvents) ||
		ack.Catalog.Event.EventID != fixture.CatalogEventID ||
		ack.Catalog.FactSetRevisionID != fixture.FactSetRevisionID ||
		ack.Catalog.SourceScopeRevision != fixture.SourceScopeRevision ||
		ack.Catalog.Event.RawSHA256 != proof.EventRawSHA256 ||
		ack.Catalog.Event.JCSSHA256 != proof.EventJCSSHA256 ||
		ack.Catalog.FactSetPayloadJCSSHA256 != proof.FactSetJCSSHA256 ||
		!ack.Catalog.FactsCompleteDeclared || len(ack.Catalog.Sources) != 2 ||
		len(ack.Transported) != 2 || len(ack.Selected) != 3 ||
		len(ack.DCBatchReceipts) < 1 ||
		!validSHA(ack.ODSPrefixSHA256) ||
		!validSHA(ack.ODSEvidenceSHA256) ||
		!validSHA(ack.DCIndexSHA256) ||
		selected[baseline] || !selected[fixture.CatalogEventID] {
		t.Fatalf("same-parent complete Catalog/ACK source projection is incomplete: %+v", ack)
	}
	for _, id := range fixture.CatalogQualityEventIDs {
		if !selected[id] {
			t.Fatalf("independent target Fact quality Event %s missing from source selection", id)
		}
	}
	for _, judged := range ack.Transported {
		if judged.WikiRevisionID != frozen.WikiRevisionID ||
			judged.SourceScopeRevision != frozen.SourceScopeRevision ||
			!selected[judged.Event.EventID] {
			t.Fatalf("latest transported judgment did not name pinned source: %+v", judged)
		}
	}
	result, err := json.Marshal(struct {
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
	}{SchemaVersion: "sea.wiki.fact-set-sourceproof-real.v1",
		CatalogEventID:       fixture.CatalogEventID,
		FactSetRevisionID:    fixture.FactSetRevisionID,
		SourceScopeRevision:  fixture.SourceScopeRevision,
		WikiRevisionID:       frozen.WikiRevisionID,
		CutoffOffset:         ack.CutoffOffset,
		CommittedOffset:      ack.CommittedOffset,
		AcknowledgedAtLeast:  ack.AcknowledgedAtLeast,
		RequiredSourceCount:  len(ack.Catalog.Sources),
		TransportedJudgments: len(ack.Transported),
		SelectedEventIDs: []string{fixture.CatalogEventID,
			fixture.CatalogQualityEventIDs[0], fixture.CatalogQualityEventIDs[1]},
		ODSPrefixSHA256:     ack.ODSPrefixSHA256,
		ODSEvidenceSHA256:   ack.ODSEvidenceSHA256,
		DCIndexSHA256:       ack.DCIndexSHA256,
		DCBatchReceiptCount: len(ack.DCBatchReceipts),
		QualityState:        NotEvaluable})
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(fixture.ResultPath,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("real SourceProof private result file must be new")
	}
	if _, err := output.Write(result); err != nil {
		output.Close()
		t.Fatal("real SourceProof private result write failed")
	}
	if err := output.Close(); err != nil {
		t.Fatal("real SourceProof private result close failed")
	}
}
