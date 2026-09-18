package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
)

var (
	ErrSourceMismatch    = errors.New("same-revision source does not match indexed locator and quote")
	ErrReceipt           = errors.New("citation acceptance receipt missing or mismatched")
	ErrEvidence          = errors.New("evidence not found in this search")
	ErrRetrievalContract = errors.New("search executor returned wrong snapshot or candidate")
)

// SourceReader is a read-only RTW adapter. It must return the requested
// revision, not the current head. The caller supplies the fixed snapshot and
// indexed location; this package verifies the returned text and locator.
type SourceReader interface {
	Read(context.Context, Snapshot, VerifiedCandidate) (corpus.Chunk, error)
}

type SourceReadFunc func(context.Context, Snapshot, VerifiedCandidate) (corpus.Chunk, error)

func (f SourceReadFunc) Read(ctx context.Context, s Snapshot, v VerifiedCandidate) (corpus.Chunk, error) {
	return f(ctx, s, v)
}

type Evidence struct {
	ID        string          `json:"evidence_id"`
	Key       Key             `json:"key"`
	Locator   corpus.Location `json:"locator"`
	Original  corpus.Ref      `json:"original"`
	Quote     string          `json:"quote"`
	QuoteHash string          `json:"quote_hash"`
	Relevance float64         `json:"relevance"`
	Sources   []LaneHit       `json:"sources"`
}

type EvidencePack struct {
	SearchID       string     `json:"search_id"`
	Snapshot       Snapshot   `json:"snapshot"`
	Profile        Profile    `json:"profile"`
	Status         string     `json:"status"`
	StopReason     string     `json:"stop_reason"`
	CoverageStatus string     `json:"coverage_status"`
	Gaps           []string   `json:"gaps"`
	Evidence       []Evidence `json:"evidence"`
}

// CitationReceipt is supplied by RTW only after the mapping was durably
// accepted. A transport acknowledgement is insufficient for this contract.
type CitationReceipt struct {
	SearchID   string `json:"search_id"`
	PackHash   string `json:"pack_hash"`
	DurableRef string `json:"durable_ref"`
}

type CitationAcceptor interface {
	Accept(context.Context, EvidencePack) (CitationReceipt, error)
}

type AcceptFunc func(context.Context, EvidencePack) (CitationReceipt, error)

func (f AcceptFunc) Accept(ctx context.Context, pack EvidencePack) (CitationReceipt, error) {
	return f(ctx, pack)
}

type EvidenceLimits struct {
	MaxReads      int
	MaxQuoteRunes int
}

// SearchExecutor can be the fixed-snapshot Service or a Search GraphAgent
// adapter that consumes its Runner through completion. The delivery boundary
// does not choose a second search execution loop.
type SearchExecutor interface {
	Execute(context.Context, Request) (Result, error)
}

type searchProfileAvailability interface {
	profileAvailability(Request) (Profile, error)
}

type ExecuteFunc func(context.Context, Request) (Result, error)

func (f ExecuteFunc) Execute(ctx context.Context, r Request) (Result, error) { return f(ctx, r) }

type Delivery struct {
	search  SearchExecutor
	checker EffectiveRevisionChecker
	source  SourceReader
	accept  CitationAcceptor
	limits  EvidenceLimits
}

func (d *Delivery) preflightProfile(r Request) (Profile, bool, error) {
	if d == nil || isNil(d.search) {
		return Profile{}, false, ErrInvalid
	}
	availability, ok := d.search.(searchProfileAvailability)
	if !ok {
		return Profile{}, false, nil
	}
	profile, err := availability.profileAvailability(r)
	return profile, true, err
}

// NewDelivery borrows all dependencies. The RTW adapter is mandatory so an
// indexed candidate can never become a publicly cited quote by itself.
func NewDelivery(search SearchExecutor, checker EffectiveRevisionChecker, source SourceReader, accept CitationAcceptor, limits EvidenceLimits) (*Delivery, error) {
	if isNil(search) || isNil(checker) || isNil(source) || isNil(accept) || limits.MaxReads < 1 || limits.MaxQuoteRunes < 1 {
		return nil, ErrInvalid
	}
	return &Delivery{search: search, checker: checker, source: source, accept: accept, limits: limits}, nil
}

type SearchResult struct {
	Retrieval Result          `json:"retrieval"`
	Pack      EvidencePack    `json:"evidence_pack"`
	Receipt   CitationReceipt `json:"citation_receipt"`
	Usage     EvidenceUsage   `json:"evidence_usage"`
}

type EvidenceUsage struct {
	SourceReadAttempts int `json:"source_read_attempts"`
	QuoteRunes         int `json:"quote_runes"`
}

