package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const (
	rootSummaryRequestKey   = "root_summary_request"
	rootSearchResultKey     = "root_search_result"
	rootModelOutputKey      = "root_model_output"
	rootSummaryResultKey    = "root_summary_result"
	rootSearchNode          = "search_and_accept"
	rootSummaryAgent        = "search_summary"
	rootFinishNode          = "validate_answer"
	rootFastMediumGateNode  = "fast_medium_gate"
	rootFastMediumAgent     = "plan_fast_medium"
	rootFastMediumCheckNode = "validate_fast_medium_plan"
	rootFastMediumOutputKey = "fast_medium_model_output"
	rootFastMediumPlanKey   = "fast_medium_checked_queries"
)

var ErrRootSummaryOutput = errors.New("root search summary graph output missing or invalid")

// RootSummarizer owns one framework Runner over the whole search, citation and
// summary sequence. The source, citation acceptor, model, sessions and telemetry
// are borrowed; only this Runner is closed by Close.
type RootSummarizer struct {
	runtime    *btwruntime.Runtime
	delivery   *Delivery
	fastMedium *FastMediumModelLimits
}

// FastMediumModelLimits bounds one optional model planning stage. The search
// policy separately fixes one batch and three total query slots.
type FastMediumModelLimits struct {
	MaxOutputTokens int
	WallTime        time.Duration
}

// RootSummaryRunOption fixes the authoritative snapshot and public identity for
// one Runner invocation. The caller must obtain that snapshot before this call.
func RootSummaryRunOption(q SummaryRequest) (agent.RunOption, error) {
	if err := validateRootSummaryRequest(q); err != nil {
		return nil, err
	}
	q.Search.Snapshot = cloneSnapshot(q.Search.Snapshot)
	return agent.MergeRuntimeState(map[string]any{rootSummaryRequestKey: q}), nil
}

func validateRootSummaryRequest(q SummaryRequest) error {
	if q.SearchID == "" || q.AnswerID == "" || q.SessionID == "" ||
		strings.TrimSpace(q.Search.Query) == "" ||
		(q.Search.Depth != Fast && q.Search.Depth != Detailed) ||
		(q.Search.Intelligence != Low && q.Search.Intelligence != Medium && q.Search.Intelligence != High) ||
		!validSnapshot(q.Search.Snapshot) {
		return ErrInvalid
	}
	if _, err := q.Subject.UserKey(); err != nil {
		return err
	}
	return nil
}

// NewRootSummarizer assembles the v1.8.1 GraphAgent with one deterministic
// delivery node, a conditional no-evidence branch, a real no-tool LLMAgent
// sub-agent and a final contract-check node. Search itself remains a domain
// operation; the Graph and Runner own Agent execution and native observability.
func NewRootSummarizer(d *Delivery, m model.Model, sessions session.Service, observed *telemetry.Bundle,
	limits ...SummaryModelLimits) (*RootSummarizer, error) {
	return newRootSummarizer(d, m, nil, sessions, observed, nil, limits...)
}

// NewRootSummarizerWithFastMedium adds one native no-tool LLMAgent planning
// stage before the existing retrieval/citation/summary sequence.
func NewRootSummarizerWithFastMedium(d *Delivery, m, plannerModel model.Model, sessions session.Service, observed *telemetry.Bundle,
	medium FastMediumModelLimits, limits ...SummaryModelLimits) (*RootSummarizer, error) {
	if isNil(plannerModel) || medium.MaxOutputTokens < 64 || medium.MaxOutputTokens > 512 || medium.WallTime < time.Second || medium.WallTime > 30*time.Second {
		return nil, ErrInvalid
	}
	return newRootSummarizer(d, m, plannerModel, sessions, observed, &medium, limits...)
}

