package search

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type ToolBudget struct {
	MaxSearchCalls int `json:"max_search_calls"`
	MaxReadCalls   int `json:"max_read_calls"`
	MaxQuoteRunes  int `json:"max_quote_runes"`
}

type ToolRemaining struct {
	SearchCalls int `json:"search_calls"`
	ReadCalls   int `json:"read_calls"`
	QuoteRunes  int `json:"quote_runes"`
}

type SearchToolInput struct {
	Query        string       `json:"query" jsonschema:"description=Search question"`
	Intelligence Intelligence `json:"intelligence" jsonschema:"description=low medium or high"`
	ContinueID   string       `json:"continue_search_id,omitempty" jsonschema:"description=Existing search ID for continuation; currently unavailable"`
}

type SearchToolResult struct {
	Search    SearchResult  `json:"search"`
	Remaining ToolRemaining `json:"remaining"`
}

type ReadEvidenceInput struct {
	SearchID   string `json:"search_id" jsonschema:"description=ID returned by search_fast or search_detailed"`
	EvidenceID string `json:"evidence_id" jsonschema:"description=Evidence ID returned by the same search"`
}

type ReadEvidenceResult struct {
	Evidence  Evidence        `json:"evidence"`
	Receipt   CitationReceipt `json:"citation_receipt"`
	Remaining ToolRemaining   `json:"remaining"`
}

// ToolSession is allocated for one authenticated invocation. The caller owns
// its lifetime and must never share it across subjects; model JSON cannot set
// its snapshot, publication scope or cumulative budget.
type ToolSession struct {
	delivery     *Delivery
	snapshot     Snapshot
	operationID  string
	remaining    ToolRemaining
	allowPartial bool
	allowLower   bool
	gate         chan struct{}
	sequence     int
	searches     map[string]SearchResult
}

func NewToolSession(delivery *Delivery, snapshot Snapshot, operationID string, budget ToolBudget, allowPartial, allowLower bool) (*ToolSession, error) {
	if delivery == nil || !validSnapshot(snapshot) || strings.TrimSpace(operationID) == "" || len(operationID) > 200 ||
		budget.MaxSearchCalls < 1 || budget.MaxReadCalls < 1 || budget.MaxQuoteRunes < 1 {
		return nil, ErrInvalid
	}
	s := &ToolSession{delivery: delivery, snapshot: cloneSnapshot(snapshot), operationID: operationID,
		remaining:    ToolRemaining{budget.MaxSearchCalls, budget.MaxReadCalls, budget.MaxQuoteRunes},
		allowPartial: allowPartial, allowLower: allowLower, gate: make(chan struct{}, 1), searches: make(map[string]SearchResult)}
	s.gate <- struct{}{}
	return s, nil
}

