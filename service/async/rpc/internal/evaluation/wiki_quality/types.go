// Package wiki_quality freezes human-owned Wiki facts and computes offline
// diagnostics. It cannot approve an RTW revision, edit head or Release.
package wiki_quality

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

const (
	CaseSchema     = "sea.wiki.quality-case.v1"
	ReportSchema   = "sea.wiki.quality-report.v1"
	DatasetSchema  = "sea.wiki.quality-dataset.v1"
	MetricRevision = "wiki-quality-human-facts-v1"
)

var (
	ErrEvidence = errors.New("Wiki quality input differs from frozen RTW/DC bytes")
	ErrScope    = errors.New("Wiki quality version or source scope differs")
	ErrDataset  = errors.New("Wiki quality dataset manifest differs")
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
var paragraphPattern = regexp.MustCompile(`^paragraph:([1-9][0-9]*)$`)

type DataKind string

const (
	SyntheticFixture DataKind = "synthetic_fixture"
	HumanAdmin       DataKind = "human_admin"
)

type State string

const (
	Observed      State = "observed"
	NotEvaluable  State = "not_evaluable"
	NotApplicable State = "not_applicable"
)

// HashDomains are deliberately separate. A SourceRevision body, RTW business
// Compile input, DC whole Submit, candidate/Wiki Markdown, RTW Accept body,
// thirteen-key technical Manifest and DC whole Result have different owners.
type HashDomains struct {
	CompileInputSHA256      string `json:"compile_input_sha256"`
	DCJobInputSHA256        string `json:"dc_job_input_sha256"`
	RTWAcceptResultSHA256   string `json:"rtw_accept_result_sha256"`
	TechnicalManifestSHA256 string `json:"technical_manifest_sha256"`
	DCTechnicalResultSHA256 string `json:"dc_technical_result_sha256"`
}

// Scope binds the source order chosen for the original RTW Compile, the page
// base and the model/prompt that produced this candidate. Active head/Release
// is intentionally absent: historical quality is about this accepted revision.
type Scope struct {
	ModuleID             string      `json:"module_id"`
	PageID               string      `json:"page_id"`
	CompileID            string      `json:"compile_id,omitempty"`
	Generation           int64       `json:"generation"`
	BaseRevisionID       string      `json:"base_revision_id"`
	SourceRevisionIDs    []string    `json:"source_revision_ids"`
	ModelConfigurationID string      `json:"model_configuration_id,omitempty"`
	PromptVersion        string      `json:"prompt_version,omitempty"`
	Hashes               HashDomains `json:"hash_domains"`
}

// Source is a hydrated immutable RTW SourceRevision. QualifiedAtReview is
// supplied by the later RTW read-only authority, not inferred from a Wiki ref.
type Source struct {
	RevisionID        string `json:"revision_id"`
	ModuleID          string `json:"module_id"`
	Kind              string `json:"kind"`
	Content           string `json:"-"`
	ContentSHA256     string `json:"content_sha256"`
	QualifiedAtReview bool   `json:"qualified_at_review"`
	WithdrawnAtReview bool   `json:"withdrawn_at_review"`
}

type SourceRef struct {
	RevisionID string `json:"revision_id"`
	Locator    string `json:"locator"`
}

type WikiKind string

const (
	AIAccepted         WikiKind = "ai_accepted"
	ManualRevisionKind WikiKind = "manual_revision"
)

// WikiVersion carries exact raw Markdown; RevisionID is immutable RTW history.
type WikiVersion struct {
	Kind           WikiKind    `json:"kind"`
	RevisionID     string      `json:"revision_id"`
	ModuleID       string      `json:"module_id"`
	PageID         string      `json:"page_id"`
	BaseRevisionID string      `json:"base_revision_id"`
	Content        string      `json:"-"`
	ContentSHA256  string      `json:"content_sha256"`
	SourceRefs     []SourceRef `json:"source_refs"`
	Withdrawn      bool        `json:"withdrawn"`
}

// Candidate is the model proposal before RTW Accept. For an AI target, its
// exact Markdown SHA must match WikiVersion.ContentSHA256. For a human target,
// PreviousAI is a separate immutable Wiki revision whose ID is the new base.
type Candidate struct {
	Content       string      `json:"-"`
	ContentSHA256 string      `json:"content_sha256"`
	SourceRefs    []SourceRef `json:"source_refs"`
}

// Fact is the complete, RTW-human-selected expected fact unit. Start/End are
// byte offsets into the original SourceRevision object, and OriginalByteSHA
// hashes exactly Quote. They must lie within the pinned paragraph:N block.
// ConflictGroup is a HUMAN declared relation; this package never infers one.
type Fact struct {
	FactID             string `json:"fact_id"`
	SourceRevisionID   string `json:"source_revision_id"`
	Locator            string `json:"locator"`
	OriginalByteStart  int    `json:"original_byte_start"`
	OriginalByteEnd    int    `json:"original_byte_end"`
	Quote              string `json:"quote"`
	OriginalByteSHA256 string `json:"original_byte_sha256"`
	Required           bool   `json:"required"`
	ConflictGroup      string `json:"conflict_group,omitempty"`
}

type Disposition string

const (
	Covered      Disposition = "covered"
	Missing      Disposition = "missing"
	Conflict     Disposition = "conflict"
	Undetermined Disposition = "undetermined"
)

type WikiSpanProvenance string

const DerivedFirstMatch WikiSpanProvenance = "derived_first_match"

// A grade from 0 to 3 and disposition are independently signed human labels;
// there is no automatic grade mapping or LLM self-judgment. Non-missing labels
// bind RTW's claim TEXT/SHA. The Wiki byte span is derived from the first
// exact match in the target raw object; RTW v1 does not sign its position.
type FactJudgment struct {
	FactID              string             `json:"fact_id"`
	Grade               string             `json:"grade,omitempty"` // RTW defines grade meaning; BTW only counts 0..3.
	Disposition         Disposition        `json:"disposition"`
	WikiByteStart       int                `json:"wiki_byte_start"`
	WikiByteEnd         int                `json:"wiki_byte_end"`
	WikiClaimText       string             `json:"wiki_claim_text"`
	WikiClaimSHA256     string             `json:"wiki_claim_sha256,omitempty"`
	WikiSpanProvenance  WikiSpanProvenance `json:"wiki_span_provenance,omitempty"`
	Reason              string             `json:"reason"`
	BaseJudgeRevisionID string             `json:"base_judge_revision_id,omitempty"`
}

// Review is a BTW typed handoff, NOT the RTW single-fact Event wire layout.
// The RTW read-only adapter must independently verify the Event and a future
// complete FactCatalog/SourceScope receipt before returning AuthorityReceipt.
type Review struct {
	ReviewID         string            `json:"review_id"`
	ReviewVersion    string            `json:"review_version"`
	RTWActorID       string            `json:"rtw_actor_id,omitempty"`
	RubricVersion    string            `json:"rubric_version"`
	WikiRevisionID   string            `json:"wiki_revision_id"`
	DataKind         DataKind          `json:"data_kind"`
	SourceProvenance *SourceProvenance `json:"source_provenance,omitempty"`
	FactsComplete    bool              `json:"facts_complete"`
	LabelsComplete   bool              `json:"labels_complete"`
	Facts            []Fact            `json:"facts"`
	Judgments        []FactJudgment    `json:"judgments"`
}

// This is BTW DATASET provenance, not an invented RTW Event wire shape. The
// later independent Wiki ODS cursor supplies the TRUE contiguous DC offset
// and verifies the original RTW Event JCS SHA through its private read port.
type SourceProvenance struct {
	Producer          string `json:"producer"`
	EventID           string `json:"event_id"`
	RTWEventJCSSHA256 string `json:"rtw_event_jcs_sha256"`
	DCOffset          string `json:"dc_offset"`
}

// AuthorityVerifier is implemented by the RTW Event/PG reader only after its
// independent complete FactCatalog/SourceScope receipt is frozen.
// A mere caller-supplied human_admin string cannot make a case evaluable.
type AuthorityVerifier interface {
	Verify(context.Context, Scope, []Source, Review) (AuthorityReceipt, error)
}

// A DC receipt is a separate authority from RTW's human fact event. BTW must
// not label caller-supplied token counts observed in a real human dataset.
type CostVerifier interface {
	Verify(context.Context, Scope, CostReceipt) error
}

type Verifiers struct {
	Facts AuthorityVerifier
	Cost  CostVerifier
}

type AuthorityReceipt struct {
	ReviewJCSSHA256         string
	SourceScopeJCSSHA256    string
	FactCatalogRevisionID   string
	FactCatalogJCSSHA256    string // Separate future complete Catalog/Scope proof, never one judgment Event.
	FactsComplete           bool
	LabelsComplete          bool
	RevisionsQualified      bool
	AuthorityEvidenceSHA256 string
	RTWEventJCSSHA256       string
	DCOffset                string
}

// Input evaluates exactly one immutable AI or human Wiki revision. To compare
// a later human edit with its AI base, evaluate both as distinct target cases;
// PreviousAI is only edit-delta evidence, never the target's fact judgment.
type Input struct {
	Scope      Scope
	Sources    []Source
	Target     WikiVersion
	Candidate  *Candidate
	PreviousAI *WikiVersion
	Review     Review
	Cost       *CostReceipt
}

type CostReceipt struct {
	DCUsageReceiptSHA256 string `json:"dc_usage_receipt_sha256"`
	ModelConfigurationID string `json:"model_configuration_id"`
	PromptTokens         int64  `json:"prompt_tokens"`
	CompletionTokens     int64  `json:"completion_tokens"`
	TotalTokens          int64  `json:"total_tokens"`
	WallMillis           int64  `json:"wall_millis"`
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func JCS(value any) ([]byte, string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return nil, "", err
	}
	return canonical, Digest(canonical), nil
}

func validHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func validText(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= limit && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0)
}

func sortedFacts(facts []Fact) []Fact {
	out := append([]Fact(nil), facts...)
	sort.Slice(out, func(i, j int) bool { return out[i].FactID < out[j].FactID })
	return out
}

func sortedJudgments(judgments []FactJudgment) []FactJudgment {
	out := append([]FactJudgment(nil), judgments...)
	sort.Slice(out, func(i, j int) bool { return out[i].FactID < out[j].FactID })
	return out
}
