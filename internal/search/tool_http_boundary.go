package search

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

const (
	toolRunRequestKey = "tool_search_request"
	toolRunResultKey  = "tool_search_result"
	toolRunNode       = "search_and_accept_tool_evidence"
	toolRunApp        = "search_tools_root"
)

var ErrToolRunOutput = errors.New("tool search graph output missing or invalid")

// ToolRunRequest contains only fields fixed by RTW's signed child scope. The
// public caller cannot select its identity, snapshot, search ID or limits.
type ToolRunRequest struct {
	Subject     btwruntime.SubjectRef
	SessionID   string
	OperationID string
	BudgetRef   string
	SearchID    string
	Search      Request
	Limits      EvidenceLimits
}

func validToolRunRequest(q ToolRunRequest) bool {
	if _, err := q.Subject.UserKey(); err != nil {
		return false
	}
	return q.SessionID != "" && q.OperationID != "" && q.BudgetRef != "" && q.SearchID != "" &&
		strings.TrimSpace(q.Search.Query) != "" && q.Limits.MaxReads > 0 && q.Limits.MaxQuoteRunes > 0 &&
		(q.Search.Depth == Fast || q.Search.Depth == Detailed) &&
		(q.Search.Intelligence == Low || q.Search.Intelligence == Medium || q.Search.Intelligence == High) &&
		validSnapshot(q.Search.Snapshot)
}

func validToolRunResult(found SearchResult, q ToolRunRequest) bool {
	if !validGraphResult(found.Retrieval, q.Search) || found.Pack.SearchID != q.SearchID ||
		!reflect.DeepEqual(found.Pack.Snapshot, q.Search.Snapshot) ||
		found.Pack.Profile != found.Retrieval.Profile || found.Pack.Status != found.Retrieval.Status && found.Pack.Status != "partial" && found.Pack.Status != "empty" ||
		found.Usage.SourceReadAttempts < len(found.Pack.Evidence) || found.Usage.SourceReadAttempts > q.Limits.MaxReads ||
		found.Usage.QuoteRunes < 0 || found.Usage.QuoteRunes > q.Limits.MaxQuoteRunes {
		return false
	}
	if len(found.Pack.Evidence) == 0 {
		return found.Pack.Status == "empty" && found.Receipt == (CitationReceipt{})
	}
	hash, err := found.Pack.Hash()
	return err == nil && found.Receipt.SearchID == q.SearchID && found.Receipt.PackHash == hash && found.Receipt.DurableRef != ""
}

// ToolRunBoundary owns a tRPC-Agent-Go Graph/Runner and private attempt
// sessions. RTW owns the durable parent budget and accepted citation rows.
type ToolRunBoundary struct {
	runtime   *btwruntime.Runtime
	attempts  *inmemory.SessionService
	nodes     *toolRunNodeLifetime
	mu        sync.Mutex
	closed    bool
	next      uint64
	active    map[uint64]context.CancelFunc
	activeWG  sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// Runner.Close cancels its event loop but need not wait for a Graph node that
// is already inside the borrowed RTW CitationAcceptor. Track the actual node
// separately from the public Search goroutine, which can return on cancel.
type toolRunNodeLifetime struct {
	mu     sync.Mutex
	closed bool
	active sync.WaitGroup
}

func (n *toolRunNodeLifetime) begin() (func(), error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, btwruntime.ErrClosed
	}
	n.active.Add(1)
	return n.active.Done, nil
}

func (n *toolRunNodeLifetime) closeAndWait() {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.closed = true
	n.mu.Unlock()
	n.active.Wait()
}