// Search reads exact source revisions and obtains the durable RTW citation
// receipt before returning any quote. A receipt failure returns no pack.
func (d *Delivery) Search(ctx context.Context, searchID string, request Request) (SearchResult, error) {
	if ctx == nil || strings.TrimSpace(searchID) == "" || !validSnapshot(request.Snapshot) {
		return SearchResult{}, ErrInvalid
	}
	request.Snapshot = cloneSnapshot(request.Snapshot)
	expected := cloneSnapshot(request.Snapshot)
	retrieval, err := d.search.Execute(ctx, request)
	if err != nil {
		// A failed adapter has not passed the delivery contract. In particular,
		// its error result may still contain indexed text or invalid provenance.
		return SearchResult{}, err
	}
	if err := validateRetrieval(expected, request, retrieval); err != nil {
		return SearchResult{}, err
	}
	pack := EvidencePack{SearchID: searchID, Snapshot: cloneSnapshot(retrieval.Snapshot), Profile: retrieval.Profile,
		Status: retrieval.Status, StopReason: retrieval.StopReason, CoverageStatus: "not_assessed", Gaps: []string{}, Evidence: []Evidence{}}
	runes := 0
	reads := 0
	for _, candidate := range retrieval.Verified {
		if err := ctx.Err(); err != nil {
			return SearchResult{Retrieval: retrieval}, err
		}
		if reads >= d.limits.MaxReads {
			pack.Gaps = appendUnique(pack.Gaps, "read_budget")
			break
		}
		reads++
		quote, readErr := d.source.Read(ctx, cloneSnapshot(retrieval.Snapshot), cloneVerified([]VerifiedCandidate{candidate})[0])
		if readErr != nil {
			if err := ctx.Err(); err != nil {
				return SearchResult{Retrieval: retrieval}, err
			}
			if !request.AllowPartial {
				return SearchResult{Retrieval: retrieval}, fmt.Errorf("read same-revision source: %w", readErr)
			}
			pack.Gaps = appendUnique(pack.Gaps, "source_unavailable")
			continue
		}
		if err := checkSource(candidate, quote); err != nil {
			return SearchResult{Retrieval: retrieval}, err
		}
		if runes+utf8.RuneCountInString(quote.Text) > d.limits.MaxQuoteRunes {
			pack.Gaps = appendUnique(pack.Gaps, "quote_budget")
			continue
		}
		valid, err := d.checker.Check(ctx, retrieval.Snapshot, candidate.Chunk)
		if err != nil {
			return SearchResult{Retrieval: retrieval}, fmt.Errorf("recheck source state: %w", err)
		}
		if !valid {
			pack.Gaps = appendUnique(pack.Gaps, "revision_withdrawn")
			continue
		}
		runes += utf8.RuneCountInString(quote.Text)
		pack.Evidence = append(pack.Evidence, Evidence{ID: evidenceID(searchID, candidate.Key, quote.TextHash),
			Key: candidate.Key, Locator: quote.Location, Original: quote.Original, Quote: quote.Text,
			QuoteHash: quote.TextHash, Relevance: candidate.RRFScore, Sources: append([]LaneHit(nil), candidate.Sources...)})
	}
	if len(pack.Evidence) == 0 {
		pack.Status, pack.StopReason = "empty", "no_evidence"
	} else if len(pack.Gaps) != 0 || len(pack.Evidence) < len(retrieval.Verified) || retrieval.Status != "complete" {
		pack.Status = "partial"
	}
	if err := ctx.Err(); err != nil {
		return SearchResult{Retrieval: retrieval}, err
	}
	if len(pack.Evidence) == 0 {
		return SearchResult{Retrieval: retrieval, Pack: pack, Usage: EvidenceUsage{reads, runes}}, nil
	}
	packHash, err := pack.Hash()
	if err != nil {
		return SearchResult{Retrieval: retrieval}, fmt.Errorf("encode fixed evidence pack: %w", err)
	}
	receipt, err := d.accept.Accept(ctx, cloneEvidencePack(pack))
	if err != nil {
		return SearchResult{Retrieval: retrieval}, fmt.Errorf("accept citations: %w", err)
	}
	if receipt.SearchID != searchID || receipt.PackHash != packHash || receipt.DurableRef == "" {
		return SearchResult{Retrieval: retrieval}, ErrReceipt
	}
	if err := ctx.Err(); err != nil {
		// The RTW transaction may already have committed. Preserve its receipt
		// for recovery, but never return a public quote as a cancelled success.
		return SearchResult{Retrieval: retrieval, Receipt: receipt, Usage: EvidenceUsage{reads, runes}}, err
	}
	return SearchResult{Retrieval: retrieval, Pack: pack, Receipt: receipt, Usage: EvidenceUsage{reads, runes}}, nil
}

// SearchWithin applies an RTW-signed per-child upper bound without changing the
// shared delivery policy. RTW owns the cumulative parent ledger and refunds;
// this method only constrains the current source reads and public quote runes.
func (d *Delivery) SearchWithin(ctx context.Context, searchID string, request Request, limits EvidenceLimits) (SearchResult, error) {
	if d == nil || limits.MaxReads < 1 || limits.MaxQuoteRunes < 1 {
		return SearchResult{}, ErrInvalid
	}
	bounded := *d
	if limits.MaxReads < bounded.limits.MaxReads {
		bounded.limits.MaxReads = limits.MaxReads
	}
	if limits.MaxQuoteRunes < bounded.limits.MaxQuoteRunes {
		bounded.limits.MaxQuoteRunes = limits.MaxQuoteRunes
	}
	return bounded.Search(ctx, searchID, request)
}

