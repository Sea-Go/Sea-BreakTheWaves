package grounding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const RTWKeySchema = "sea.rtw.reviewer-key.v1"
const RTWReceiptSchema = "sea.rtw.answer-grounding-review-receipt.v1"
const RTWReviewerAuthority = "ridethewind.knowledge.admin"

var ErrRTWRecordNotFound = errors.New("RTW authority record not found")
var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
var logicalIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)
var publicKeyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// RTWReviewReceipt is RTW's immutable, Worker-readable PG/Outbox projection.
// RTW verifies answer/quote/pack, while BTW independently binds trace/model
// source bytes from the previously frozen Case.
type RTWReviewReceipt struct {
	SchemaVersion            string `json:"schema_version"`
	CaseSHA256               string `json:"case_sha256"`
	CaseJSON                 string `json:"case_json"`
	ReviewJSON               string `json:"review_json"`
	ReviewSHA256             string `json:"review_sha256"`
	KeyID                    string `json:"key_id"`
	ReviewerAuthority        string `json:"reviewer_authority"`
	ReviewerID               string `json:"reviewer_id"`
	DataKind                 string `json:"data_kind"`
	RegistryRevisionAtCommit int64  `json:"registry_revision_at_commit"`
	AnswerID                 string `json:"answer_id"`
	SearchID                 string `json:"search_id"`
	AcceptedAnswerSHA256     string `json:"accepted_answer_sha256"`
	TurnSHA256               string `json:"turn_sha256"`
	CitationPackRef          string `json:"citation_pack_ref"`
	CitationPackSHA256       string `json:"citation_pack_sha256"`
	TraceAuthorityStatus     string `json:"trace_authority_status"`
	ReviewedAt               string `json:"reviewed_at"`
	EventID                  string `json:"event_id"`
	EventSHA256              string `json:"event_sha256"`
}

type RTWReviewerKey struct {
	SchemaVersion       string `json:"schema_version"`
	KeyID               string `json:"key_id"`
	ReviewerAuthority   string `json:"reviewer_authority"`
	ReviewerID          string `json:"reviewer_id"`
	DataKind            string `json:"data_kind"`
	PublicKeyEd25519Hex string `json:"public_key_ed25519_hex"`
	RegisteredAt        string `json:"registered_at"`
	RevokedAt           string `json:"revoked_at"`
	Status              string `json:"status"`
	RegistryRevision    int64  `json:"registry_revision"`
	RegistrationEventID string `json:"registration_event_id"`
	RevocationEventID   string `json:"revocation_event_id"`
}

type RTWCitationEvidence struct {
	ID         string          `json:"evidence_id"`
	SourceKind string          `json:"source_kind"`
	ContentID  string          `json:"content_id"`
	RevisionID string          `json:"revision_id"`
	ChunkID    string          `json:"chunk_id"`
	Original   json.RawMessage `json:"original"`
	Locator    json.RawMessage `json:"locator"`
	QuoteHash  string          `json:"quote_hash"`
	State      string          `json:"state"`
}

type RTWCitationRecord struct {
	SearchID            string                `json:"search_id"`
	PackHash            string                `json:"pack_hash"`
	DurableRef          string                `json:"durable_ref"`
	ModuleID            string                `json:"module_id"`
	ReleaseID           string                `json:"release_id"`
	Generation          int64                 `json:"generation"`
	PublicationRevision string                `json:"publication_revision"`
	Evidence            []RTWCitationEvidence `json:"evidence"`
}

type RTWAuthority interface {
	Review(context.Context, string) (RTWReviewReceipt, error)
	ReviewerKey(context.Context, string) (RTWReviewerKey, error)
	Citation(context.Context, string) (RTWCitationRecord, error)
}

// HTTPAuthority uses a server-configured Worker token. No reviewer public key
// is accepted from local review/trust files on this path.
type HTTPAuthority struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewHTTPAuthority(baseURL, token string) (*HTTPAuthority, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" || strings.TrimSpace(token) == "" ||
		(parsed.Scheme == "http" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") {
		return nil, ErrEvidence
	}
	return &HTTPAuthority{baseURL: strings.TrimRight(baseURL, "/"), token: strings.TrimSpace(token),
		client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}, nil
}

func (a *HTTPAuthority) get(ctx context.Context, path string, out any) error {
	if a == nil || a.client == nil || ctx == nil {
		return ErrEvidence
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+a.token)
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ErrRTWRecordNotFound
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: RTW Worker HTTP %d", ErrEvidence, response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return ErrEvidence
	}
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF ||
		envelope.Code != 200 || envelope.Msg != "success" || len(envelope.Data) == 0 {
		return fmt.Errorf("%w: RTW Worker success envelope", ErrEvidence)
	}
	decoder = json.NewDecoder(bytes.NewReader(envelope.Data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("%w: RTW Worker data fields", ErrEvidence)
	}
	return nil
}

func (a *HTTPAuthority) Review(ctx context.Context, caseSHA string) (RTWReviewReceipt, error) {
	var result RTWReviewReceipt
	if !hashPattern.MatchString(caseSHA) {
		return result, ErrEvidence
	}
	err := a.get(ctx, "/internal/v1/knowledge/answer-grounding/reviews/"+caseSHA, &result)
	return result, err
}

