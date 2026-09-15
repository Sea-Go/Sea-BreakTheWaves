package grounding

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const ReviewSchema = "sea.search.answer-grounding-review.v1"
const TrustSchema = "sea.search.answer-grounding-trust.v1"

// Claim is an exact UTF-8 byte span of the accepted answer. The label is a
// human evaluation, not a lexical overlap or LLM self-critique.
type Claim struct {
	StartByte   int      `json:"start_byte"`
	EndByte     int      `json:"end_byte"`
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidence_ids"`
	Label       string   `json:"label"` // supported, unsupported, uncertain
	Reason      string   `json:"reason"`
}

type ReviewPayload struct {
	SchemaVersion     string  `json:"schema_version"`
	CaseSHA256        string  `json:"case_sha256"`
	PolicyRevision    string  `json:"policy_revision"`
	DataKind          string  `json:"data_kind"` // human_admin or synthetic_fixture
	ReviewerAuthority string  `json:"reviewer_authority"`
	ReviewerID        string  `json:"reviewer_id"`
	ReviewedAt        string  `json:"reviewed_at"`
	CoverageComplete  bool    `json:"coverage_complete"`
	Claims            []Claim `json:"claims"`
}

// Signature is Ed25519 over encoding/json's deterministic UTF-8 bytes of
// ReviewPayload. Trusted public keys are supplied out of band by RTW admin.
type Review struct {
	Payload   ReviewPayload `json:"payload"`
	Signature string        `json:"signature"`
}

type Trust struct {
	SchemaVersion string `json:"schema_version"`
	DataKind      string `json:"data_kind"`
	Authority     string `json:"authority"`
	ReviewerID    string `json:"reviewer_id"`
	PublicKey     string `json:"public_key_ed25519_hex"`
}

type Result struct {
	SchemaVersion   string `json:"schema_version"`
	CaseID          string `json:"case_id"`
	CaseSHA256      string `json:"case_sha256"`
	ReviewSHA256    string `json:"review_sha256,omitempty"`
	Decision        string `json:"decision"`
	EvidenceLevel   string `json:"evidence_level"`
	Unsupported     int    `json:"unsupported_claims"`
	Supported       int    `json:"supported_claims"`
	Uncertain       int    `json:"uncertain_claims"`
	Activation      string `json:"activation"`
	RuntimeBlocking bool   `json:"runtime_blocking"`
}

func ValidateCase(value Case) error {
	if value.SchemaVersion != CaseSchema || value.PolicyRevision != ReviewPolicy ||
		value.ReviewStatus != "pending_human_review" || value.Activation != "none" ||
		value.SearchID == "" || value.AnswerID == "" || value.ModelInteractionID == "" ||
		!hashPattern.MatchString(value.ModelResponseSHA256) ||
		!hashPattern.MatchString(value.RTWAnswerReportSHA256) ||
		!hashPattern.MatchString(value.DCUsageReportSHA256) ||
		!hashPattern.MatchString(value.RTWTraceLogSHA256) ||
		!tracePattern.MatchString(value.RTWTraceID) ||
		value.TraceScope != "rtw_structured_log_not_otlp_collector" ||
		value.CitationPackRef != citationRef(value.SearchID) ||
		len(value.Evidence) == 0 ||
		strings.TrimSpace(value.AcceptedAnswer) == "" ||
		Digest([]byte(value.AcceptedAnswer)) != value.AcceptedAnswerSHA256 {
		return ErrEvidence
	}
	seenEvidence := map[string]bool{}
	for _, evidence := range value.Evidence {
		if evidence.ID == "" || seenEvidence[evidence.ID] || evidence.Quote == "" ||
			Digest([]byte(evidence.Quote)) != evidence.QuoteSHA256 {
			return ErrEvidence
		}
		seenEvidence[evidence.ID] = true
	}
	copyValue := value
	copyValue.CaseID = ""
	identity, err := json.Marshal(copyValue)
	if err != nil || value.CaseID != "grounding-"+Digest(identity) {
		return ErrEvidence
	}
	return nil
}

