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
	"time"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const (
	toolRunRequestKey       = "tool_search_request"
	toolRunResultKey        = "tool_search_result"
	toolRunNode             = "search_and_accept_tool_evidence"
	toolRunApp              = "search_tools_root"
	toolFastMediumGateNode  = "fast_medium_tools_gate"
	toolFastMediumAgent     = "plan_fast_medium_tools"
	toolFastMediumCheckNode = "validate_fast_medium_tools_plan"
	toolFastMediumOutputKey = "fast_medium_tools_model_output"
	toolFastMediumPlanKey   = "fast_medium_tools_checked_queries"
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
		found.Retrieval.Profile.EffectiveIntelligence != q.Search.Intelligence ||
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
	runtime    *btwruntime.Runtime
	delivery   *Delivery
	fastMedium *FastMediumModelLimits
	attempts   *inmemory.SessionService
	nodes      *toolRunNodeLifetime
	mu         sync.Mutex
	closed     bool
	next       uint64
	active     map[uint64]context.CancelFunc
	activeWG   sync.WaitGroup
	closeOnce  sync.Once
	closeErr   error
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
	return newToolRunBoundary(delivery, nil, observed, nil)
}

// NewToolRunBoundaryWithFastMedium adds one no-tool native Planner stage only
// for a separately enabled fast/medium Tools product route. Low keeps its
// original graph path and the caller still owns the borrowed model and RTW.
func NewToolRunBoundaryWithFastMedium(delivery *Delivery, plannerModel model.Model, observed *telemetry.Bundle,
	medium FastMediumModelLimits) (*ToolRunBoundary, error) {
	if isNil(plannerModel) || medium.MaxOutputTokens < 64 || medium.MaxOutputTokens > 512 ||
		medium.WallTime < time.Second || medium.WallTime > 30*time.Second {
		return nil, ErrInvalid
	}
	return newToolRunBoundary(delivery, plannerModel, observed, &medium)
}