func (a *HTTPAuthority) ReviewerKey(ctx context.Context, keyID string) (RTWReviewerKey, error) {
	var result RTWReviewerKey
	if !keyIDPattern.MatchString(keyID) {
		return result, ErrEvidence
	}
	err := a.get(ctx, "/internal/v1/knowledge/reviewer-keys/"+keyID, &result)
	return result, err
}

func (a *HTTPAuthority) Citation(ctx context.Context, searchID string) (RTWCitationRecord, error) {
	var result RTWCitationRecord
	if !logicalIDPattern.MatchString(searchID) {
		return result, ErrEvidence
	}
	err := a.get(ctx, "/internal/v1/knowledge/search-citations/"+searchID, &result)
	return result, err
}

func parseUTC(value string) (time.Time, error) {
	when, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	_, offset := when.Zone()
	if offset != 0 {
		return time.Time{}, ErrEvidence
	}
	return when, nil
}

func strictReview(raw string) (Review, error) {
	var review Review
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&review) != nil || decoder.Decode(new(any)) != io.EOF {
		return Review{}, ErrEvidence
	}
	return review, nil
}

func validateRTWCitation(value Case, receipt RTWReviewReceipt, citation RTWCitationRecord) error {
	durableRef := citationRef(value.SearchID)
	durableHash := strings.TrimPrefix(durableRef, citationRefPrefix)
	publicationRevision, revisionErr := strconv.ParseInt(citation.PublicationRevision, 10, 64)
	if citation.SearchID != value.SearchID || citation.DurableRef != durableRef ||
		citation.PackHash != receipt.CitationPackSHA256 || !hashPattern.MatchString(citation.PackHash) ||
		citation.PackHash == durableHash || !logicalIDPattern.MatchString(citation.ModuleID) ||
		!logicalIDPattern.MatchString(citation.ReleaseID) || citation.Generation < 1 || revisionErr != nil ||
		publicationRevision < 1 || citation.PublicationRevision != strconv.FormatInt(publicationRevision, 10) ||
		len(citation.Evidence) == 0 {
		return fmt.Errorf("%w: RTW current durable citation pack", ErrEvidence)
	}
	wanted := make(map[string]Evidence, len(value.Evidence))
	for _, evidence := range value.Evidence {
		wanted[evidence.ID] = evidence
	}
	seen := make(map[string]bool, len(citation.Evidence))
	matched := 0
	for _, cited := range citation.Evidence {
		if !logicalIDPattern.MatchString(cited.ID) || seen[cited.ID] ||
			(cited.SourceKind != "source" && cited.SourceKind != "wiki") ||
			!logicalIDPattern.MatchString(cited.ContentID) || !logicalIDPattern.MatchString(cited.RevisionID) ||
			!logicalIDPattern.MatchString(cited.ChunkID) || !json.Valid(cited.Original) ||
			bytes.Equal(bytes.TrimSpace(cited.Original), []byte("null")) || !json.Valid(cited.Locator) ||
			bytes.Equal(bytes.TrimSpace(cited.Locator), []byte("null")) ||
			!hashPattern.MatchString(cited.QuoteHash) ||
			(cited.State != "available" && cited.State != "unavailable") {
			return fmt.Errorf("%w: RTW cited evidence fields", ErrEvidence)
		}
		seen[cited.ID] = true
		if expected, ok := wanted[cited.ID]; ok {
			if cited.QuoteHash != expected.QuoteSHA256 {
				return fmt.Errorf("%w: RTW cited quote hash", ErrEvidence)
			}
			matched++
		}
	}
	if matched != len(wanted) {
		return fmt.Errorf("%w: RTW accepted-answer citations missing from pack", ErrEvidence)
	}
	return nil
}

