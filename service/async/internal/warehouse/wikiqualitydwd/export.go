// Package wikiqualitydwd freezes an acknowledged Wiki quality ODS prefix for
// offline provenance. It does not consume DC events, grade a page, or publish
// a dataset. Its only authority is the existing SourceProof reader.
package wikiqualitydwd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/evaluation/wiki_quality/sourceproof"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/warehouse/wikiqualitysource"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

var ErrSource = errors.New("Wiki FactSet offline source evidence differs from acknowledged ODS")

const NotEvaluable = "not_evaluable"
const SourceOnly = "rtw_dc_source_proof_only"

// PrefixRow accounts for every accepted producer position. Technical skips
// remain here solely to prove the continuous prefix and carry no Fact label.
type PrefixRow struct {
	Producer                 string `json:"producer"`
	SourceOffset             int64  `json:"source_offset"`
	EventID                  string `json:"event_id"`
	EventType                string `json:"event_type"`
	Status                   string `json:"status"`
	EventSpecJCS             string `json:"event_spec_jcs"`
	EventJCSSHA256           string `json:"event_jcs_sha256"`
	DCReceipt                string `json:"dc_receipt"`
	DCReceiptSHA256          string `json:"dc_receipt_sha256"`
	RTWOriginalEvent         string `json:"rtw_original_event"`
	RTWOriginalEventSHA256   string `json:"rtw_original_event_sha256"`
	FactSetPayloadJCS        string `json:"fact_set_payload_jcs"`
	FactSetPayloadJCSSHA256  string `json:"fact_set_payload_jcs_sha256"`
	AcknowledgedCutoffOffset int64  `json:"acknowledged_cutoff_offset"`
	ODSEvidenceSHA256        string `json:"ods_evidence_sha256"`
	DCIndexSHA256            string `json:"dc_index_sha256"`
}

// CatalogFactRow is an administrator-declared inventory entry tied to one
// immutable Catalog revision, original Event and accepted DC receipt.
type CatalogFactRow struct {
	Producer                 string `json:"producer"`
	CatalogOffset            int64  `json:"catalog_offset"`
	CatalogEventID           string `json:"catalog_event_id"`
	CatalogEventRawSHA256    string `json:"catalog_event_raw_sha256"`
	CatalogEventJCSSHA256    string `json:"catalog_event_jcs_sha256"`
	CatalogDCReceiptSHA256   string `json:"catalog_dc_receipt_sha256"`
	FactSetPayloadJCSSHA256  string `json:"fact_set_payload_jcs_sha256"`
	ModuleID                 string `json:"module_id"`
	PageID                   string `json:"page_id"`
	WikiRevisionID           string `json:"wiki_revision_id"`
	FactSetRevisionID        string `json:"fact_set_revision_id"`
	SourceScopeRevision      string `json:"source_scope_revision"`
	FactID                   string `json:"fact_id"`
	SourceRevisionID         string `json:"source_revision_id"`
	SourceContentSHA256      string `json:"source_content_sha256"`
	Locator                  string `json:"locator"`
	SourceByteStart          string `json:"source_byte_start"`
	SourceByteEnd            string `json:"source_byte_end"`
	SourceQuote              string `json:"source_quote"`
	SourceQuoteSHA256        string `json:"source_quote_sha256"`
	Required                 bool   `json:"required"`
	ConflictGroup            string `json:"conflict_group"`
	FactsCompleteDeclared    bool   `json:"facts_complete_declared"`
	DeclarationSource        string `json:"declaration_source"`
	RTWActorID               string `json:"rtw_actor_id"`
	AcknowledgedCutoffOffset int64  `json:"acknowledged_cutoff_offset"`
	QualityState             string `json:"quality_state"`
}

