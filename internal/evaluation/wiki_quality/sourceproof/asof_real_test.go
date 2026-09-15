package sourceproof

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/warehouse/wikiqualitysource"
	"github.com/jackc/pgx/v5/pgxpool"
)

type realDCCurrentReceipt struct {
	SchemaVersion               string   `json:"schema_version"`
	ModuleID                    string   `json:"module_id"`
	FactSetRevisionID           string   `json:"fact_set_revision_id"`
	WikiRevisionID              string   `json:"wiki_revision_id"`
	SourceScopeRevision         string   `json:"source_scope_revision"`
	SourceVersion               int64    `json:"source_version"`
	DCCutoffOffset              int64    `json:"dc_cutoff_offset"`
	RTWCandidateJCSSHA256       string   `json:"rtw_candidate_jcs_sha256"`
	DCIndexSHA256               string   `json:"dc_index_sha256"`
	ODSEvidenceSHA256           string   `json:"ods_evidence_sha256"`
	CurrentHeadJudgeRevisionIDs []string `json:"current_head_judge_revision_ids"`
	CurrentHeadEventIDs         []string `json:"current_head_event_ids"`
	RTWSourceAndWikiAvailable   bool     `json:"rtw_source_and_wiki_available"`
	QualityState                string   `json:"quality_state"`
	HumanCatalogVerified        bool     `json:"human_catalog_verified"`
	D07Evaluable                bool     `json:"d07_evaluable"`
	ProductionVerified          bool     `json:"production_verified"`
}

func writeRealAsOfWitness0600(t *testing.T, directory, name string, raw []byte) string {
	t.Helper()
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if !ordinaryPrivate0600(path) {
		t.Fatalf("source version witness is not exact ordinary 0600: %s", path)
	}
	return path
}

