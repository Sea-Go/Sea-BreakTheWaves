package search

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

var ErrAcceptedHistory = errors.New("accepted search history unavailable or invalid")

// AcceptedRootTurn is a product handoff record, not a replayable framework
// Agent transcript. Only a completely validated root result may form one.
type AcceptedRootTurn struct {
	Request SummaryRequest `json:"request"`
	Result  SummaryResult  `json:"result"`
}

// AcceptedRootHistory is implemented by the authoritative conversation owner.
// Commit must durably and atomically accept one validated turn, keyed by the
// complete SubjectRef, logical session ID and AnswerID. Duplicate identical
// commits must be idempotent; conflicting AnswerIDs must fail. List must return
// only committed turns for that exact scope, in accepted order. A bare framework
// Session.Service does not satisfy this contract.
type AcceptedRootHistory interface {
	Commit(context.Context, AcceptedRootTurn) error
	List(context.Context, btwruntime.SubjectRef, string) ([]AcceptedRootTurn, error)
}

// AcceptedRootLookup is an optional exact AnswerID read. A durable history
// owner implements it when a worker may retry a lost response directly. An
// absent turn is distinct from an unavailable or malformed owner response.
// Existing histories without this capability keep their original behavior.
type AcceptedRootLookup interface {
	Get(context.Context, btwruntime.SubjectRef, string, string) (AcceptedRootTurn, bool, error)
}

// RootSessionBoundary owns private per-attempt framework Sessions. Failed or
// unvalidated model/Graph events never reach the accepted history interface.
// The provided history and telemetry are borrowed; Close never closes them.
type RootSessionBoundary struct {
	root      *RootSummarizer
	attempts  *inmemory.SessionService
	history   AcceptedRootHistory
	budget    RootHistoryBudget
	mu        sync.Mutex
	closed    bool
	next      uint64
	active    map[uint64]context.CancelFunc
	activeWG  sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

func NewRootSessionBoundary(d *Delivery, m model.Model, history AcceptedRootHistory, observed *telemetry.Bundle,
	limits ...SummaryModelLimits) (*RootSessionBoundary, error) {
	return newRootSessionBoundary(d, m, nil, history, observed, nil, nil, limits...)
}

// NewRootSessionBoundaryWithFastMedium keeps the v1 accepted-history boundary
// while adding one model planning stage only for fast/medium Summary requests.
func NewRootSessionBoundaryWithFastMedium(d *Delivery, m, plannerModel model.Model, history AcceptedRootHistory, observed *telemetry.Bundle,
	medium FastMediumModelLimits, limits ...SummaryModelLimits) (*RootSessionBoundary, error) {
	if isNil(plannerModel) || medium.MaxOutputTokens < 64 || medium.MaxOutputTokens > 512 || medium.WallTime < time.Second || medium.WallTime > 30*time.Second {
		return nil, ErrInvalid
	}
	return newRootSessionBoundary(d, m, plannerModel, history, observed, &medium, nil, limits...)
}

// NewRootSessionBoundaryWithHistorySeed adds explicit accepted-history
// injection: each new product attempt sees up to budget.MaxTurns prior
// accepted turns within budget.MaxBytes. Without this constructor the
// boundary keeps its original no-injection behavior.
func NewRootSessionBoundaryWithHistorySeed(d *Delivery, m model.Model, history AcceptedRootHistory, observed *telemetry.Bundle,
	budget RootHistoryBudget, limits ...SummaryModelLimits) (*RootSessionBoundary, error) {
	return newRootSessionBoundary(d, m, nil, history, observed, nil, &budget, limits...)
}

// NewRootSessionBoundaryWithFastMediumAndHistorySeed combines the fast/medium
// planning stage with explicit accepted-history injection.
func NewRootSessionBoundaryWithFastMediumAndHistorySeed(d *Delivery, m, plannerModel model.Model, history AcceptedRootHistory, observed *telemetry.Bundle,
	medium FastMediumModelLimits, budget RootHistoryBudget, limits ...SummaryModelLimits) (*RootSessionBoundary, error) {
	if isNil(plannerModel) || medium.MaxOutputTokens < 64 || medium.MaxOutputTokens > 512 || medium.WallTime < time.Second || medium.WallTime > 30*time.Second {
		return nil, ErrInvalid
	}
	return newRootSessionBoundary(d, m, plannerModel, history, observed, &medium, &budget, limits...)
}

func newRootSessionBoundary(d *Delivery, m, plannerModel model.Model, history AcceptedRootHistory, observed *telemetry.Bundle,
	medium *FastMediumModelLimits, budget *RootHistoryBudget, limits ...SummaryModelLimits) (*RootSessionBoundary, error) {
	if isNil(history) {
		return nil, ErrInvalid
	}
	bounded := RootHistoryBudget{}
	if budget != nil {
		if !budget.valid() {
			return nil, ErrHistoryBudget
		}
		bounded = *budget
	}
	attempts := inmemory.NewSessionService()
	var root *RootSummarizer
	var err error
	if medium == nil {
		root, err = NewRootSummarizer(d, m, attempts, observed, limits...)
	} else {
		root, err = NewRootSummarizerWithFastMedium(d, m, plannerModel, attempts, observed, *medium, limits...)
	}
	if err != nil {
		_ = attempts.Close()
		return nil, err
	}
	return &RootSessionBoundary{root: root, attempts: attempts, history: history, budget: bounded,
		active: make(map[uint64]context.CancelFunc)}, nil
}

func (b *RootSessionBoundary) beginAttempt(parent context.Context) (context.Context, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, btwruntime.ErrClosed
	}
	ctx, cancel := context.WithCancel(parent)
	b.next++
	id := b.next
	b.active[id] = cancel
	b.activeWG.Add(1)
	return ctx, func() {
		cancel()
		b.mu.Lock()
		delete(b.active, id)
		b.mu.Unlock()
		b.activeWG.Done()
	}, nil
}