func validateRetrieval(expected Snapshot, request Request, result Result) error {
	if !reflect.DeepEqual(expected, result.Snapshot) {
		return fmt.Errorf("%w: executor snapshot differs from the signed snapshot", ErrRetrievalContract)
	}
	if !validGraphResult(result, request) {
		return fmt.Errorf("%w: graph result violates the request contract (status=%s verified=%d subqueries=%d)",
			ErrRetrievalContract, result.Status, len(result.Verified), result.UsedSubqueries)
	}
	if !allowedEffectiveIntelligence(request.Intelligence, result.Profile.EffectiveIntelligence) {
		return fmt.Errorf("%w: effective intelligence %q not allowed for %q",
			ErrRetrievalContract, result.Profile.EffectiveIntelligence, request.Intelligence)
	}
	allowed := make(map[string]bool, len(expected.ValidRevisionIDs))
	for _, id := range expected.ValidRevisionIDs {
		allowed[id] = true
	}
	byKey := make(map[Key]Candidate, len(result.Candidates))
	for _, c := range result.Candidates {
		_, duplicate := byKey[c.Key]
		if c.Chunk.Text != "" || c.Key != key(c.Chunk) ||
			math.IsNaN(c.RRFScore) || math.IsInf(c.RRFScore, 0) ||
			!validCandidateSources(c.Sources, expected) || duplicate {
			return fmt.Errorf("%w: candidate %s violates the provenance contract", ErrRetrievalContract, c.Key.ChunkID)
		}
		byKey[c.Key] = c
	}
	seen := make(map[Key]bool, len(result.Verified))
	for _, v := range result.Verified {
		c, present := byKey[v.Key]
		if v.Key != key(v.Chunk) || !allowed[v.Key.RevisionID] || v.Chunk.Text != "" ||
			math.IsNaN(v.RRFScore) || math.IsInf(v.RRFScore, 0) ||
			!validCandidateSources(v.Sources, expected) || !present || seen[v.Key] ||
			c.RRFScore != v.RRFScore || !reflect.DeepEqual(c.Chunk, v.Chunk) || !reflect.DeepEqual(c.Sources, v.Sources) {
			return fmt.Errorf("%w: verified %s violates the candidate contract", ErrRetrievalContract, v.Key.ChunkID)
		}
		seen[v.Key] = true
	}
	return nil
}

func allowedEffectiveIntelligence(requested, effective Intelligence) bool {
	switch requested {
	case Low:
		return effective == Low
	case Medium:
		return effective == Medium || effective == Low
	case High:
		return effective == High || effective == Medium || effective == Low
	default:
		return false
	}
}

func validCandidateSources(hits []LaneHit, snapshot Snapshot) bool {
	if len(hits) == 0 || len(hits) > len(lanes) {
		return false
	}
	seen := make(map[Lane]bool, len(hits))
	for _, hit := range hits {
		index, present := snapshot.Indexes[hit.Lane]
		if !present || seen[hit.Lane] || hit.Index != index || hit.Rank < 1 ||
			math.IsNaN(hit.RawScore) || math.IsInf(hit.RawScore, 0) {
			return false
		}
		seen[hit.Lane] = true
	}
	return true
}

func (p EvidencePack) Hash() (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func cloneEvidencePack(p EvidencePack) EvidencePack {
	p.Snapshot = cloneSnapshot(p.Snapshot)
	p.Gaps = append([]string{}, p.Gaps...)
	copyEvidence := make([]Evidence, len(p.Evidence))
	for i, e := range p.Evidence {
		e.Sources = append([]LaneHit(nil), e.Sources...)
		copyEvidence[i] = e
	}
	p.Evidence = copyEvidence
	return p
}

func evidenceID(searchID string, key Key, quoteHash string) string {
	b, _ := json.Marshal(struct {
		SearchID string
		Key      Key
		Hash     string
	}{searchID, key, quoteHash})
	sum := sha256.Sum256(b)
	return "ev_" + hex.EncodeToString(sum[:])[:24]
}

func checkSource(candidate VerifiedCandidate, quote corpus.Chunk) error {
	indexed := candidate.Chunk
	if candidate.Key != key(indexed) || quote.ID != indexed.ID || key(quote) != candidate.Key ||
		!reflect.DeepEqual(quote.Location, indexed.Location) || quote.Location.Locator == "" ||
		quote.Original != indexed.Original || !artifacts.ValidHash(quote.Original.SHA256) ||
		quote.Original.Key != "sha256/"+quote.Original.SHA256 || quote.Text == "" ||
		!artifacts.ValidHash(indexed.TextHash) || quote.TextHash != indexed.TextHash ||
		artifacts.Hash([]byte(quote.Text)) != indexed.TextHash {
		return ErrSourceMismatch
	}
	if quote.SourceKind != "source" && quote.SourceKind != "wiki" {
		return ErrSourceMismatch
	}
	return nil
}

func appendUnique(xs []string, value string) []string {
	for _, x := range xs {
		if x == value {
			return xs
		}
	}
	return append(xs, value)
}