func newRootSummarizer(d *Delivery, m, plannerModel model.Model, sessions session.Service, observed *telemetry.Bundle,
	medium *FastMediumModelLimits, limits ...SummaryModelLimits) (*RootSummarizer, error) {
	if d == nil || isNil(m) || isNil(sessions) || observed == nil || !observed.Installed() {
		return nil, ErrInvalid
	}
	config := model.GenerationConfig{Stream: false, Temperature: model.Float64Ptr(0)}
	if len(limits) > 1 || len(limits) == 1 && (limits[0].MaxOutputTokens < 1 || limits[0].MaxOutputTokens > 8192) {
		return nil, ErrInvalid
	}
	if len(limits) == 1 {
		config.MaxTokens = model.IntPtr(limits[0].MaxOutputTokens)
	}
	summaryOptions := []llmagent.Option{llmagent.WithModel(m), llmagent.WithInstruction(summaryInstruction),
		llmagent.WithTools([]tool.Tool{}), llmagent.WithEnableCodeExecutionResponseProcessor(false),
		llmagent.WithGenerationConfig(config)}
	if medium != nil {
		summaryOptions = append(summaryOptions, llmagent.WithMaxLLMCalls(1))
	}
	summaryAgent := llmagent.New(rootSummaryAgent, summaryOptions...)
	schema := graph.NewStateSchema().
		AddField(rootSummaryRequestKey, graph.StateField{Type: reflect.TypeOf(SummaryRequest{}), Reducer: graph.DefaultReducer}).
		AddField(rootSearchResultKey, graph.StateField{Type: reflect.TypeOf(SearchResult{}), Reducer: graph.DefaultReducer}).
		AddField(rootModelOutputKey, graph.StateField{Type: reflect.TypeOf(""), Reducer: graph.DefaultReducer}).
		AddField(rootSummaryResultKey, graph.StateField{Type: reflect.TypeOf(SummaryResult{}), Reducer: graph.DefaultReducer}).
		AddField(graph.StateKeyLastResponse, graph.StateField{Type: reflect.TypeOf(""), Reducer: graph.DefaultReducer})
	if medium != nil {
		schema.AddField(rootFastMediumOutputKey, graph.StateField{Type: reflect.TypeOf(""), Reducer: graph.DefaultReducer}).
			AddField(rootFastMediumPlanKey, graph.StateField{Type: reflect.TypeOf([]string{}), Reducer: graph.DefaultReducer})
	}
	builder := graph.NewStateGraph(schema).
		AddNode(rootSearchNode, func(ctx context.Context, state graph.State) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			q, ok := graph.GetStateValue[SummaryRequest](state, rootSummaryRequestKey)
			if !ok || validateRootSummaryRequest(q) != nil {
				return nil, ErrInvalid
			}
			if medium != nil && q.Search.Depth == Fast && q.Search.Intelligence == Medium && len(q.Search.Snapshot.ValidRevisionIDs) > 0 {
				planned, ok := graph.GetStateValue[[]string](state, rootFastMediumPlanKey)
				if !ok {
					return nil, ErrFastMediumPlan
				}
				var err error
				ctx, err = WithFastMediumPlan(ctx, q.Search.Query, planned)
				if err != nil {
					return nil, err
				}
			}
			found, err := d.Search(ctx, q.SearchID, q.Search)
			if err != nil {
				return nil, fmt.Errorf("search and accept citations: %w", err)
			}
			update := graph.State{rootSearchResultKey: found}
			if len(found.Pack.Evidence) == 0 {
				return update, nil
			}
			if err := validateAcceptedPack(found, q.SearchID); err != nil {
				return nil, err
			}
			prompt := struct {
				Question               string          `json:"question"`
				Pack                   EvidencePack    `json:"fixed_evidence_pack"`
				AcceptedSessionHistory json.RawMessage `json:"accepted_session_history,omitempty"`
			}{Question: q.Search.Query, Pack: found.Pack}
			// The product boundary owns this seed; direct Summarize callers and
			// every rejected or unvalidated run can never place one here. A
			// seed that fails its own integrity check fails the search closed.
			if seed, ok := acceptedHistorySeedFromContext(ctx); ok {
				if err := historySeedIntegrity(seed); err != nil {
					return nil, err
				}
				if seed.Block != "" {
					prompt.AcceptedSessionHistory = json.RawMessage(seed.Block)
				}
			}
			raw, err := json.Marshal(prompt)
			if err != nil {
				return nil, fmt.Errorf("encode accepted evidence: %w", err)
			}
			update[graph.StateKeyLastResponse] = string(raw)
			return update, nil
		}).
		AddAgentNode(rootSummaryAgent, graph.WithSubgraphInputFromLastResponse(),
			graph.WithSubgraphIsolatedMessages(true),
			graph.WithSubgraphOutputMapper(func(_ graph.State, result graph.SubgraphResult) graph.State {
				return graph.State{rootModelOutputKey: result.LastResponse}
			})).
		AddNode(rootFinishNode, func(ctx context.Context, state graph.State) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			q, ok := graph.GetStateValue[SummaryRequest](state, rootSummaryRequestKey)
			if !ok || validateRootSummaryRequest(q) != nil {
				return nil, ErrInvalid
			}
			found, ok := graph.GetStateValue[SearchResult](state, rootSearchResultKey)
			if !ok {
				return nil, ErrRootSummaryOutput
			}
			out := SummaryResult{Search: found, AnswerID: q.AnswerID, SummaryStatus: "insufficient"}
			if len(found.Pack.Evidence) == 0 {
				return graph.State{rootSummaryResultKey: out}, nil
			}
			if err := validateAcceptedPack(found, q.SearchID); err != nil {
				return nil, err
			}
			raw, ok := graph.GetStateValue[string](state, rootModelOutputKey)
			if !ok {
				return nil, ErrSummary
			}
			answer, citations, err := validateSummaryJSON(raw, found.Pack)
			if err != nil {
				return nil, err
			}
			out.Answer, out.Citations, out.SummaryStatus = answer, citations, "succeeded"
			return graph.State{rootSummaryResultKey: out}, nil
		}).
		AddConditionalEdges(rootSearchNode, func(_ context.Context, state graph.State) (string, error) {
			found, ok := graph.GetStateValue[SearchResult](state, rootSearchResultKey)
			if !ok {
				return "", ErrRootSummaryOutput
			}
			if len(found.Pack.Evidence) == 0 {
				return "insufficient", nil
			}
			return "summary", nil
		}, map[string]string{"insufficient": rootFinishNode, "summary": rootSummaryAgent}).
		AddEdge(rootSummaryAgent, rootFinishNode).
		SetFinishPoint(rootFinishNode)
	subAgents := []agent.Agent{summaryAgent}
	if medium != nil {
		// Deterministic planning: the checked-queries contract must not vary
		// between retries of the same signed search.
		plannerConfig := model.GenerationConfig{Stream: false, MaxTokens: model.IntPtr(medium.MaxOutputTokens),
			Temperature: model.Float64Ptr(0)}
		plannerAgent := llmagent.New(rootFastMediumAgent, llmagent.WithModel(plannerModel),
			llmagent.WithInstruction(fastMediumInstruction), llmagent.WithTools([]tool.Tool{}),
			llmagent.WithEnableCodeExecutionResponseProcessor(false), llmagent.WithGenerationConfig(plannerConfig),
			llmagent.WithMaxLLMCalls(1))
		subAgents = append(subAgents, plannerAgent)
		builder = builder.
			AddNode(rootFastMediumGateNode, func(ctx context.Context, state graph.State) (any, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				q, ok := graph.GetStateValue[SummaryRequest](state, rootSummaryRequestKey)
				if !ok || validateRootSummaryRequest(q) != nil {
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
					return nil, fmt.Errorf("encode fast medium planning question: %w", err)
				}
				return graph.State{graph.StateKeyLastResponse: string(prompt)}, nil
			}).
			AddAgentNode(rootFastMediumAgent, graph.WithSubgraphInputFromLastResponse(),
				graph.WithSubgraphIsolatedMessages(true),
				graph.WithSubgraphOutputMapper(func(_ graph.State, result graph.SubgraphResult) graph.State {
					return graph.State{rootFastMediumOutputKey: result.LastResponse}
				})).
			AddNode(rootFastMediumCheckNode, func(ctx context.Context, state graph.State) (any, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				q, ok := graph.GetStateValue[SummaryRequest](state, rootSummaryRequestKey)
				raw, outputOK := graph.GetStateValue[string](state, rootFastMediumOutputKey)
				if !ok || !outputOK || q.Search.Depth != Fast || q.Search.Intelligence != Medium {
					return nil, ErrFastMediumPlan
				}
				queries, err := ParseFastMediumPlan(raw, q.Search.Query)
				if err != nil {
					return nil, err
				}
				return graph.State{rootFastMediumPlanKey: queries}, nil
			}).
			AddConditionalEdges(rootFastMediumGateNode, func(_ context.Context, state graph.State) (string, error) {
				q, ok := graph.GetStateValue[SummaryRequest](state, rootSummaryRequestKey)
				if !ok {
					return "", ErrInvalid
				}
				if q.Search.Depth == Fast && q.Search.Intelligence == Medium && len(q.Search.Snapshot.ValidRevisionIDs) > 0 {
					return "plan", nil
				}
				return "direct", nil
			}, map[string]string{"plan": rootFastMediumAgent, "direct": rootSearchNode}).
			AddEdge(rootFastMediumAgent, rootFastMediumCheckNode).
			AddEdge(rootFastMediumCheckNode, rootSearchNode).
			SetEntryPoint(rootFastMediumGateNode)
	} else {
		builder = builder.SetEntryPoint(rootSearchNode)
	}
	compiled, err := builder.Compile()
	if err != nil {
		return nil, fmt.Errorf("compile root search summary graph: %w", err)
	}
	ag, err := graphagent.New("search_summary_root", compiled, graphagent.WithSubAgents(subAgents))
	if err != nil {
		return nil, fmt.Errorf("create root search summary agent: %w", err)
	}
	r, err := btwruntime.New("search_summary_root", ag, sessions, observed)
	if err != nil {
		return nil, err
	}
	root := &RootSummarizer{runtime: r, delivery: d}
	if medium != nil {
		owned := *medium
		root.fastMedium = &owned
	}
	return root, nil
}