func newToolRunBoundary(delivery *Delivery, plannerModel model.Model, observed *telemetry.Bundle,
	medium *FastMediumModelLimits) (*ToolRunBoundary, error) {
	if delivery == nil || observed == nil || !observed.Installed() || observed.Closed() {
		return nil, ErrInvalid
	}
	nodes := &toolRunNodeLifetime{}
	schema := graph.NewStateSchema().
		AddField(toolRunRequestKey, graph.StateField{Type: reflect.TypeOf(ToolRunRequest{}), Reducer: graph.DefaultReducer}).
		AddField(toolRunResultKey, graph.StateField{Type: reflect.TypeOf(SearchResult{}), Reducer: graph.DefaultReducer})
	if medium != nil {
		schema.AddField(toolFastMediumOutputKey, graph.StateField{Type: reflect.TypeOf(""), Reducer: graph.DefaultReducer}).
			AddField(toolFastMediumPlanKey, graph.StateField{Type: reflect.TypeOf([]string{}), Reducer: graph.DefaultReducer}).
			AddField(graph.StateKeyLastResponse, graph.StateField{Type: reflect.TypeOf(""), Reducer: graph.DefaultReducer})
	}
	builder := graph.NewStateGraph(schema).
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
			if medium != nil && q.Search.Depth == Fast && q.Search.Intelligence == Medium && len(q.Search.Snapshot.ValidRevisionIDs) > 0 {
				planned, ok := graph.GetStateValue[[]string](state, toolFastMediumPlanKey)
				if !ok {
					return nil, ErrFastMediumPlan
				}
				plannedCtx, planErr := WithFastMediumPlan(ctx, q.Search.Query, planned)
				if planErr != nil {
					return nil, planErr
				}
				ctx = plannedCtx
			}
			found, err := delivery.SearchWithin(ctx, q.SearchID, q.Search, q.Limits)
			if err != nil {
				return nil, fmt.Errorf("search and accept tool evidence: %w", err)
			}
			if !validToolRunResult(found, q) {
				return nil, ErrToolRunOutput
			}
			return graph.State{toolRunResultKey: found}, nil
		}).SetFinishPoint(toolRunNode)
	var subAgents []agent.Agent
	if medium != nil {
		plannerConfig := model.GenerationConfig{Stream: false, MaxTokens: model.IntPtr(medium.MaxOutputTokens)}
		planner := llmagent.New(toolFastMediumAgent, llmagent.WithModel(plannerModel),
			llmagent.WithInstruction(fastMediumInstruction), llmagent.WithTools([]tool.Tool{}),
			llmagent.WithEnableCodeExecutionResponseProcessor(false), llmagent.WithGenerationConfig(plannerConfig),
			llmagent.WithMaxLLMCalls(1))
		subAgents = []agent.Agent{planner}
		builder = builder.
			AddNode(toolFastMediumGateNode, func(ctx context.Context, state graph.State) (any, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				q, ok := graph.GetStateValue[ToolRunRequest](state, toolRunRequestKey)
				if !ok || !validToolRunRequest(q) {
					return nil, ErrInvalid
				}
				if q.Search.Depth != Fast || q.Search.Intelligence != Medium || len(q.Search.Snapshot.ValidRevisionIDs) == 0 {
					return graph.State{}, nil
				}
				prompt, err := json.Marshal(struct {
					Question      string `json:"question"`
					MaxNewQueries int    `json:"max_new_queries"`
				}{q.Search.Query, maxFastMediumNewQueries})
				if err != nil {
					return nil, fmt.Errorf("encode fast medium Tools question: %w", err)
				}
				return graph.State{graph.StateKeyLastResponse: string(prompt)}, nil
			}).
			AddAgentNode(toolFastMediumAgent, graph.WithSubgraphInputFromLastResponse(),
				graph.WithSubgraphIsolatedMessages(true),
				graph.WithSubgraphOutputMapper(func(_ graph.State, result graph.SubgraphResult) graph.State {
					return graph.State{toolFastMediumOutputKey: result.LastResponse}
				})).
			AddNode(toolFastMediumCheckNode, func(ctx context.Context, state graph.State) (any, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				q, ok := graph.GetStateValue[ToolRunRequest](state, toolRunRequestKey)
				raw, outputOK := graph.GetStateValue[string](state, toolFastMediumOutputKey)
				if !ok || !outputOK || q.Search.Depth != Fast || q.Search.Intelligence != Medium {
					return nil, ErrFastMediumPlan
				}
				queries, err := ParseFastMediumPlan(raw, q.Search.Query)
				if err != nil {
					return nil, err
				}
				return graph.State{toolFastMediumPlanKey: queries}, nil
			}).
			AddConditionalEdges(toolFastMediumGateNode, func(_ context.Context, state graph.State) (string, error) {
				q, ok := graph.GetStateValue[ToolRunRequest](state, toolRunRequestKey)
				if !ok {
					return "", ErrInvalid
				}
				if q.Search.Depth == Fast && q.Search.Intelligence == Medium && len(q.Search.Snapshot.ValidRevisionIDs) > 0 {
					return "plan", nil
				}
				return "direct", nil
			}, map[string]string{"plan": toolFastMediumAgent, "direct": toolRunNode}).
			AddEdge(toolFastMediumAgent, toolFastMediumCheckNode).
			AddEdge(toolFastMediumCheckNode, toolRunNode).
			SetEntryPoint(toolFastMediumGateNode)
	} else {
		builder = builder.SetEntryPoint(toolRunNode)
	}
	compiled, err := builder.Compile()
	if err != nil {
		return nil, fmt.Errorf("compile tool search graph: %w", err)
	}
	var ag *graphagent.GraphAgent
	if medium == nil {
		ag, err = graphagent.New(toolRunApp, compiled)
	} else {
		ag, err = graphagent.New(toolRunApp, compiled, graphagent.WithSubAgents(subAgents))
	}
	if err != nil {
		return nil, fmt.Errorf("construct tool search agent: %w", err)
	}
	attempts := inmemory.NewSessionService()
	runtime, err := btwruntime.New(toolRunApp, ag, attempts, observed)
	if err != nil {
		_ = attempts.Close()
		return nil, err
	}
	b := &ToolRunBoundary{runtime: runtime, delivery: delivery, attempts: attempts, nodes: nodes,
		active: make(map[uint64]context.CancelFunc)}
	if medium != nil {
		owned := *medium
		b.fastMedium = &owned
	}
	return b, nil
}

// PreflightProfile checks the fixed local policy before any Runner/model or
// Reader effect. Medium requires a Service-backed exact profile; an RTW-signed
// allow-lower flag cannot silently change the public Tools intelligence.
func (b *ToolRunBoundary) PreflightProfile(ctx context.Context, request Request) error {
	if b == nil || b.delivery == nil || ctx == nil {
		return ErrInvalid
	}
	if request.Depth != Fast || (request.Intelligence != Low && request.Intelligence != Medium) {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.Intelligence == Medium && b.fastMedium == nil {
		return ErrUnavailable
	}
	profile, checked, err := b.delivery.preflightProfile(request)
	if err != nil {
		return err
	}
	if request.Intelligence == Medium && !checked {
		return ErrUnavailable
	}
	if checked && profile.EffectiveIntelligence != request.Intelligence {
		return ErrUnavailable
	}
	return nil
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
	if err := b.PreflightProfile(ctx, q.Search); err != nil {
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
	if b.fastMedium != nil && q.Search.Depth == Fast && q.Search.Intelligence == Medium {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.fastMedium.WallTime)
		defer cancel()
		ref, err := invocationForTool(q)
		if err != nil {
			return SearchResult{}, err
		}
		ctx, err = WithModelInvocationRef(ctx, ref)
		if err != nil {
			return SearchResult{}, err
		}
	}
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