// Summarize executes the existing single Runner/Graph in a fresh private
// Session. The real logical SessionID is retained only in the accepted handoff.
// Context cancellation or a rejected commit never yields a public answer.
func (b *RootSessionBoundary) Summarize(ctx context.Context, q SummaryRequest) (SummaryResult, error) {
	failed := SummaryResult{AnswerID: q.AnswerID, SummaryStatus: "failed"}
	if b == nil || b.root == nil || b.attempts == nil || b.history == nil || ctx == nil {
		return failed, ErrInvalid
	}
	if err := validateRootSummaryRequest(q); err != nil {
		return failed, err
	}
	if err := ctx.Err(); err != nil {
		return failed, err
	}
	var done func()
	var err error
	ctx, done, err = b.beginAttempt(ctx)
	if err != nil {
		return failed, err
	}
	defer done()
	if b.root.fastMedium != nil && q.Search.Depth == Fast && q.Search.Intelligence == Medium {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.root.fastMedium.WallTime)
		defer cancel()
	}
	user, err := q.Subject.UserKey()
	if err != nil {
		return failed, err
	}
	if lookup, ok := b.history.(AcceptedRootLookup); ok {
		committed, found, err := lookup.Get(ctx, q.Subject, q.SessionID, q.AnswerID)
		if err != nil {
			return failed, fmt.Errorf("look up accepted search turn: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return failed, err
		}
		if found {
			// A same-key retry has to reproduce the entire fixed request, not
			// merely the search ID or answer ID. The product owner also verifies
			// its immutable operation hash and current citation state.
			if !reflect.DeepEqual(committed.Request, q) || !validRootSummaryResult(committed.Result, q) {
				return failed, ErrAcceptedHistory
			}
			owned, err := cloneAcceptedRootTurn(committed)
			if err != nil {
				return failed, fmt.Errorf("copy accepted search turn: %w", err)
			}
			return owned.Result, nil
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return failed, fmt.Errorf("create private search session: %w", err)
	}
	if b.budget.valid() {
		// Accepted history reaches the model only through this explicit budget.
		// A history owner that is unavailable or returns a corrupted turn fails
		// the request closed; the model never runs against a partial session.
		seed, err := b.historySeed(ctx, q.Subject, q.SessionID)
		if err != nil {
			return failed, err
		}
		if seed.Block != "" {
			ctx = WithAcceptedHistorySeed(ctx, seed)
		}
	}
	private := q
	private.SessionID = "search-attempt-" + hex.EncodeToString(nonce[:])
	result, runErr := b.root.Summarize(ctx, private)
	// RootSummarizer reads the framework stream through EOF before returning.
	// Delete the private record on both success and failure, after the Runner is
	// done with it and before any validated answer can cross this boundary.
	deleteErr := b.attempts.DeleteSession(context.Background(), session.Key{
		AppName: "search_summary_root", UserID: user, SessionID: private.SessionID})
	if runErr != nil || deleteErr != nil {
		return failed, errors.Join(runErr, deleteErr)
	}
	if err := ctx.Err(); err != nil {
		return failed, err
	}
	if !validRootSummaryResult(result, q) {
		return failed, ErrRootSummaryOutput
	}
	turn := AcceptedRootTurn{Request: q, Result: result}
	turn, err = cloneAcceptedRootTurn(turn)
	if err != nil {
		return failed, fmt.Errorf("encode accepted search turn: %w", err)
	}
	if err := b.history.Commit(ctx, turn); err != nil {
		return failed, fmt.Errorf("commit accepted search turn: %w", err)
	}
	// Commit can durably accept this key and still return nil after the caller's
	// deadline. That turn remains readable from the history owner, but this
	// request must never report a successful public answer after cancellation.
	if err := ctx.Err(); err != nil {
		return failed, err
	}
	return result, nil
}

// History reads only accepted product turns. It is not a framework Session
// history; a new attempt consumes it only through the explicit history seed.
func (b *RootSessionBoundary) History(ctx context.Context, subject btwruntime.SubjectRef, sessionID string) ([]AcceptedRootTurn, error) {
	if b == nil || b.history == nil || ctx == nil || sessionID == "" {
		return nil, ErrInvalid
	}
	if _, err := subject.UserKey(); err != nil {
		return nil, err
	}
	return b.validatedHistory(ctx, subject, sessionID)
}

// historySeed bounds the session's accepted turns into one prompt block. It
// shares History's whole-turn validation: a corrupted or foreign stored turn
// fails closed instead of reaching the model.
func (b *RootSessionBoundary) historySeed(ctx context.Context, subject btwruntime.SubjectRef, sessionID string) (acceptedHistorySeed, error) {
	turns, err := b.validatedHistory(ctx, subject, sessionID)
	if err != nil {
		return acceptedHistorySeed{}, err
	}
	facts := make([]acceptedHistoryTurn, 0, len(turns))
	for _, turn := range turns {
		fact, injectable, err := acceptedHistoryTurnOf(turn)
		if err != nil {
			return acceptedHistorySeed{}, err
		}
		if injectable {
			facts = append(facts, fact)
		}
	}
	block, err := renderAcceptedHistoryBlock(facts, b.budget)
	if err != nil {
		return acceptedHistorySeed{}, err
	}
	seed := acceptedHistorySeed{Budget: b.budget, Turns: facts, Block: block}
	if err := historySeedIntegrity(seed); err != nil {
		return acceptedHistorySeed{}, err
	}
	return seed, nil
}

func (b *RootSessionBoundary) validatedHistory(ctx context.Context, subject btwruntime.SubjectRef, sessionID string) ([]AcceptedRootTurn, error) {
	turns, err := b.history.List(ctx, subject, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list accepted search turns: %w", err)
	}
	for _, turn := range turns {
		if turn.Request.Subject != subject || turn.Request.SessionID != sessionID ||
			validateRootSummaryRequest(turn.Request) != nil ||
			!validRootSummaryResult(turn.Result, turn.Request) {
			return nil, ErrAcceptedHistory
		}
	}
	// The store is borrowed. Never return its nested maps/slices to a caller.
	owned := make([]AcceptedRootTurn, 0, len(turns))
	for _, turn := range turns {
		copy, err := cloneAcceptedRootTurn(turn)
		if err != nil {
			return nil, fmt.Errorf("copy accepted search history: %w", err)
		}
		owned = append(owned, copy)
	}
	return owned, nil
}

func cloneAcceptedRootTurn(turn AcceptedRootTurn) (AcceptedRootTurn, error) {
	raw, err := json.Marshal(turn)
	if err != nil {
		return AcceptedRootTurn{}, err
	}
	var owned AcceptedRootTurn
	if err := json.Unmarshal(raw, &owned); err != nil {
		return AcceptedRootTurn{}, err
	}
	return owned, nil
}

func (b *RootSessionBoundary) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		cancels := make([]context.CancelFunc, 0, len(b.active))
		for _, cancel := range b.active {
			cancels = append(cancels, cancel)
		}
		b.mu.Unlock()
		for _, cancel := range cancels {
			cancel()
		}
		var errs []error
		if b.root != nil {
			errs = append(errs, b.root.Close())
		}
		// The Runner waits for its stream, but an accepted-history Commit can
		// still be active after that stream ends. Borrowed history must finish
		// before the private Session service is closed and Close returns.
		b.activeWG.Wait()
		if b.attempts != nil {
			errs = append(errs, b.attempts.Close())
		}
		b.closeErr = errors.Join(errs...)
	})
	return b.closeErr
}