func NewToolRunBoundary(delivery *Delivery, observed *telemetry.Bundle) (*ToolRunBoundary, error) {
	if delivery == nil || observed == nil || !observed.Installed() || observed.Closed() {
		return nil, ErrInvalid
	}
	nodes := &toolRunNodeLifetime{}
	schema := graph.NewStateSchema().
		AddField(toolRunRequestKey, graph.StateField{Type: reflect.TypeOf(ToolRunRequest{}), Reducer: graph.DefaultReducer}).
		AddField(toolRunResultKey, graph.StateField{Type: reflect.TypeOf(SearchResult{}), Reducer: graph.DefaultReducer})
	compiled, err := graph.NewStateGraph(schema).
		AddNode(toolRunNode, func(ctx context.Context, state graph.State) (any, error) {
			finish, err := nodes.begin()
			if err != nil {
				return nil, err
			}
			defer finish()
			q, ok := graph.GetStateValue[ToolRunRequest](state, toolRunRequestKey)
			if !ok || !validToolRunRequest(q) {
				return nil, ErrInvalid
			}
			found, err := delivery.SearchWithin(ctx, q.SearchID, q.Search, q.Limits)
			if err != nil {
				return nil, fmt.Errorf("search and accept tool evidence: %w", err)
			}
			if !validToolRunResult(found, q) {
				return nil, ErrToolRunOutput
			}
			return graph.State{toolRunResultKey: found}, nil
		}).
		SetEntryPoint(toolRunNode).SetFinishPoint(toolRunNode).Compile()
	if err != nil {
		return nil, fmt.Errorf("compile tool search graph: %w", err)
	}
	ag, err := graphagent.New(toolRunApp, compiled)
	if err != nil {
		return nil, fmt.Errorf("construct tool search agent: %w", err)
	}
	attempts := inmemory.NewSessionService()
	runtime, err := btwruntime.New(toolRunApp, ag, attempts, observed)
	if err != nil {
		_ = attempts.Close()
		return nil, err
	}
	return &ToolRunBoundary{runtime: runtime, attempts: attempts, nodes: nodes,
		active: make(map[uint64]context.CancelFunc)}, nil
}

func (b *ToolRunBoundary) beginAttempt(parent context.Context) (context.Context, func(), error) {
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

// Search consumes the complete Runner stream, deletes its private session and
// returns only a validated delivery result. A lost HTTP reply can be retried
// with the same RTW search ID and recovered by RTW's idempotent acceptor.
func (b *ToolRunBoundary) Search(ctx context.Context, q ToolRunRequest) (SearchResult, error) {
	if b == nil || b.runtime == nil || b.attempts == nil || ctx == nil || !validToolRunRequest(q) {
		return SearchResult{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	var done func()
	var err error
	ctx, done, err = b.beginAttempt(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	defer done()
	q.Search.Snapshot = cloneSnapshot(q.Search.Snapshot)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return SearchResult{}, fmt.Errorf("create private tool session: %w", err)
	}
	privateSession := "tool-attempt-" + hex.EncodeToString(nonce[:])
	option := agent.MergeRuntimeState(map[string]any{toolRunRequestKey: q})
	var completed int
	var found SearchResult
	_, runErr := b.runtime.Run(ctx, btwruntime.Request{Subject: q.Subject, SessionID: privateSession,
		RunID: q.SearchID, Message: model.NewUserMessage(q.Search.Query), Options: []agent.RunOption{option}},
		func(_ context.Context, e *event.Event) error {
			if !graph.IsGraphCompletionEvent(e) {
				return nil
			}
			completed++
			raw, ok := e.StateDelta[toolRunResultKey]
			if !ok || json.Unmarshal(raw, &found) != nil || !validToolRunResult(found, q) {
				return ErrToolRunOutput
			}
			return nil
		})
	user, _ := q.Subject.UserKey()
	deleteErr := b.attempts.DeleteSession(context.Background(), session.Key{AppName: toolRunApp,
		UserID: user, SessionID: privateSession})
	if runErr != nil || deleteErr != nil {
		return SearchResult{}, errors.Join(runErr, deleteErr)
	}
	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	if completed != 1 || !validToolRunResult(found, q) {
		return SearchResult{}, ErrToolRunOutput
	}
	return found, nil
}

func (b *ToolRunBoundary) Close() error {
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
		if b.runtime != nil {
			errs = append(errs, b.runtime.Close())
		}
		// Closing the Runner cancels the caller, but the Graph may still be in
		// the RTW acceptor. First stop new node registration and await its work.
		b.nodes.closeAndWait()
		b.activeWG.Wait()
		if b.attempts != nil {
			errs = append(errs, b.attempts.Close())
		}
		b.closeErr = errors.Join(errs...)
	})
	return b.closeErr
}
