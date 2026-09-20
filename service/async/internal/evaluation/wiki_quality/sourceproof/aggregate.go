// Package sourceproof freezes a bounded aggregate of RTW Catalog and per-Fact
// judgment provenance. It prepares an authority handoff, not a quality grade.
package sourceproof

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

const (
	Schema       = "sea.wiki.quality-sourceproof.v1"
	Producer     = "ridethewind.knowledge"
	Declaration  = "admin_jwt_allowlist_declaration"
	NotEvaluable = "not_evaluable"
	SourceOnly   = "rtw_dc_source_proof_only"
)

var ErrProof = errors.New("Wiki quality source aggregate lacks frozen RTW/DC evidence")
var ErrPrefix = errors.New("Wiki quality source aggregate lacks a true continuous prefix")

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)
var locatorPattern = regexp.MustCompile(`^paragraph:[1-9][0-9]*$`)

type EventProof struct {
	EventID     string `json:"event_id"`
	RawSHA256   string `json:"raw_sha256"`
	JCSSHA256   string `json:"jcs_sha256"`
	DCOffset    string `json:"dc_offset"`
	OriginalRaw []byte `json:"-"`
}

// FactCatalog is a BTW typed projection. The later RTW reader must verify
// each field against the ORIGINAL RTW Event and its explicit revision GET;
// this package does not presume the still-unfrozen FactSet wire JSON keys.
type CatalogFact struct {
	FactID              string `json:"fact_id"`
	SourceRevisionID    string `json:"source_revision_id"`
	Locator             string `json:"locator"`
	SourceContentSHA256 string `json:"source_content_sha256"`
	SourceQuoteSHA256   string `json:"source_quote_sha256"`
	Required            bool   `json:"required"`
}

type CatalogSource struct {
	RevisionID    string `json:"revision_id"`
	ContentSHA256 string `json:"content_sha256"`
}

type CatalogProof struct {
	ModuleID                string          `json:"module_id"`
	PageID                  string          `json:"page_id"`
	WikiRevisionID          string          `json:"wiki_revision_id"`
	SourceScopeRevision     string          `json:"source_scope_revision"`
	FactSetRevisionID       string          `json:"fact_set_revision_id"`
	FactSetPayloadJCSSHA256 string          `json:"fact_set_payload_jcs_sha256"`
	FactSetPayloadJCS       []byte          `json:"-"`
	Event                   EventProof      `json:"event"`
	Sources                 []CatalogSource `json:"sources"`
	Facts                   []CatalogFact   `json:"facts"`
	FactsCompleteDeclared   bool            `json:"facts_complete_declared"`
	DeclarationSource       string          `json:"declaration_source"`
	RTWActorID              string          `json:"rtw_actor_id"`
}

// One judgment Event cannot stand in for the Catalog or for another FactID.
// JudgeRevisionID identifies the immutable RTW decision version at Cutoff.
type JudgmentProof struct {
	WikiRevisionID         string     `json:"wiki_revision_id"`
	FactID                 string     `json:"fact_id"`
	JudgeRevisionID        string     `json:"judge_revision_id"`
	HeadAtCutoffRevisionID string     `json:"head_at_cutoff_revision_id"`
	SourceScopeRevision    string     `json:"source_scope_revision"`
	RTWActorID             string     `json:"rtw_actor_id"`
	Event                  EventProof `json:"event"`
}

type PrefixBatch struct {
	FromOffset       string `json:"from_offset"`
	ToOffset         string `json:"to_offset"`
	BatchJCSSHA256   string `json:"batch_jcs_sha256"`
	ODSReceiptSHA256 string `json:"ods_receipt_sha256"`
}

type PrefixEvent struct {
	DCOffset  string `json:"dc_offset"`
	EventID   string `json:"event_id"`
	JCSSHA256 string `json:"jcs_sha256"`
}

// Batches account for EVERY DC producer position from one through Through.
// Selected contains only Catalog/Judgment membership, never a substitute for
// the full prefix. The injected verifier must query true ODS/DC receipts.
type PrefixProof struct {
	Producer      string        `json:"producer"`
	Consumer      string        `json:"consumer"`
	ThroughOffset string        `json:"through_offset"`
	CutoffOffset  string        `json:"cutoff_offset"`
	Batches       []PrefixBatch `json:"batches"`
	Selected      []PrefixEvent `json:"selected"`
}