// Summarize consumes the complete framework event stream before returning an
// answer. Raw model events never leave this method and cannot become public
// output before the citation receipt and final validation are both complete.
func (s *RootSummarizer) Summarize(ctx context.Context, q SummaryRequest) (SummaryResult, error) {
	out := SummaryResult{AnswerID: q.AnswerID, SummaryStatus: "failed"}
	if s == nil || s.runtime == nil || s.delivery == nil || ctx == nil {
		return out, ErrInvalid
	}
	option, err := RootSummaryRunOption(q)
	if err != nil {
		return out, err
	}
	profile, checked, err := s.delivery.preflightProfile(q.Search)
	if err != nil {
		return out, err
	}
	// The public Summary response has no effective-level/change-reason fields.
	// Even an RTW-signed AllowLower flag cannot make a downgraded execution look
	// like the requested medium or high product until that response is versioned.
	if checked && profile.EffectiveIntelligence != q.Search.Intelligence {
		return out, ErrUnavailable
	}
	if checked && s.fastMedium != nil && profile.EffectiveIntelligence == Medium &&
		(q.Search.Depth != Fast || q.Search.Intelligence != Medium) {
		return out, ErrUnavailable
	}
	if s.fastMedium != nil && q.Search.Depth == Fast && q.Search.Intelligence == Medium {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.fastMedium.WallTime)
		defer cancel()
	}
	fixed := q
	fixed.Search.Snapshot = cloneSnapshot(q.Search.Snapshot)
	invocation, err := invocationForSummary(fixed)
	if err != nil {
		return out, err
	}
	ctx, err = WithModelInvocationRef(ctx, invocation)
	if err != nil {
		return out, err
	}
	var completed int
	var candidate SummaryResult
	_, err = s.runtime.Run(ctx, btwruntime.Request{Subject: q.Subject, SessionID: q.SessionID,
		RunID: q.AnswerID, Message: model.NewUserMessage(q.Search.Query), Options: []agent.RunOption{option}},
		func(_ context.Context, e *event.Event) error {
			if !graph.IsGraphCompletionEvent(e) {
				return nil
			}
			completed++
			raw, ok := e.StateDelta[rootSummaryResultKey]
			if !ok {
				return ErrRootSummaryOutput
			}
			if err := json.Unmarshal(raw, &candidate); err != nil {
				return fmt.Errorf("decode root graph result: %w", err)
			}
			if !validRootSummaryResult(candidate, fixed) {
				return ErrRootSummaryOutput
			}
			return nil
		})
	if err != nil {
		return out, fmt.Errorf("run root search summary graph: %w", err)
	}
	if completed != 1 || !validRootSummaryResult(candidate, fixed) {
		return out, ErrRootSummaryOutput
	}
	// A non-Service SearchExecutor may not offer the early profile check. Keep
	// the public Summary boundary exact even then; old accepted turns remain
	// readable through History's separate validation contract.
	if candidate.Search.Retrieval.Profile.EffectiveIntelligence != fixed.Search.Intelligence {
		return out, ErrUnavailable
	}
	return candidate, nil
}