func (s *ToolSession) withGate(ctx context.Context, fn func() error) error {
	if ctx == nil {
		return ErrInvalid
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.gate:
	}
	defer func() { s.gate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func (s *ToolSession) search(ctx context.Context, depth Depth, in SearchToolInput) (out SearchToolResult, err error) {
	if s == nil || strings.TrimSpace(in.Query) == "" ||
		(in.Intelligence != Low && in.Intelligence != Medium && in.Intelligence != High) {
		return out, ErrInvalid
	}
	if in.ContinueID != "" {
		return out, ErrUnavailable
	}
	err = s.withGate(ctx, func() error {
		if s.remaining.SearchCalls < 1 || s.remaining.ReadCalls < 1 || s.remaining.QuoteRunes < 1 {
			return ErrBudget
		}
		s.sequence++
		// RTW's durable citation identity allows only bounded ASCII letters,
		// digits, underscore, dash and dot. Hash the trusted run identity and
		// sequence so Tool calls remain deterministic without leaking raw IDs or
		// sending the old colon-delimited value that RTW rejects with HTTP 400.
		id := "search_" + artifacts.Hash([]byte(fmt.Sprintf("%s:%d", s.operationID, s.sequence)))
		s.remaining.SearchCalls--
		limits := s.delivery.limits
		if limits.MaxReads > s.remaining.ReadCalls {
			limits.MaxReads = s.remaining.ReadCalls
		}
		if limits.MaxQuoteRunes > s.remaining.QuoteRunes {
			limits.MaxQuoteRunes = s.remaining.QuoteRunes
		}
		// Reserve the bounded maximum before execution. Failed calls spend the
		// reservation too, so retries cannot mint free reader/model work.
		s.remaining.ReadCalls -= limits.MaxReads
		s.remaining.QuoteRunes -= limits.MaxQuoteRunes
		bounded := *s.delivery
		bounded.limits = limits
		result, runErr := bounded.Search(ctx, id, Request{Query: in.Query, Depth: depth, Intelligence: in.Intelligence,
			AllowPartial: s.allowPartial, AllowLowerIntelligence: s.allowLower, Snapshot: cloneSnapshot(s.snapshot)})
		if runErr != nil {
			return runErr
		}
		// Once a completed result reports actual reads and delivered quote
		// length, release the unused part of this call's reservation.
		s.remaining.ReadCalls += limits.MaxReads - result.Usage.SourceReadAttempts
		s.remaining.QuoteRunes += limits.MaxQuoteRunes - result.Usage.QuoteRunes
		s.searches[id] = SearchResult{Retrieval: Result{Verified: cloneVerified(result.Retrieval.Verified)},
			Pack: cloneEvidencePack(result.Pack), Receipt: result.Receipt, Usage: result.Usage}
		out = SearchToolResult{Search: result, Remaining: s.remaining}
		return nil
	})
	return out, err
}

func (s *ToolSession) SearchFast(ctx context.Context, in SearchToolInput) (SearchToolResult, error) {
	return s.search(ctx, Fast, in)
}

func (s *ToolSession) SearchDetailed(ctx context.Context, in SearchToolInput) (SearchToolResult, error) {
	return s.search(ctx, Detailed, in)
}

func (s *ToolSession) ReadEvidence(ctx context.Context, in ReadEvidenceInput) (out ReadEvidenceResult, err error) {
	if s == nil || in.SearchID == "" || in.EvidenceID == "" {
		return out, ErrInvalid
	}
	err = s.withGate(ctx, func() error {
		stored, ok := s.searches[in.SearchID]
		if !ok {
			return ErrEvidence
		}
		var evidence *Evidence
		var candidate *VerifiedCandidate
		for i := range stored.Pack.Evidence {
			if stored.Pack.Evidence[i].ID == in.EvidenceID {
				evidence = &stored.Pack.Evidence[i]
				break
			}
		}
		for i := range stored.Retrieval.Verified {
			if evidence != nil && stored.Retrieval.Verified[i].Key == evidence.Key {
				candidate = &stored.Retrieval.Verified[i]
				break
			}
		}
		packHash, hashErr := stored.Pack.Hash()
		if evidence == nil || candidate == nil || hashErr != nil || stored.Receipt.DurableRef == "" || stored.Receipt.PackHash != packHash {
			return ErrEvidence
		}
		if s.remaining.ReadCalls < 1 || s.remaining.QuoteRunes < utf8.RuneCountInString(evidence.Quote) {
			return ErrBudget
		}
		s.remaining.ReadCalls--
		s.remaining.QuoteRunes -= utf8.RuneCountInString(evidence.Quote)
		quote, readErr := s.delivery.source.Read(ctx, cloneSnapshot(stored.Pack.Snapshot), *candidate)
		if readErr != nil {
			return fmt.Errorf("reread evidence: %w", readErr)
		}
		if err := checkSource(*candidate, quote); err != nil {
			return err
		}
		if quote.TextHash != evidence.QuoteHash || quote.Text != evidence.Quote || quote.Location != evidence.Locator {
			return ErrSourceMismatch
		}
		valid, checkErr := s.delivery.checker.Check(ctx, stored.Pack.Snapshot, candidate.Chunk)
		if checkErr != nil {
			return fmt.Errorf("recheck evidence state: %w", checkErr)
		}
		if !valid {
			return ErrEvidence
		}
		copyEvidence := *evidence
		copyEvidence.Sources = append([]LaneHit(nil), evidence.Sources...)
		out = ReadEvidenceResult{Evidence: copyEvidence, Receipt: stored.Receipt, Remaining: s.remaining}
		return nil
	})
	return out, err
}

// Tools exposes typed framework FunctionTools. The outer caller Agent owns
// whether to search again or produce an answer; these tools return no answer.
func (s *ToolSession) Tools() ([]tool.Tool, error) {
	if s == nil {
		return nil, ErrInvalid
	}
	return []tool.Tool{
		function.NewFunctionTool(s.SearchFast, function.WithName("search_fast"), function.WithDescription("One bounded batch of fixed-snapshot evidence; returns structured citations, not an answer.")),
		function.NewFunctionTool(s.SearchDetailed, function.WithName("search_detailed"), function.WithDescription("Bounded multi-batch fixed-snapshot evidence; continuation from an existing search ID is not yet supported.")),
		function.NewFunctionTool(s.ReadEvidence, function.WithName("read_evidence"), function.WithDescription("Reread an evidence ID from this invocation's fixed revision and durable citation receipt.")),
	}, nil
}