type RevisionStatus string

const (
	Available RevisionStatus = "available"
	Withdrawn RevisionStatus = "withdrawn"
	Unknown   RevisionStatus = "unknown"
)

type SourceStatus struct {
	SourceRevisionID string         `json:"source_revision_id"`
	State            RevisionStatus `json:"state"`
}

// AtCutoff records a HISTORICAL RTW read, not today's active head/Release.
// An unknown/withdrawn state remains explicit in the source-only receipt.
type WithdrawalSnapshot struct {
	AtCutoffOffset string         `json:"at_cutoff_offset"`
	WikiState      RevisionStatus `json:"wiki_state"`
	Sources        []SourceStatus `json:"sources"`
}

type Input struct {
	Catalog     CatalogProof
	Judgments   []JudgmentProof
	Prefix      PrefixProof
	Withdrawals WithdrawalSnapshot
}

// A source reader must independently attest actual PG/ODS committed rows,
// actual DC ACK and true batch index bytes. The shape-only package cannot
// mint this receipt from sparse selected Event offsets.
type PrefixAuthority interface {
	VerifyPrefix(context.Context, PrefixProof, string) (PrefixReceipt, error)
}

type PrefixReceipt struct {
	Producer                  string
	Consumer                  string
	CoverageIndexJCSSHA256    string
	CommittedThroughOffset    string
	AcknowledgedThroughOffset string
	AuthorityReceiptSHA256    string
}

type Receipt struct {
	SchemaVersion           string             `json:"schema_version"`
	Producer                string             `json:"producer"`
	WikiRevisionID          string             `json:"wiki_revision_id"`
	SourceScopeRevision     string             `json:"source_scope_revision"`
	FactSetRevisionID       string             `json:"fact_set_revision_id"`
	Catalog                 EventProof         `json:"catalog_event"`
	FactSetPayloadJCSSHA256 string             `json:"fact_set_payload_jcs_sha256"`
	Sources                 []CatalogSource    `json:"source_revisions"`
	RequiredFactIDs         []string           `json:"required_fact_ids"`
	Judgments               []JudgmentProof    `json:"judgments"`
	PrefixIndexJCSSHA256    string             `json:"prefix_index_jcs_sha256"`
	PrefixAuthoritySHA256   string             `json:"prefix_authority_sha256"`
	CutoffOffset            string             `json:"cutoff_offset"`
	WithdrawalSnapshot      WithdrawalSnapshot `json:"withdrawal_snapshot"`
	FactsCompleteDeclared   bool               `json:"facts_complete_declared"`
	DeclarationSource       string             `json:"declaration_source"`
	RTWActorID              string             `json:"rtw_actor_id"`
	EvidenceLevel           string             `json:"evidence_level"`
	QualityState            string             `json:"quality_state"`
	QualityReason           string             `json:"quality_reason"`
	Activation              string             `json:"activation"`
}

type Frozen struct {
	Receipt    Receipt
	ReceiptJCS []byte
	RootSHA256 string
}

func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func validSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func jcs(value any) ([]byte, string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return nil, "", err
	}
	return canonical, digest(canonical), nil
}

func decimal(raw string, positive bool) (int64, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && (n > 0 || !positive && n == 0) && strconv.FormatInt(n, 10) == raw
}

func actor(value string) bool {
	if strings.TrimSpace(value) == "" || len(value) > 200 || !utf8.ValidString(value) {
		return false
	}
	for _, ch := range value {
		if ch < 0x20 || ch == 0x7f {
			return false
		}
	}
	return true
}

func event(p EventProof) (int64, error) {
	offset, ok := decimal(p.DCOffset, true)
	if !ok || !idPattern.MatchString(p.EventID) || !validSHA(p.RawSHA256) ||
		!validSHA(p.JCSSHA256) || len(p.OriginalRaw) < 2 || len(p.OriginalRaw) > 1<<20 ||
		digest(p.OriginalRaw) != p.RawSHA256 {
		return 0, ErrProof
	}
	canonical, err := jsoncanonicalizer.Transform(p.OriginalRaw)
	if err != nil || digest(canonical) != p.JCSSHA256 {
		return 0, ErrProof
	}
	return offset, nil
}

func factID(sourceID, locator, quoteSHA string) string {
	return "fact_" + digest([]byte(sourceID+"\x00"+locator+"\x00"+quoteSHA))
}