// JudgmentRow records a transported Admin claim, including its grade, as
// source evidence. The grade is never treated as observed human quality.
type JudgmentRow struct {
	Producer                 string `json:"producer"`
	SourceOffset             int64  `json:"source_offset"`
	EventID                  string `json:"event_id"`
	EventRawSHA256           string `json:"event_raw_sha256"`
	EventJCSSHA256           string `json:"event_jcs_sha256"`
	DCReceiptSHA256          string `json:"dc_receipt_sha256"`
	ModuleID                 string `json:"module_id"`
	PageID                   string `json:"page_id"`
	WikiRevisionID           string `json:"wiki_revision_id"`
	FactSetRevisionID        string `json:"fact_set_revision_id"`
	SourceScopeRevision      string `json:"source_scope_revision"`
	FactID                   string `json:"fact_id"`
	JudgmentID               string `json:"judgment_id"`
	JudgeRevisionID          string `json:"judge_revision_id"`
	JudgeRevision            string `json:"judge_revision"`
	BaseJudgeRevisionID      string `json:"base_judge_revision_id"`
	SourceRevisionID         string `json:"source_revision_id"`
	SourceQuoteSHA256        string `json:"source_quote_sha256"`
	Assessment               string `json:"assessment"`
	ClaimedGrade             *int   `json:"claimed_grade"`
	ClaimedCitationPresent   bool   `json:"claimed_citation_present"`
	RTWActorID               string `json:"rtw_actor_id"`
	AcknowledgedCutoffOffset int64  `json:"acknowledged_cutoff_offset"`
	QualityState             string `json:"quality_state"`
}

type Frozen struct {
	PrefixRows    []PrefixRow
	CatalogFacts  []CatalogFactRow
	Judgments     []JudgmentRow
	PrefixJSONL   []byte
	CatalogJSONL  []byte
	JudgmentJSONL []byte
	Manifest      Manifest
	ManifestJCS   []byte
	ManifestSHA   string
}

// Manifest pins all three append-only landing byte streams to a single ACK
// cutoff. A later build uses a new generation and never overwrites this one.
type Manifest struct {
	Schema               string `json:"schema"`
	Producer             string `json:"producer"`
	Consumer             string `json:"consumer"`
	ModuleID             string `json:"module_id"`
	PageID               string `json:"page_id"`
	WikiRevisionID       string `json:"wiki_revision_id"`
	FactSetRevisionID    string `json:"fact_set_revision_id"`
	SourceScopeRevision  string `json:"source_scope_revision"`
	CutoffOffset         int64  `json:"cutoff_offset"`
	AcknowledgedAtLeast  int64  `json:"acknowledged_at_least"`
	CommittedAtLeast     int64  `json:"committed_at_least"`
	ODSEvidenceSHA256    string `json:"ods_evidence_sha256"`
	DCIndexSHA256        string `json:"dc_index_sha256"`
	PrefixJSONLSHA256    string `json:"prefix_jsonl_sha256"`
	CatalogJSONLSHA256   string `json:"catalog_jsonl_sha256"`
	JudgmentJSONLSHA256  string `json:"judgment_jsonl_sha256"`
	PrefixRows           int    `json:"prefix_rows"`
	CatalogFacts         int    `json:"catalog_facts"`
	JudgmentRevisions    int    `json:"judgment_revisions"`
	TechnicalSkips       int    `json:"technical_skips"`
	TransportedFactCount int    `json:"transported_fact_count"`
	EvidenceLevel        string `json:"evidence_level"`
	QualityState         string `json:"quality_state"`
	Activation           string `json:"activation"`
}

func hash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func jcs(raw []byte) ([]byte, error) { return jsoncanonicalizer.Transform(raw) }

func jcsValue(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jcs(raw)
}