func validateAcceptedPack(found SearchResult, searchID string) error {
	if found.Pack.SearchID != searchID || len(found.Pack.Evidence) == 0 {
		return ErrReceipt
	}
	hash, err := found.Pack.Hash()
	if err != nil || found.Receipt.SearchID != searchID ||
		found.Receipt.PackHash != hash || found.Receipt.DurableRef == "" {
		return ErrReceipt
	}
	return nil
}

func validateSummaryJSON(raw string, pack EvidencePack) (string, []string, error) {
	var body struct {
		Answer    string   `json:"answer"`
		Citations []string `json:"citations"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF ||
		strings.TrimSpace(body.Answer) == "" || len(body.Citations) == 0 {
		return "", nil, ErrSummary
	}
	allowed := make(map[string]bool, len(pack.Evidence))
	for _, evidence := range pack.Evidence {
		allowed[evidence.ID] = true
	}
	seen := make(map[string]bool, len(body.Citations))
	for _, id := range body.Citations {
		if !allowed[id] || seen[id] {
			return "", nil, ErrSummary
		}
		seen[id] = true
	}
	return body.Answer, body.Citations, nil
}

func validRootSummaryResult(out SummaryResult, q SummaryRequest) bool {
	if out.AnswerID != q.AnswerID || !validGraphResult(out.Search.Retrieval, q.Search) ||
		out.Search.Pack.SearchID != q.SearchID ||
		!reflect.DeepEqual(out.Search.Pack.Snapshot, q.Search.Snapshot) {
		return false
	}
	if len(out.Search.Pack.Evidence) == 0 {
		return out.Search.Pack.Status == "empty" && out.Search.Receipt == (CitationReceipt{}) &&
			out.SummaryStatus == "insufficient" && out.Answer == "" && len(out.Citations) == 0
	}
	if validateAcceptedPack(out.Search, q.SearchID) != nil || out.SummaryStatus != "succeeded" {
		return false
	}
	allowed := make(map[string]bool, len(out.Search.Pack.Evidence))
	for _, evidence := range out.Search.Pack.Evidence {
		allowed[evidence.ID] = true
	}
	if strings.TrimSpace(out.Answer) == "" || len(out.Citations) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range out.Citations {
		if !allowed[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (s *RootSummarizer) Close() error {
	if s == nil || s.runtime == nil {
		return nil
	}
	return s.runtime.Close()
}