// Evaluate returns pending without an authenticated review. A signed
// unsupported span rejects the offline case; a supported result requires
// explicit complete coverage of all non-whitespace answer bytes.
func Evaluate(value Case, caseRaw, reviewRaw []byte, trust *Trust) (Result, error) {
	if err := ValidateCase(value); err != nil || len(caseRaw) == 0 {
		return Result{}, ErrEvidence
	}
	canonicalCase, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return Result{}, err
	}
	if !bytes.Equal(caseRaw, append(canonicalCase, '\n')) {
		return Result{}, ErrEvidence
	}
	result := Result{SchemaVersion: "sea.search.answer-grounding-result.v1", CaseID: value.CaseID,
		CaseSHA256: Digest(caseRaw), Decision: "pending_human_review", EvidenceLevel: "rtw_structured_trace_model_and_quote_bound",
		Activation: "none", RuntimeBlocking: false}
	if len(reviewRaw) == 0 {
		return result, nil
	}
	if trust == nil {
		return Result{}, errors.New("trusted RTW admin reviewer key required")
	}
	var review Review
	if json.Unmarshal(reviewRaw, &review) != nil ||
		review.Payload.SchemaVersion != ReviewSchema || review.Payload.PolicyRevision != ReviewPolicy ||
		review.Payload.CaseSHA256 != Digest(caseRaw) || review.Payload.ReviewerID == "" ||
		review.Payload.ReviewerAuthority == "" || len(review.Payload.Claims) == 0 ||
		(review.Payload.DataKind != "human_admin" && review.Payload.DataKind != "synthetic_fixture") ||
		trust.SchemaVersion != TrustSchema || trust.DataKind != review.Payload.DataKind ||
		trust.Authority != review.Payload.ReviewerAuthority || trust.ReviewerID != review.Payload.ReviewerID {
		return Result{}, ErrEvidence
	}
	when, err := time.Parse(time.RFC3339Nano, review.Payload.ReviewedAt)
	_, offset := when.Zone()
	if err != nil || offset != 0 {
		return Result{}, ErrEvidence
	}
	publicKey, err := hex.DecodeString(trust.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return Result{}, ErrEvidence
	}
	signature, err := hex.DecodeString(review.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Result{}, ErrEvidence
	}
	payload, err := json.Marshal(review.Payload)
	if err != nil || !ed25519.Verify(publicKey, payload, signature) {
		return Result{}, ErrEvidence
	}
	result.ReviewSHA256 = Digest(reviewRaw)
	result.EvidenceLevel = review.Payload.DataKind
	validEvidence := map[string]bool{}
	for _, evidence := range value.Evidence {
		validEvidence[evidence.ID] = true
	}
	answer := []byte(value.AcceptedAnswer)
	covered := make([]bool, len(answer))
	for _, claim := range review.Payload.Claims {
		if claim.StartByte < 0 || claim.EndByte > len(answer) || claim.EndByte <= claim.StartByte ||
			!utf8.Valid(answer[:claim.StartByte]) || !utf8.Valid(answer[:claim.EndByte]) ||
			claim.Text != string(answer[claim.StartByte:claim.EndByte]) ||
			strings.TrimSpace(claim.Reason) == "" || len(claim.EvidenceIDs) == 0 {
			return Result{}, ErrEvidence
		}
		seen := map[string]bool{}
		for _, id := range claim.EvidenceIDs {
			if !validEvidence[id] || seen[id] {
				return Result{}, ErrEvidence
			}
			seen[id] = true
		}
		for i := claim.StartByte; i < claim.EndByte; i++ {
			if covered[i] {
				return Result{}, ErrEvidence
			}
			covered[i] = true
		}
		switch claim.Label {
		case "supported":
			result.Supported++
		case "unsupported":
			result.Unsupported++
		case "uncertain":
			result.Uncertain++
		default:
			return Result{}, ErrEvidence
		}
	}
	if result.Unsupported > 0 {
		result.Decision = "rejected_unsupported"
	} else if result.Uncertain > 0 {
		result.Decision = "pending_adjudication"
	} else if review.Payload.CoverageComplete {
		complete := true
		for i, b := range answer {
			if b != ' ' && b != '\n' && b != '\t' && !covered[i] {
				complete = false
			}
		}
		if complete {
			result.Decision = "supported_review_complete"
		}
	}
	// A public key supplied beside the review is not an RTW administrator
	// registry. Verify its claims, but never promote a self-declared human key
	// until RTW provides a separately pinned and revocable trust owner.
	if review.Payload.DataKind == "human_admin" {
		result.Decision = "pending_authority_registry"
		result.EvidenceLevel = "self_supplied_key_not_RTW_registry"
	} else {
		result.Decision = "synthetic_" + result.Decision
	}
	return result, nil
}
