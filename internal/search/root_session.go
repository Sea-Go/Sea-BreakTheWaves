package search

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
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

// RootSessionBoundary owns private per-attempt framework Sessions. Failed or
// unvalidated model/Graph events never reach the accepted history interface.
// The provided history and telemetry are borrowed; Close never closes them.
type RootSessionBoundary struct {
	root     *RootSummarizer
	attempts *inmemory.SessionService
	history  AcceptedRootHistory
}

func NewRootSessionBoundary(d *Delivery, m model.Model, history AcceptedRootHistory, observed *telemetry.Bundle,
	limits ...SummaryModelLimits) (*RootSessionBoundary, error) {
	return newRootSessionBoundary(d, m, nil, history, observed, nil, limits...)
}

// NewRootSessionBoundaryWithFastMedium keeps the v1 accepted-history boundary
// while adding one model planning stage only for fast/medium Summary requests.
func NewRootSessionBoundaryWithFastMedium(d *Delivery, m, plannerModel model.Model, history AcceptedRootHistory, observed *telemetry.Bundle,
	medium FastMediumModelLimits, limits ...SummaryModelLimits) (*RootSessionBoundary, error) {
	if isNil(plannerModel) || medium.MaxOutputTokens < 64 || medium.MaxOutputTokens > 512 || medium.WallTime < time.Second || medium.WallTime > 30*time.Second {
		return nil, ErrInvalid
	}
	return newRootSessionBoundary(d, m, plannerModel, history, observed, &medium, limits...)
}

func newRootSessionBoundary(d *Delivery, m, plannerModel model.Model, history AcceptedRootHistory, observed *telemetry.Bundle,
	medium *FastMediumModelLimits, limits ...SummaryModelLimits) (*RootSessionBoundary, error) {
	if isNil(history) {
		return nil, ErrInvalid
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
	return &RootSessionBoundary{root: root, attempts: attempts, history: history}, nil
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
	if b.root.fastMedium != nil && q.Search.Depth == Fast && q.Search.Intelligence == Medium {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.root.fastMedium.WallTime)
		defer cancel()
	}
	user, err := q.Subject.UserKey()
	if err != nil {
		return failed, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return failed, fmt.Errorf("create private search session: %w", err)
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
	return result, nil
}

// History reads only accepted product turns. It is not a framework Session
// history and does not currently seed the isolated LLMAgent's messages.
func (b *RootSessionBoundary) History(ctx context.Context, subject btwruntime.SubjectRef, sessionID string) ([]AcceptedRootTurn, error) {
	if b == nil || b.history == nil || ctx == nil || sessionID == "" {
		return nil, ErrInvalid
	}
	if _, err := subject.UserKey(); err != nil {
		return nil, err
	}
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
	var errs []error
	if b.root != nil {
		errs = append(errs, b.root.Close())
	}
	if b.attempts != nil {
		errs = append(errs, b.attempts.Close())
	}
	return errors.Join(errs...)
}