func jsonl[T any](rows []T) ([]byte, error) {
	var out bytes.Buffer
	for _, row := range rows {
		encoded, err := jcsValue(row)
		if err != nil {
			return nil, err
		}
		out.Write(encoded)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

// Freeze first runs the existing SourceProof historical RTW, read-only ODS
// repeatable-read and DC ACK reader. It then re-reads the fixed ODS cutoff and
// rejects any change to the complete original Event/receipt/sidecar evidence.
// Only the last TRANSPORTED judgment at cutoff is selected; an untransported
// RTW as-of head and source withdrawal state still need separate authority.
func Freeze(ctx context.Context, reader sourceproof.AuthorityReader,
	req sourceproof.PinnedRequest, dc sourceproof.DCAckReader) (Frozen, error) {
	var out Frozen
	ack, err := reader.ReadAcknowledgedPinnedSource(ctx, req, dc)
	if err != nil {
		return out, err
	}
	snapshot, err := reader.ODS.ReadPrefix(ctx, req.CutoffOffset)
	if err != nil {
		return out, err
	}
	snapshotJCS, err := jcsValue(snapshot.Rows)
	if err != nil || snapshot.Producer != sourceproof.Producer ||
		snapshot.Consumer != wikiqualitysource.DefaultConsumer ||
		snapshot.CommittedOffset < req.CutoffOffset ||
		int64(len(snapshot.Rows)) != req.CutoffOffset ||
		hash(snapshotJCS) != ack.ODSEvidenceSHA256 {
		return out, ErrSource
	}
	index := make([]sourceproof.PrefixEvent, 0, req.CutoffOffset)
	for offset, row := range snapshot.Rows {
		if row.Offset != int64(offset)+1 {
			return out, ErrSource
		}
		index = append(index, sourceproof.PrefixEvent{DCOffset: strconv.FormatInt(row.Offset, 10),
			EventID: row.EventID, JCSSHA256: row.DCInputHash})
	}
	indexJCS, err := jcsValue(index)
	if err != nil || hash(indexJCS) != ack.ODSPrefixSHA256 {
		return out, ErrSource
	}
	catalogOffset, err := strconv.ParseInt(ack.Catalog.Event.DCOffset, 10, 64)
	if err != nil || catalogOffset < 1 || catalogOffset > req.CutoffOffset {
		return out, ErrSource
	}
	catalogRow := snapshot.Rows[catalogOffset-1]
	var catalogEvent eventing.Event
	if catalogRow.Status != "fact_set_verified" ||
		catalogRow.EventID != ack.Catalog.Event.EventID ||
		catalogRow.AuthorityRawSHA256 != ack.Catalog.Event.RawSHA256 ||
		catalogRow.DCInputHash != ack.Catalog.Event.JCSSHA256 ||
		catalogRow.FactSetPayloadJCSSHA256 != ack.Catalog.FactSetPayloadJCSSHA256 ||
		!bytes.Equal(catalogRow.FactSetPayloadJCS, ack.Catalog.FactSetPayloadJCS) ||
		json.Unmarshal(catalogRow.EventSpec, &catalogEvent) != nil {
		return out, ErrSource
	}
	frozenCatalog, err := wikiqualitysource.ParseFactSetV1(catalogEvent,
		catalogRow.AuthorityRaw, catalogRow.FactSetPayloadJCSSHA256)
	if err != nil || frozenCatalog.FactSetRevisionID != req.FactSetRevisionID ||
		frozenCatalog.SourceScopeRevision != req.SourceScopeRevision ||
		frozenCatalog.WikiRevisionID != req.WikiRevisionID {
		return out, ErrSource
	}
	facts := make(map[string]wikiqualitysource.FactSetFactV1, len(frozenCatalog.Facts))
	for _, fact := range frozenCatalog.Facts {
		facts[fact.FactID] = fact
		out.CatalogFacts = append(out.CatalogFacts, CatalogFactRow{
			Producer: sourceproof.Producer, CatalogOffset: catalogOffset,
			CatalogEventID:          catalogRow.EventID,
			CatalogEventRawSHA256:   catalogRow.AuthorityRawSHA256,
			CatalogEventJCSSHA256:   catalogRow.DCInputHash,
			CatalogDCReceiptSHA256:  hash(catalogRow.DCReceipt),
			FactSetPayloadJCSSHA256: catalogRow.FactSetPayloadJCSSHA256,
			ModuleID:                frozenCatalog.ModuleID, PageID: frozenCatalog.PageID,
			WikiRevisionID:      frozenCatalog.WikiRevisionID,
			FactSetRevisionID:   frozenCatalog.FactSetRevisionID,
			SourceScopeRevision: frozenCatalog.SourceScopeRevision,
			FactID:              fact.FactID, SourceRevisionID: fact.SourceRevisionID,
			SourceContentSHA256: fact.SourceContentSHA256,
			Locator:             fact.Locator, SourceByteStart: fact.SourceByteStart,
			SourceByteEnd: fact.SourceByteEnd, SourceQuote: fact.SourceQuote,
			SourceQuoteSHA256: fact.SourceQuoteSHA256, Required: fact.Required,
			ConflictGroup:            fact.ConflictGroup,
			FactsCompleteDeclared:    frozenCatalog.FactsComplete,
			DeclarationSource:        frozenCatalog.DeclarationSource,
			RTWActorID:               frozenCatalog.ActorID,
			AcknowledgedCutoffOffset: req.CutoffOffset, QualityState: NotEvaluable})
	}
	latest := map[string]JudgmentRow{}
	for _, row := range snapshot.Rows {
		out.PrefixRows = append(out.PrefixRows, PrefixRow{
			Producer: sourceproof.Producer, SourceOffset: row.Offset,
			EventID: row.EventID, EventType: row.EventType, Status: row.Status,
			EventSpecJCS: string(row.EventSpec), EventJCSSHA256: row.DCInputHash,
			DCReceipt: string(row.DCReceipt), DCReceiptSHA256: hash(row.DCReceipt),
			RTWOriginalEvent:         string(row.AuthorityRaw),
			RTWOriginalEventSHA256:   row.AuthorityRawSHA256,
			FactSetPayloadJCS:        string(row.FactSetPayloadJCS),
			FactSetPayloadJCSSHA256:  row.FactSetPayloadJCSSHA256,
			AcknowledgedCutoffOffset: req.CutoffOffset,
			ODSEvidenceSHA256:        ack.ODSEvidenceSHA256, DCIndexSHA256: ack.DCIndexSHA256})
		if row.Status == "technical_skip" {
			continue
		}
		if row.Status != "quality_verified" {
			continue
		}
		var event eventing.Event
		if json.Unmarshal(row.EventSpec, &event) != nil {
			return Frozen{}, ErrSource
		}
		var hinted struct {
			WikiRevisionID string `json:"wiki_revision_id"`
			FactID         string `json:"fact_id"`
		}
		if json.Unmarshal(event.Payload, &hinted) != nil ||
			hinted.WikiRevisionID != req.WikiRevisionID {
			continue
		}
		fact, relevant := facts[hinted.FactID]
		if !relevant {
			continue
		}
		judgment, err := wikiqualitysource.ParseJudgmentV1(event, row.AuthorityRaw)
		if err != nil || judgment.ModuleID != frozenCatalog.ModuleID ||
			judgment.PageID != frozenCatalog.PageID ||
			judgment.WikiContentSHA256 != frozenCatalog.WikiContentSHA256 ||
			judgment.SourceRevisionID != fact.SourceRevisionID ||
			judgment.SourceContentSHA256 != fact.SourceContentSHA256 ||
			judgment.Locator != fact.Locator ||
			judgment.SourceByteStart != fact.SourceByteStart ||
			judgment.SourceByteEnd != fact.SourceByteEnd ||
			judgment.SourceQuote != fact.SourceQuote ||
			judgment.SourceQuoteSHA256 != fact.SourceQuoteSHA256 {
			return Frozen{}, ErrSource
		}
		entry := JudgmentRow{Producer: sourceproof.Producer,
			SourceOffset: row.Offset, EventID: row.EventID,
			EventRawSHA256:  row.AuthorityRawSHA256,
			EventJCSSHA256:  row.DCInputHash,
			DCReceiptSHA256: hash(row.DCReceipt), ModuleID: judgment.ModuleID,
			PageID: judgment.PageID, WikiRevisionID: judgment.WikiRevisionID,
			FactSetRevisionID:   frozenCatalog.FactSetRevisionID,
			SourceScopeRevision: frozenCatalog.SourceScopeRevision,
			FactID:              judgment.FactID, JudgmentID: judgment.JudgmentID,
			JudgeRevisionID:     judgment.JudgeRevisionID,
			JudgeRevision:       judgment.JudgeRevision,
			BaseJudgeRevisionID: judgment.BaseJudgeRevisionID,
			SourceRevisionID:    judgment.SourceRevisionID,
			SourceQuoteSHA256:   judgment.SourceQuoteSHA256,
			Assessment:          judgment.Assessment, ClaimedGrade: judgment.Grade,
			ClaimedCitationPresent:   judgment.CitationPresent,
			RTWActorID:               judgment.ActorID,
			AcknowledgedCutoffOffset: req.CutoffOffset,
			QualityState:             NotEvaluable}
		out.Judgments = append(out.Judgments, entry)
		latest[judgment.FactID] = entry
	}
	if len(latest) != len(ack.Transported) {
		return Frozen{}, ErrSource
	}
	for _, transported := range ack.Transported {
		last, ok := latest[transported.FactID]
		if !ok || last.JudgeRevisionID != transported.JudgeRevisionID ||
			last.EventID != transported.Event.EventID ||
			strconv.FormatInt(last.SourceOffset, 10) != transported.Event.DCOffset ||
			last.EventRawSHA256 != transported.Event.RawSHA256 ||
			last.EventJCSSHA256 != transported.Event.JCSSHA256 {
			return Frozen{}, ErrSource
		}
	}
	out.PrefixJSONL, err = jsonl(out.PrefixRows)
	if err != nil {
		return Frozen{}, fmt.Errorf("freeze Wiki prefix JSONL: %w", err)
	}
	out.CatalogJSONL, err = jsonl(out.CatalogFacts)
	if err != nil {
		return Frozen{}, fmt.Errorf("freeze Wiki FactSet JSONL: %w", err)
	}
	out.JudgmentJSONL, err = jsonl(out.Judgments)
	if err != nil {
		return Frozen{}, fmt.Errorf("freeze Wiki judgments JSONL: %w", err)
	}
	out.Manifest = Manifest{Schema: "sea.wiki.fact-set-offline-source.v1",
		Producer: sourceproof.Producer, Consumer: wikiqualitysource.DefaultConsumer,
		ModuleID: req.ModuleID, PageID: req.PageID,
		WikiRevisionID:      req.WikiRevisionID,
		FactSetRevisionID:   req.FactSetRevisionID,
		SourceScopeRevision: req.SourceScopeRevision,
		CutoffOffset:        req.CutoffOffset,
		AcknowledgedAtLeast: ack.AcknowledgedAtLeast,
		CommittedAtLeast:    snapshot.CommittedOffset,
		ODSEvidenceSHA256:   ack.ODSEvidenceSHA256,
		DCIndexSHA256:       ack.DCIndexSHA256,
		PrefixJSONLSHA256:   hash(out.PrefixJSONL),
		CatalogJSONLSHA256:  hash(out.CatalogJSONL),
		JudgmentJSONLSHA256: hash(out.JudgmentJSONL),
		PrefixRows:          len(out.PrefixRows), CatalogFacts: len(out.CatalogFacts),
		JudgmentRevisions:    len(out.Judgments),
		TechnicalSkips:       countSkips(out.PrefixRows),
		TransportedFactCount: len(ack.Transported),
		EvidenceLevel:        SourceOnly, QualityState: NotEvaluable,
		Activation: "none"}
	out.ManifestJCS, err = jcsValue(out.Manifest)
	if err != nil {
		return Frozen{}, fmt.Errorf("freeze Wiki source manifest: %w", err)
	}
	out.ManifestSHA = hash(out.ManifestJCS)
	return out, nil
}

// Write creates exact 0600 evidence files in a caller-owned fresh directory.
// A nonempty/symlink directory fails before the first file. An IO failure may
// leave partial files; the loader requires all four exact files and hashes.
// Write creates no active pointer.
func (f Frozen) Write(directory string) error {
	manifestJCS, err := jcsValue(f.Manifest)
	if directory == "" || len(f.PrefixJSONL) == 0 ||
		len(f.CatalogJSONL) == 0 || len(f.JudgmentJSONL) == 0 ||
		err != nil || !bytes.Equal(manifestJCS, f.ManifestJCS) ||
		len(f.ManifestJCS) == 0 || hash(f.ManifestJCS) != f.ManifestSHA ||
		hash(f.PrefixJSONL) != f.Manifest.PrefixJSONLSHA256 ||
		hash(f.CatalogJSONL) != f.Manifest.CatalogJSONLSHA256 ||
		hash(f.JudgmentJSONL) != f.Manifest.JudgmentJSONLSHA256 {
		return ErrSource
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return ErrSource
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return os.ErrExist
	}
	for _, artifact := range []struct {
		name string
		body []byte
	}{{"prefix.jsonl", f.PrefixJSONL},
		{"catalog-facts.jsonl", f.CatalogJSONL},
		{"judgments.jsonl", f.JudgmentJSONL},
		{"manifest.json", f.ManifestJCS}} {
		file, err := os.OpenFile(filepath.Join(directory, artifact.name),
			os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if _, err := file.Write(artifact.body); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

func countSkips(rows []PrefixRow) int {
	count := 0
	for _, row := range rows {
		if row.Status == "technical_skip" {
			count++
		}
	}
	return count
}