// EvaluateFromRTW fetches both review and current key status from RTW Worker.
// Revocation invalidates a historical signature's present authority without
// erasing the immutable review fact. Human labels remain pending until a
// separately pinned RTW service identity is configured outside caller input.
func EvaluateFromRTW(ctx context.Context, value Case, caseRaw []byte, authority RTWAuthority) (Result, error) {
	if authority == nil || ctx == nil {
		return Result{}, ErrEvidence
	}
	base, err := Evaluate(value, caseRaw, nil, nil)
	if err != nil {
		return Result{}, err
	}
	receipt, err := authority.Review(ctx, base.CaseSHA256)
	if errors.Is(err, ErrRTWRecordNotFound) {
		return base, nil
	}
	if err != nil {
		return Result{}, err
	}
	durableRef := citationRef(value.SearchID)
	durableHash := strings.TrimPrefix(durableRef, citationRefPrefix)
	if receipt.SchemaVersion != RTWReceiptSchema || receipt.CaseSHA256 != base.CaseSHA256 ||
		receipt.CaseJSON != string(caseRaw) || receipt.ReviewJSON == "" ||
		Digest([]byte(receipt.ReviewJSON)) != receipt.ReviewSHA256 ||
		!keyIDPattern.MatchString(receipt.KeyID) || receipt.ReviewerAuthority != RTWReviewerAuthority ||
		receipt.ReviewerID == "" || (receipt.DataKind != "human_admin" && receipt.DataKind != "synthetic_fixture") ||
		receipt.RegistryRevisionAtCommit < 1 || receipt.AnswerID != value.AnswerID ||
		receipt.SearchID != value.SearchID || receipt.AcceptedAnswerSHA256 != value.AcceptedAnswerSHA256 ||
		!hashPattern.MatchString(receipt.TurnSHA256) || receipt.CitationPackRef != durableRef ||
		!hashPattern.MatchString(receipt.CitationPackSHA256) || receipt.CitationPackSHA256 == durableHash ||
		receipt.TraceAuthorityStatus != "external_case_unverified" ||
		receipt.EventID == "" || !hashPattern.MatchString(receipt.EventSHA256) {
		return Result{}, fmt.Errorf("%w: RTW immutable receipt/source fields", ErrEvidence)
	}
	review, err := strictReview(receipt.ReviewJSON)
	if err != nil ||
		review.Payload.CaseSHA256 != base.CaseSHA256 || review.Payload.ReviewerAuthority != receipt.ReviewerAuthority ||
		review.Payload.ReviewerID != receipt.ReviewerID || review.Payload.DataKind != receipt.DataKind {
		return Result{}, fmt.Errorf("%w: RTW review payload/receipt identity", ErrEvidence)
	}
	claimTime, err := parseUTC(review.Payload.ReviewedAt)
	if err != nil {
		return Result{}, fmt.Errorf("%w: review UTC time", ErrEvidence)
	}
	citation, err := authority.Citation(ctx, value.SearchID)
	if err != nil {
		return Result{}, err
	}
	if err := validateRTWCitation(value, receipt, citation); err != nil {
		return Result{}, err
	}
	key, err := authority.ReviewerKey(ctx, receipt.KeyID)
	if errors.Is(err, ErrRTWRecordNotFound) {
		base.ReviewSHA256 = receipt.ReviewSHA256
		base.Decision = "pending_authority_registry"
		base.EvidenceLevel = "rtw_registry_key_not_found"
		return base, nil
	}
	if err != nil {
		return Result{}, err
	}
	if key.SchemaVersion != RTWKeySchema || key.KeyID != receipt.KeyID ||
		key.ReviewerAuthority != receipt.ReviewerAuthority || key.ReviewerID != receipt.ReviewerID ||
		key.DataKind != receipt.DataKind || key.RegistryRevision < receipt.RegistryRevisionAtCommit ||
		!publicKeyPattern.MatchString(key.PublicKeyEd25519Hex) || key.RegisteredAt == "" || key.RegistrationEventID == "" ||
		(key.Status != "active" && key.Status != "revoked") {
		return Result{}, fmt.Errorf("%w: RTW current reviewer registry fields", ErrEvidence)
	}
	registeredAt, err := parseUTC(key.RegisteredAt)
	if err != nil {
		return Result{}, fmt.Errorf("%w: reviewer registration UTC time", ErrEvidence)
	}
	reviewedAt, err := parseUTC(receipt.ReviewedAt)
	if err != nil || !reviewedAt.Equal(claimTime) || reviewedAt.Before(registeredAt) {
		return Result{}, fmt.Errorf("%w: signed review/key active interval", ErrEvidence)
	}
	if key.Status == "active" && (key.RegistryRevision != receipt.RegistryRevisionAtCommit ||
		key.RevokedAt != "" || key.RevocationEventID != "") ||
		key.Status == "revoked" && (key.RegistryRevision <= receipt.RegistryRevisionAtCommit ||
			key.RevokedAt == "" || key.RevocationEventID == "") {
		return Result{}, fmt.Errorf("%w: reviewer active/revoked state", ErrEvidence)
	}
	if key.Status == "revoked" {
		revokedAt, err := parseUTC(key.RevokedAt)
		if err != nil || revokedAt.Before(reviewedAt) || revokedAt.Before(registeredAt) {
			return Result{}, fmt.Errorf("%w: revocation UTC time", ErrEvidence)
		}
	}
	trust := &Trust{SchemaVersion: TrustSchema, DataKind: key.DataKind,
		Authority: key.ReviewerAuthority, ReviewerID: key.ReviewerID, PublicKey: key.PublicKeyEd25519Hex}
	result, err := Evaluate(value, caseRaw, []byte(receipt.ReviewJSON), trust)
	if err != nil {
		return Result{}, fmt.Errorf("RTW registered review signature/span: %w", err)
	}
	if key.Status == "revoked" {
		result.Decision = "pending_revoked_authority"
		result.EvidenceLevel = "rtw_registry_revoked"
	} else if key.DataKind == "synthetic_fixture" {
		result.EvidenceLevel = "rtw_registry_synthetic_fixture"
	} else {
		result.Decision = "pending_authority_service_identity"
		result.EvidenceLevel = "rtw_worker_key_unpinned_human_admin"
	}
	return result, nil
}