// The old warehouse+SourceProof and optional DWD package tests have already
// committed this same real PG ODS prefix and DC ACK. This test only READS it.
func TestRTWRealWikiDCCurrentAlignment(t *testing.T) {
	directory := os.Getenv("SEA_BTW_WIKI_ASOF_EVIDENCE_DIR")
	if directory == "" {
		t.Skip("RTW must explicitly select a persistent DC as-of witness directory")
	}
	info, err := os.Lstat(directory)
	if err != nil || !filepath.IsAbs(directory) || !info.IsDir() ||
		info.Mode().Perm() != 0700 {
		t.Fatal("RTW DC-alignment witness requires a caller-owned ordinary empty 0700 directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("DC-alignment witness directory was already used by another generation")
	}
	if os.Getenv("WIKI_QUALITY_SOURCE_TEST_DSN") == "" ||
		os.Getenv("SEA_BTW_SOURCEPROOF_REAL_FIXTURE") == "" ||
		os.Getenv("SEA_RTW_REAL_WIKI_FACT_SET_FIXTURE") == "" {
		t.Fatal("DC-alignment test must run after the real same-PG source tests")
	}
	fixture := readRealSourceFixture(t, os.Getenv("SEA_BTW_SOURCEPROOF_REAL_FIXTURE"))
	readSameParentWarehouseFixture(t,
		os.Getenv("SEA_RTW_REAL_WIKI_FACT_SET_FIXTURE"), fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := pgxpool.New(ctx, os.Getenv("WIKI_QUALITY_SOURCE_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := wikiqualitysource.CheckFactSetSchema(ctx, db); err != nil {
		t.Fatalf("strong Wiki FactSet ODS schema is required: %v", err)
	}
	rtw, err := NewRTWHTTPReader(fixture.RTWURL, fixture.RTWToken, fixture.AdminToken)
	if err != nil {
		t.Fatal("RTW historical Catalog/Worker original Event reader unavailable")
	}
	sourceVersion, err := NewHTTPSourceVersionReader(fixture.RTWURL, fixture.RTWToken)
	if err != nil {
		t.Fatal("RTW Worker-only source version candidate reader unavailable")
	}
	dc, err := NewHTTPDCAckReader(fixture.DCURL, fixture.DCToken)
	if err != nil {
		t.Fatal("DataCenter immutable acknowledged-prefix reader unavailable")
	}
	original, err := rtw.ReadFactSetEvent(ctx, fixture.CatalogEventID)
	if err != nil {
		t.Fatalf("RTW original Catalog Event cannot be pinned: %v", err)
	}
	var event eventing.Event
	if json.Unmarshal(original.EventJSON, &event) != nil {
		t.Fatal("RTW Catalog Event has no typed source envelope")
	}
	set, err := wikiqualitysource.ParseFactSetV1(event, original.EventJSON,
		original.FactSetJCSSHA256)
	if err != nil || set.FactSetRevisionID != fixture.FactSetRevisionID ||
		set.SourceScopeRevision != fixture.SourceScopeRevision || len(set.Facts) != 2 {
		t.Fatalf("explicit two-fact historical Catalog changed: %v", err)
	}
	pinned := PinnedRequest{ModuleID: set.ModuleID, PageID: set.PageID,
		WikiRevisionID:      set.WikiRevisionID,
		SourceScopeRevision: set.SourceScopeRevision,
		FactSetRevisionID:   set.FactSetRevisionID,
		CutoffOffset:        int64(fixture.ExpectedEvents)}
	reader := AuthorityReader{ODS: PGODSReader{DB: db}, RTW: rtw}
	aligned, err := reader.ReadDCCurrentAlignment(ctx, pinned, dc, sourceVersion)
	if err != nil || aligned.SourceVersion < 1 ||
		aligned.CutoffOffset != int64(fixture.ExpectedEvents) ||
		!aligned.SourceAvailable || aligned.QualityState != "not_evaluable" ||
		len(aligned.RequiredHeads) != 2 || !validSHA(aligned.RTWCandidateJCSSHA256) ||
		!validSHA(aligned.DCIndexSHA256) || !validSHA(aligned.ODSEvidenceSHA256) {
		t.Fatalf("real RTW current V did not align with DC ACK C without human claims: %+v err=%v", aligned, err)
	}
	candidate, err := sourceVersion.ReadWikiQualitySourceVersionCandidate(ctx,
		pinned, aligned.SourceVersion)
	if err != nil || candidate.EventCount != int(aligned.SourceVersion) {
		t.Fatalf("real RTW source Candidate V cannot be re-read for lasting evidence: %v", err)
	}
	canonical, candidateSHA, err := jcs(candidate)
	if err != nil || candidateSHA != aligned.RTWCandidateJCSSHA256 {
		t.Fatal("RTW current source version changed between DC alignment and original witness freeze")
	}
	writeRealAsOfWitness0600(t, directory, "rtw-source-version-candidate.json", canonical)
	receipt := realDCCurrentReceipt{
		SchemaVersion: "sea.wiki.dc-current-source-alignment.v1",
		ModuleID:      pinned.ModuleID, FactSetRevisionID: pinned.FactSetRevisionID,
		WikiRevisionID: pinned.WikiRevisionID, SourceScopeRevision: pinned.SourceScopeRevision,
		SourceVersion: aligned.SourceVersion, DCCutoffOffset: aligned.CutoffOffset,
		RTWCandidateJCSSHA256:     candidateSHA,
		DCIndexSHA256:             aligned.DCIndexSHA256,
		ODSEvidenceSHA256:         aligned.ODSEvidenceSHA256,
		RTWSourceAndWikiAvailable: aligned.SourceAvailable,
		QualityState:              "not_evaluable",
	}
	for _, head := range aligned.RequiredHeads {
		receipt.CurrentHeadJudgeRevisionIDs = append(receipt.CurrentHeadJudgeRevisionIDs,
			head.JudgeRevisionID)
		receipt.CurrentHeadEventIDs = append(receipt.CurrentHeadEventIDs, head.EventID)
	}
	receiptRaw, receiptSHA, err := jcs(receipt)
	if err != nil {
		t.Fatal(err)
	}
	writeRealAsOfWitness0600(t, directory, "dc-current-source-alignment.json", receiptRaw)
	t.Logf("same acknowledged Wiki quality source V=%d DC C=%d CandidateJCS=%s ReceiptJCS=%s quality_state=not_evaluable",
		aligned.SourceVersion, aligned.CutoffOffset, candidateSHA, receiptSHA)
}
