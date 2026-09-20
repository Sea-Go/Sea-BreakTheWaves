package recommendationv2

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	trpcagent "trpc.group/trpc-go/trpc-agent-go/agent"
	trpcgraph "trpc.group/trpc-go/trpc-agent-go/graph"
)

const (
	stateRequest  = "request"
	stateConfig   = "config"
	stateProfile  = "profile"
	statePath     = "path"
	stateItems    = "items"
	stateResponse = "response"
	stateTraceID  = "trace_id"
	stateSteps    = "steps"
	stateCost     = "cost"
	stateFallback = "fallback"
	stateStream   = "stream"
)

var errNoRecommendationItems = errors.New("recommendation v2: no items returned")

type ProfileProvider interface {
	LoadProfile(ctx context.Context, user UserIdentity) (ProfileSnapshot, error)
}

type ProfileUpdater interface {
	UpdateProfile(ctx context.Context, event BehaviorEvent) error
}

type RecommendationSkill interface {
	Name() string
	Category() string
	CostLevel() string
	AllowedPath(path string) bool
}

type RecommendationContext struct {
	Request RecommendRequest
	Config  EffectiveRecommendConfig
	Profile ProfileSnapshot
	Path    string
	Items   []RecommendItem
	Cost    CostReport
}

type RecallProvider interface {
	Recall(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error)
}

type RankProvider interface {
	Rank(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error)
}

type RerankProvider interface {
	Rerank(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error)
}

type QualityProvider interface {
	Score(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, *FallbackReport, error)
}

type ExplainProvider interface {
	Explain(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, []Explanation, error)
}

type InMemoryProfileStore struct{}

func (InMemoryProfileStore) LoadProfile(_ context.Context, user UserIdentity) (ProfileSnapshot, error) {
	now := time.Now().UTC()
	return ProfileSnapshot{
		User: user,
		Profile: UserProfile{
			Static: map[string]any{
				"tenant_id": user.TenantID,
				"locale":    user.Locale,
			},
			Dynamic: map[string]any{
				"channel": user.Channel,
			},
			Behavior:  map[string]any{},
			Temporal:  map[string]any{},
			UpdatedAt: now,
		},
		SnapshotID: "profile_" + randID(),
		CreatedAt:  now,
	}, nil
}

func (InMemoryProfileStore) UpdateProfile(context.Context, BehaviorEvent) error { return nil }

type defaultRecallProvider struct{}

func (defaultRecallProvider) Recall(_ context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error) {
	req := rctx.Request
	cfg := rctx.Config
	topK := cfg.Recall.TopK
	if topK <= 0 {
		topK = 50
	}
	sources := cfg.Recall.Sources
	if len(sources) == 0 {
		sources = []string{cfg.Recall.FallbackSource}
	}
	items := make([]RecommendItem, 0, topK)
	for i := 0; i < topK; i++ {
		source := sources[i%len(sources)]
		id := fmt.Sprintf("%s_%02d", source, i+1)
		if req.Query != "" {
			id = fmt.Sprintf("%s_%02d_%s", source, i+1, sanitizeID(req.Query))
		}
		items = append(items, RecommendItem{
			ID:        id,
			ArticleID: id,
			Score:     1 - float64(i)*0.01,
			Source:    source,
			Features: map[string]any{
				"recall_source": source,
				"scenario":      req.Scenario,
				"path":          rctx.Path,
			},
		})
	}
	cost := rctx.Cost
	cost.ToolCalls++
	cost.VectorQueries++
	if rctx.Path == PathHybrid {
		cost.GraphQueries++
	}
	return items, cost, nil
}

type defaultRankProvider struct{}

func (defaultRankProvider) Rank(_ context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error) {
	items := cloneItems(rctx.Items)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Score > items[j].Score
	})
	for i := range items {
		items[i].Rank = i + 1
		items[i].Score += rankBoost(items[i].Source)
	}
	cost := rctx.Cost
	cost.ToolCalls++
	return items, cost, nil
}

type defaultRerankProvider struct{}

func (defaultRerankProvider) Rerank(_ context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error) {
	cfg := rctx.Config
	items := cloneItems(rctx.Items)
	limit := cfg.Rerank.TopN
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	items = items[:limit]
	for i := range items {
		items[i].Score += 0.05
		items[i].Reason = "reranked by " + cfg.Rerank.Model
		items[i].Features = mergeAnyMap(items[i].Features, map[string]any{"rerank_model": cfg.Rerank.Model})
	}
	cost := rctx.Cost
	cost.ToolCalls++
	cost.LLMCalls++
	cost.TokensIn += 120
	cost.TokensOut += 80
	cost.RerankCost += cfg.Rerank.Budget
	cost.EstimatedAmount += cfg.Rerank.Budget
	return items, cost, nil
}

type defaultQualityProvider struct{}

func (defaultQualityProvider) Score(_ context.Context, rctx RecommendationContext) ([]RecommendItem, *FallbackReport, error) {
	items := cloneItems(rctx.Items)
	if len(items) == 0 {
		fallback := &FallbackReport{Triggered: true, Reason: errNoRecommendationItems.Error(), Source: "rule"}
		return items, fallback, nil
	}
	for i := range items {
		items[i].Features = mergeAnyMap(items[i].Features, map[string]any{"quality_score": 0.85})
	}
	return items, nil, nil
}

type defaultExplainProvider struct{}

func (defaultExplainProvider) Explain(_ context.Context, rctx RecommendationContext) ([]RecommendItem, []Explanation, error) {
	items := cloneItems(rctx.Items)
	explanations := make([]Explanation, 0, len(items))
	for i := range items {
		if items[i].Reason == "" {
			items[i].Reason = "matched by " + items[i].Source + " under trpc-agent-go StateGraph"
		}
		explanations = append(explanations, Explanation{
			ItemID: items[i].ID,
			Text:   items[i].Reason,
			Source: items[i].Source,
		})
	}
	return items, explanations, nil
}

type RecommendationRuntime struct {
	config   RecommendConfig
	configs  ConfigProvider
	profiles ProfileProvider
	updater  ProfileUpdater
	skills   *SkillRegistry
	policy   ToolPolicy
	recall   RecallProvider
	rank     RankProvider
	rerank   RerankProvider
	quality  QualityProvider
	explain  ExplainProvider
	events   *EventBus
	activity ActivityAnalyzer
	obs      *observationCollector
	graph    *trpcgraph.Graph
	streamMu sync.RWMutex
	streams  map[string]chan<- StreamEvent
}

func NewRecommendationRuntime(opts ...Option) *RecommendationRuntime {
	rt := &RecommendationRuntime{
		config:   DefaultConfig(),
		profiles: InMemoryProfileStore{},
		updater:  InMemoryProfileStore{},
		skills:   NewSkillRegistry(),
		policy:   NewStaticToolPolicy(),
		recall:   defaultRecallProvider{},
		rank:     defaultRankProvider{},
		rerank:   defaultRerankProvider{},
		quality:  defaultQualityProvider{},
		explain:  defaultExplainProvider{},
		events:   NewEventBus(nil),
		obs:      newObservationCollector(),
		streams:  make(map[string]chan<- StreamEvent),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(rt)
		}
	}
	rt.attachEventReporter()
	rt.graph = rt.mustBuildGraph()
	return rt
}

func (rt *RecommendationRuntime) attachEventReporter() {
	if rt == nil || rt.events == nil {
		return
	}
	rt.events.SetFailureReporter(func(event BehaviorEvent, hookName string, mode HookMode, err error) {
		if rt.obs != nil {
			rt.obs.recordHookFailure(event, hookName, mode)
		}
	})
}

type Option func(*RecommendationRuntime)

func WithConfig(cfg RecommendConfig) Option {
	return func(rt *RecommendationRuntime) { rt.config = cfg }
}

func WithConfigProvider(provider ConfigProvider) Option {
	return func(rt *RecommendationRuntime) {
		if provider != nil {
			rt.configs = provider
		}
	}
}

func WithProfileProvider(provider ProfileProvider) Option {
	return func(rt *RecommendationRuntime) {
		if provider != nil {
			rt.profiles = provider
		}
	}
}

func WithProfileUpdater(updater ProfileUpdater) Option {
	return func(rt *RecommendationRuntime) {
		if updater != nil {
			rt.updater = updater
		}
	}
}

func WithSkillRegistry(registry *SkillRegistry) Option {
	return func(rt *RecommendationRuntime) {
		if registry != nil {
			rt.skills = registry
		}
	}
}

func WithSkillDirectory(path string) Option {
	return func(rt *RecommendationRuntime) {
		if rt == nil || rt.skills == nil || strings.TrimSpace(path) == "" {
			return
		}
		if err := rt.skills.LoadDirectory(path); err != nil {
			panic(err)
		}
	}
}

func WithToolPolicy(policy ToolPolicy) Option {
	return func(rt *RecommendationRuntime) {
		if policy != nil {
			rt.policy = policy
		}
	}
}

func WithEventBus(bus *EventBus) Option {
	return func(rt *RecommendationRuntime) {
		if bus != nil {
			rt.events = bus
		}
	}
}

// WithActivityAnalyzer installs the synchronous raw-event analysis boundary.
// It is intentionally separate from asynchronous event hooks because the
// resulting score must be returned to the local trigger in the same request.
func WithActivityAnalyzer(analyzer ActivityAnalyzer) Option {
	return func(rt *RecommendationRuntime) {
		if analyzer != nil {
			rt.activity = analyzer
		}
	}
}

func (rt *RecommendationRuntime) registerAsyncHook(h Hook) {
	if rt == nil || h == nil {
		return
	}
	if rt.events == nil {
		rt.events = NewEventBus(nil)
	}
	rt.events.RegisterAsync(h)
}

func WithRecallProvider(provider RecallProvider) Option {
	return func(rt *RecommendationRuntime) {
		if provider != nil {
			rt.recall = provider
		}
	}
}

func WithRankProvider(provider RankProvider) Option {
	return func(rt *RecommendationRuntime) {
		if provider != nil {
			rt.rank = provider
		}
	}
}

func WithRerankProvider(provider RerankProvider) Option {
	return func(rt *RecommendationRuntime) {
		if provider != nil {
			rt.rerank = provider
		}
	}
}

func WithQualityProvider(provider QualityProvider) Option {
	return func(rt *RecommendationRuntime) {
		if provider != nil {
			rt.quality = provider
		}
	}
}

func WithExplainProvider(provider ExplainProvider) Option {
	return func(rt *RecommendationRuntime) {
		if provider != nil {
			rt.explain = provider
		}
	}
}

func (rt *RecommendationRuntime) Recommend(ctx context.Context, req RecommendRequest) (RecommendResponse, error) {
	return rt.recommend(ctx, req, nil)
}

func (rt *RecommendationRuntime) StreamRecommend(ctx context.Context, req RecommendRequest) (<-chan StreamEvent, error) {
	if rt == nil {
		return nil, fmt.Errorf("recommendation v2: nil runtime")
	}
	ch := make(chan StreamEvent, 32)
	go func() {
		defer close(ch)
		_, _ = rt.recommend(ctx, req, ch)
	}()
	return ch, nil
}

func (rt *RecommendationRuntime) recommend(ctx context.Context, req RecommendRequest, stream chan<- StreamEvent) (RecommendResponse, error) {
	if rt == nil {
		return RecommendResponse{}, fmt.Errorf("recommendation v2: nil runtime")
	}
	start := time.Now().UTC()
	normalized, err := normalizeRequest(req)
	if err != nil {
		return RecommendResponse{}, err
	}
	req = normalized
	cfg, err := rt.resolveConfig(ctx, req)
	if err != nil {
		return RecommendResponse{}, err
	}
	traceID := "trace_" + randID()
	ctx, rootSpan := otel.Tracer("sea/recommendationv2").Start(ctx, "recommendation.v2.run")
	rootSpan.SetAttributes(
		attribute.String("sea.trace_id", traceID),
		attribute.String("recommendation.request_id", req.RequestID),
		attribute.String("recommendation.tenant_id", req.TenantID),
		attribute.String("recommendation.channel", req.Channel),
		attribute.String("recommendation.scenario", req.Scenario),
		attribute.String("recommendation.config_path_mode", cfg.PathMode),
		attribute.Int("recommendation.top_k", req.TopK),
	)
	defer rootSpan.End()
	rt.registerStream(traceID, stream)
	defer rt.unregisterStream(traceID)
	emitStream(stream, StreamEvent{
		Type:      "run_started",
		TraceID:   traceID,
		RequestID: req.RequestID,
		Metadata:  map[string]any{"framework": "trpc-agent-go", "graph": "RecommendationGraph"},
	})

	initialState := trpcgraph.State{
		stateRequest:  req,
		stateConfig:   cfg,
		stateTraceID:  traceID,
		stateSteps:    []TraceStep{},
		stateCost:     CostReport{},
		stateFallback: (*FallbackReport)(nil),
		stateStream:   stream,
	}
	invocation := &trpcagent.Invocation{
		InvocationID: traceID,
		AgentName:    "RecommendationGraph",
		MaxLLMCalls:  maxLLMCallsForConfig(cfg),
	}
	executor, err := trpcgraph.NewExecutor(rt.graph)
	if err != nil {
		return RecommendResponse{}, err
	}
	eventCh, err := executor.Execute(ctx, initialState, invocation)
	if err != nil {
		return RecommendResponse{}, err
	}

	var finalState trpcgraph.State
	var graphErr error
	for ev := range eventCh {
		if ev == nil {
			continue
		}
		if ev.Error != nil {
			graphErr = errors.New(ev.Error.Message)
			rt.publish(ctx, eventFromValues(req, traceID, stateString(finalState, statePath), EventError, map[string]any{"error": ev.Error.Message}))
			continue
		}
		if ev.Done {
			finalState = stateFromDelta(ev.StateDelta)
			break
		}
	}
	if finalState == nil {
		if graphErr == nil {
			graphErr = fmt.Errorf("recommendation v2: graph did not complete")
		}
		rootSpan.RecordError(graphErr)
		rootSpan.SetStatus(codes.Error, graphErr.Error())
		return rt.errorResponse(ctx, req, traceID, "", initialState, start, graphErr, stream)
	}
	if resp, ok := finalState[stateResponse].(RecommendResponse); ok {
		resp.Metrics.LatencyMillis = time.Since(start).Milliseconds()
		rt.obs.recordRecommendation(resp)
		rootSpan.SetAttributes(
			attribute.String("recommendation.path_taken", resp.PathTaken),
			attribute.Int("recommendation.item_count", len(resp.Items)),
			attribute.Bool("recommendation.fallback", resp.Fallback != nil),
			attribute.Int("recommendation.llm_calls", resp.Cost.LLMCalls),
			attribute.Int("recommendation.tool_calls", resp.Cost.ToolCalls),
			attribute.Float64("recommendation.estimated_amount", resp.Cost.EstimatedAmount),
		)
		rootSpan.SetStatus(codes.Ok, "ok")
		emitStream(stream, StreamEvent{Type: "run_finished", TraceID: resp.TraceID, RequestID: resp.RequestID, Response: &resp})
		return resp, nil
	}
	err = fmt.Errorf("recommendation v2: missing response")
	rootSpan.RecordError(err)
	rootSpan.SetStatus(codes.Error, err.Error())
	return rt.errorResponse(ctx, req, traceID, stateString(finalState, statePath), finalState, start, err, stream)
}

func (rt *RecommendationRuntime) RecordEvents(ctx context.Context, req EventBatchRequest) EventBatchResponse {
	resp := EventBatchResponse{}
	for _, ev := range req.Events {
		if ev.EventID == "" {
			ev.EventID = "evt_" + randID()
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = time.Now().UTC()
		}
		ok, failures := rt.events.Publish(ctx, ev)
		if !ok {
			resp.Rejected = append(resp.Rejected, failures...)
			rt.obs.recordBehaviorEvent(ev, false)
			continue
		}
		if rt.updater != nil {
			if err := rt.updater.UpdateProfile(ctx, ev); err != nil {
				resp.Rejected = append(resp.Rejected, err.Error())
				rt.obs.recordBehaviorEvent(ev, false)
				continue
			}
		}
		rt.obs.recordBehaviorEvent(ev, true)
		resp.Accepted++
		if len(ev.RawEvent) > 0 {
			resp.ActivityAnalysis = append(resp.ActivityAnalysis, rt.analyzeActivityEvent(ctx, ev))
		}
	}
	rt.obs.recordEvents(resp.Accepted, len(resp.Rejected))
	return resp
}

func (rt *RecommendationRuntime) analyzeActivityEvent(ctx context.Context, event BehaviorEvent) ActivityAnalysisResult {
	requestID, requestIDErr := activityRequestID(event.EventID)
	if requestIDErr != nil {
		if rt != nil && rt.obs != nil {
			rt.obs.recordActivityAnalysis(false, false)
		}
		return ActivityAnalysisResult{Status: "failed", ErrorCode: "invalid_event_id"}
	}
	if rt == nil || rt.activity == nil {
		if rt != nil && rt.obs != nil {
			rt.obs.recordActivityAnalysis(false, false)
		}
		return ActivityAnalysisResult{Status: "disabled", RequestID: requestID, ErrorCode: "activity_worker_disabled"}
	}
	result, err := rt.activity.Analyze(ctx, ActivityAnalysisInput{
		EventID:         event.EventID,
		TenantID:        event.TenantID,
		User:            event.User,
		RawEvent:        event.RawEvent,
		ActivityContext: event.ActivityContext,
	})
	if err != nil {
		if rt.obs != nil {
			rt.obs.recordActivityAnalysis(false, false)
		}
		return ActivityAnalysisResult{Status: "failed", RequestID: requestID, ErrorCode: activityErrorCode(err)}
	}
	if rt.obs != nil {
		rt.obs.recordActivityAnalysis(result.Status == "analyzed", result.ShouldCallAgent)
	}
	return result
}

func activityErrorCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, errActivityInvalidInput):
		return "invalid_raw_event"
	case errors.Is(err, errActivityContract):
		return "invalid_worker_response"
	default:
		return "worker_unavailable"
	}
}

func (rt *RecommendationRuntime) Summary() ObservationSummary {
	if rt == nil {
		return ObservationSummary{ByPath: map[string]int64{}}
	}
	return rt.obs.summary()
}

func (rt *RecommendationRuntime) ListSkills() []SkillDefinition {
	if rt == nil || rt.skills == nil {
		return nil
	}
	return rt.skills.List()
}

func (rt *RecommendationRuntime) Trace(query TraceQueryRequest) TraceQueryResponse {
	if rt == nil || rt.obs == nil {
		return TraceQueryResponse{}
	}
	return rt.obs.trace(query)
}

func (rt *RecommendationRuntime) resolveConfig(ctx context.Context, req RecommendRequest) (EffectiveRecommendConfig, error) {
	cfg := rt.config
	if rt.configs != nil {
		scoped, err := rt.configs.ConfigFor(ctx, ConfigScope{TenantID: req.TenantID, Channel: req.Channel, Scenario: req.Scenario})
		if err != nil {
			return EffectiveRecommendConfig{}, err
		}
		cfg = MergeConfig(cfg, scoped)
	}
	cfg = MergeConfig(cfg, req.Config)
	if req.PathMode != "" {
		cfg.PathMode = &req.PathMode
	}
	return EffectiveConfig(cfg), nil
}

func (rt *RecommendationRuntime) mustBuildGraph() *trpcgraph.Graph {
	graph, err := rt.buildGraph()
	if err != nil {
		panic(err)
	}
	return graph
}

func (rt *RecommendationRuntime) buildGraph() (*trpcgraph.Graph, error) {
	sg := trpcgraph.NewStateGraph(trpcgraph.NewStateSchema())
	sg.
		AddNode("normalize_request", rt.wrapNode("normalize_request", "function", rt.nodeNormalizeRequest), trpcgraph.WithNodeType(trpcgraph.NodeTypeFunction)).
		AddNode("load_user_profile", rt.wrapNode("load_user_profile", "tool", rt.nodeLoadProfile), trpcgraph.WithNodeType(trpcgraph.NodeTypeTool)).
		AddNode("route", rt.wrapNode("route", "graph", rt.nodeRoute), trpcgraph.WithNodeType(trpcgraph.NodeTypeFunction)).
		AddNode("fast_recall", rt.wrapNode("fast_recall", "tool", rt.nodeRecall), trpcgraph.WithNodeType(trpcgraph.NodeTypeTool)).
		AddNode("fast_rank", rt.wrapNode("fast_rank", "tool", rt.nodeRank), trpcgraph.WithNodeType(trpcgraph.NodeTypeTool)).
		AddNode("slow_intent", rt.wrapNode("slow_intent", "agent", rt.nodeSlowIntent), trpcgraph.WithNodeType(trpcgraph.NodeTypeAgent)).
		AddNode("slow_recall", rt.wrapNode("slow_recall", "tool", rt.nodeRecall), trpcgraph.WithNodeType(trpcgraph.NodeTypeTool)).
		AddNode("slow_rank", rt.wrapNode("slow_rank", "tool", rt.nodeRank), trpcgraph.WithNodeType(trpcgraph.NodeTypeTool)).
		AddNode("hybrid_recall", rt.wrapNode("hybrid_recall", "tool", rt.nodeRecall), trpcgraph.WithNodeType(trpcgraph.NodeTypeTool)).
		AddNode("hybrid_rank", rt.wrapNode("hybrid_rank", "tool", rt.nodeRank), trpcgraph.WithNodeType(trpcgraph.NodeTypeTool)).
		AddNode("agent_rerank", rt.wrapNode("agent_rerank", "agent", rt.nodeRerank), trpcgraph.WithNodeType(trpcgraph.NodeTypeAgent)).
		AddNode("quality", rt.wrapNode("quality", "agent", rt.nodeQuality), trpcgraph.WithNodeType(trpcgraph.NodeTypeAgent)).
		AddNode("explain", rt.wrapNode("explain", "agent", rt.nodeExplain), trpcgraph.WithNodeType(trpcgraph.NodeTypeAgent)).
		AddNode("emit_events", rt.wrapNode("emit_events", "callback", rt.nodeEmitEvents), trpcgraph.WithNodeType(trpcgraph.NodeTypeFunction)).
		AddNode("build_response", rt.wrapNode("build_response", "function", rt.nodeBuildResponse), trpcgraph.WithNodeType(trpcgraph.NodeTypeFunction)).
		SetEntryPoint("normalize_request").
		AddEdge("normalize_request", "load_user_profile").
		AddEdge("load_user_profile", "route").
		AddConditionalEdges("route", func(_ context.Context, state trpcgraph.State) (string, error) {
			path := stateString(state, statePath)
			if path == PathFast || path == PathSlow || path == PathHybrid {
				return path, nil
			}
			return PathHybrid, nil
		}, map[string]string{
			PathFast:   "fast_recall",
			PathSlow:   "slow_intent",
			PathHybrid: "hybrid_recall",
		}).
		AddEdge("fast_recall", "fast_rank").
		AddEdge("fast_rank", "quality").
		AddEdge("slow_intent", "slow_recall").
		AddEdge("slow_recall", "slow_rank").
		AddEdge("slow_rank", "agent_rerank").
		AddEdge("hybrid_recall", "hybrid_rank").
		AddEdge("hybrid_rank", "agent_rerank").
		AddEdge("agent_rerank", "quality").
		AddEdge("quality", "explain").
		AddEdge("explain", "emit_events").
		AddEdge("emit_events", "build_response").
		SetFinishPoint("build_response")
	return sg.Compile()
}

func (rt *RecommendationRuntime) wrapNode(name, typ string, fn func(context.Context, trpcgraph.State) (trpcgraph.State, error)) func(context.Context, trpcgraph.State) (any, error) {
	return func(ctx context.Context, state trpcgraph.State) (any, error) {
		stream := rt.streamForTrace(stateString(state, stateTraceID))
		req := stateRequestValue(state)
		ctx, span := otel.Tracer("sea/recommendationv2").Start(ctx, "recommendation.v2.node."+name)
		span.SetAttributes(
			attribute.String("sea.trace_id", stateString(state, stateTraceID)),
			attribute.String("recommendation.request_id", req.RequestID),
			attribute.String("recommendation.tenant_id", req.TenantID),
			attribute.String("recommendation.channel", req.Channel),
			attribute.String("recommendation.path", stateString(state, statePath)),
			attribute.String("recommendation.node", name),
			attribute.String("recommendation.node_type", typ),
		)
		defer span.End()
		state = appendStepStart(state, name, typ)
		if err := rt.enforceSkillPolicy(ctx, name, state); err != nil {
			state = appendStepEnd(state, name, typ, err)
			rt.publish(ctx, eventFromState(state, EventError, map[string]any{"node": name, "error": err.Error()}))
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			emitStream(stream, StreamEvent{
				Type:      "step_finished",
				TraceID:   stateString(state, stateTraceID),
				RequestID: stateRequestValue(state).RequestID,
				Step:      lastStepPtr(state),
				Error:     err.Error(),
			})
			return state, err
		}
		emitStream(stream, StreamEvent{
			Type:      "step_started",
			TraceID:   stateString(state, stateTraceID),
			RequestID: stateRequestValue(state).RequestID,
			Step:      lastStepPtr(state),
		})
		next, err := fn(ctx, state)
		if err != nil {
			state = appendStepEnd(state, name, typ, err)
			rt.publish(ctx, eventFromState(state, EventError, map[string]any{"node": name, "error": err.Error()}))
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			emitStream(stream, StreamEvent{
				Type:      "step_finished",
				TraceID:   stateString(state, stateTraceID),
				RequestID: stateRequestValue(state).RequestID,
				Step:      lastStepPtr(state),
				Error:     err.Error(),
			})
			return state, err
		}
		state = mergeState(state, next)
		state = appendStepEnd(state, name, typ, nil)
		span.SetAttributes(
			attribute.String("recommendation.path", stateString(state, statePath)),
			attribute.Int("recommendation.item_count", len(stateItemsValue(state))),
			attribute.Int("recommendation.llm_calls", stateCostValue(state).LLMCalls),
			attribute.Int("recommendation.tool_calls", stateCostValue(state).ToolCalls),
			attribute.Float64("recommendation.estimated_amount", stateCostValue(state).EstimatedAmount),
		)
		span.SetStatus(codes.Ok, "ok")
		emitStream(stream, StreamEvent{
			Type:      "step_finished",
			TraceID:   stateString(state, stateTraceID),
			RequestID: stateRequestValue(state).RequestID,
			Step:      lastStepPtr(state),
		})
		return state, nil
	}
}

func (rt *RecommendationRuntime) enforceSkillPolicy(ctx context.Context, nodeName string, state trpcgraph.State) error {
	skillName := skillNameForNode(nodeName)
	if skillName == "" {
		return nil
	}
	if rt == nil || rt.skills == nil {
		return fmt.Errorf("recommendation v2: skill registry unavailable for node %s", nodeName)
	}
	skill, ok := rt.skills.Get(skillName)
	if !ok {
		return fmt.Errorf("recommendation v2: required skill not registered: %s", skillName)
	}
	req := stateRequestValue(state)
	cfg := stateConfigValue(state)
	path := stateString(state, statePath)
	if path == "" {
		path = cfg.PathMode
	}
	if path == PathAuto {
		path = ""
	}
	if rt.policy != nil {
		if err := rt.policy.Allow(ctx, ToolPolicyDecision{
			TenantID: req.TenantID,
			Channel:  req.Channel,
			Path:     path,
			Budget:   cfg.Cost.MaxAmount,
			Skill:    skill,
		}); err != nil {
			return fmt.Errorf("recommendation v2: tool policy rejected %s: %w", skillName, err)
		}
	}
	rt.publish(ctx, eventFromState(state, EventSkillInvoked, map[string]any{
		"node":       nodeName,
		"skill":      skill.Name,
		"category":   skill.Category,
		"cost_level": skill.CostLevel,
	}))
	return nil
}

func skillNameForNode(nodeName string) string {
	switch nodeName {
	case "load_user_profile":
		return "profile.load"
	case "route":
		return "route.path"
	case "fast_recall", "slow_recall", "hybrid_recall":
		return "recall.hybrid"
	case "fast_rank", "slow_rank", "hybrid_rank":
		return "rank.traditional"
	case "slow_intent":
		return "agent.intent"
	case "agent_rerank":
		return "rerank.self"
	case "quality":
		return "quality.score"
	case "explain":
		return "explain.recommend"
	case "emit_events":
		return "event.report"
	default:
		return ""
	}
}

func (rt *RecommendationRuntime) nodeNormalizeRequest(ctx context.Context, state trpcgraph.State) (trpcgraph.State, error) {
	req := state[stateRequest].(RecommendRequest)
	if ok, failures := rt.publishBlocking(ctx, eventFromState(state, EventRequestStarted, map[string]any{"scenario": req.Scenario})); !ok {
		return nil, fmt.Errorf("recommendation v2: request rejected by hook: %s", strings.Join(failures, "; "))
	}
	return trpcgraph.State{stateRequest: req}, nil
}

func (rt *RecommendationRuntime) nodeLoadProfile(ctx context.Context, state trpcgraph.State) (trpcgraph.State, error) {
	req := state[stateRequest].(RecommendRequest)
	snapshot, err := rt.profiles.LoadProfile(ctx, req.User)
	if err != nil {
		return nil, err
	}
	if req.Context != nil {
		if override, ok := req.Context["profile_override"].(map[string]any); ok {
			snapshot.Profile.Extension = mergeAnyMap(snapshot.Profile.Extension, override)
		}
	}
	return trpcgraph.State{stateProfile: snapshot}, nil
}

func (rt *RecommendationRuntime) nodeRoute(ctx context.Context, state trpcgraph.State) (trpcgraph.State, error) {
	req := state[stateRequest].(RecommendRequest)
	cfg := state[stateConfig].(EffectiveRecommendConfig)
	path := cfg.PathMode
	if path == "" || path == PathAuto {
		path = PathHybrid
		if cfg.Cost.MaxAmount > 0 && cfg.Cost.MaxAmount < 0.005 {
			path = PathFast
		}
		if strings.TrimSpace(req.Query) != "" || req.Debug {
			path = PathSlow
		}
	}
	switch path {
	case PathFast, PathSlow, PathHybrid:
	default:
		path = PathHybrid
	}
	rt.publish(ctx, eventFromState(state, EventPathRouted, map[string]any{"path": path}))
	return trpcgraph.State{statePath: path}, nil
}

func (rt *RecommendationRuntime) nodeRecall(ctx context.Context, state trpcgraph.State) (trpcgraph.State, error) {
	rctx := recommendationContextFromState(state)
	items, cost, err := rt.recall.Recall(ctx, rctx)
	if err != nil {
		return nil, err
	}
	rt.publish(ctx, eventFromState(state, EventRecallDone, map[string]any{"count": len(items), "sources": rctx.Config.Recall.Sources}))
	return trpcgraph.State{stateItems: items, stateCost: cost}, nil
}

func (rt *RecommendationRuntime) nodeRank(ctx context.Context, state trpcgraph.State) (trpcgraph.State, error) {
	items, cost, err := rt.rank.Rank(ctx, recommendationContextFromState(state))
	if err != nil {
		return nil, err
	}
	rt.publish(ctx, eventFromState(state, EventRankDone, map[string]any{"count": len(items)}))
	return trpcgraph.State{stateItems: items, stateCost: cost}, nil
}

func (rt *RecommendationRuntime) nodeSlowIntent(ctx context.Context, state trpcgraph.State) (trpcgraph.State, error) {
	cost := stateCostValue(state)
	cost.LLMCalls++
	cost.TokensIn += 80
	cost.TokensOut += 32
	cost.EstimatedAmount += 0.002
	rt.publish(ctx, eventFromState(state, EventAgentStarted, map[string]any{"agent": "IntentAgent"}))
	rt.publish(ctx, eventFromState(state, EventLLMCall, map[string]any{"agent": "IntentAgent", "tokens_in": 80, "tokens_out": 32}))
	return trpcgraph.State{stateCost: cost}, nil
}

func (rt *RecommendationRuntime) nodeRerank(ctx context.Context, state trpcgraph.State) (trpcgraph.State, error) {
	rctx := recommendationContextFromState(state)
	items, cost, err := rt.rerank.Rerank(ctx, rctx)
	if err != nil {
		return nil, err
	}
	rt.publish(ctx, eventFromState(state, EventRerankDone, map[string]any{"count": len(items), "model": rctx.Config.Rerank.Model}))
	return trpcgraph.State{stateItems: items, stateCost: cost}, nil
}

func (rt *RecommendationRuntime) nodeQuality(ctx context.Context, state trpcgraph.State) (trpcgraph.State, error) {
	items, fallback, err := rt.quality.Score(ctx, recommendationContextFromState(state))
	if err != nil {
		return nil, err
	}
	if fallback != nil {
		rt.publish(ctx, eventFromState(state, EventFallbackTriggered, map[string]any{"reason": fallback.Reason, "source": fallback.Source}))
		return trpcgraph.State{stateItems: items, stateFallback: fallback}, nil
	}
	rt.publish(ctx, eventFromState(state, EventQualityDone, map[string]any{"count": len(items)}))
	return trpcgraph.State{stateItems: items}, nil
}

func (rt *RecommendationRuntime) nodeExplain(ctx context.Context, state trpcgraph.State) (trpcgraph.State, error) {
	cfg := state[stateConfig].(EffectiveRecommendConfig)
	if !cfg.Obs.ReturnExplain {
		return nil, nil
	}
	items, _, err := rt.explain.Explain(ctx, recommendationContextFromState(state))
	if err != nil {
		return nil, err
	}
	return trpcgraph.State{stateItems: items}, nil
}

func (rt *RecommendationRuntime) nodeEmitEvents(ctx context.Context, state trpcgraph.State) (trpcgraph.State, error) {
	items := stateItemsValue(state)
	for _, item := range items {
		rt.publish(ctx, eventFromState(state, EventImpression, map[string]any{
			"article_id": item.ArticleID,
			"rank":       item.Rank,
			"source":     item.Source,
		}))
	}
	rt.publish(ctx, eventFromState(state, EventRequestFinished, map[string]any{"items": len(items)}))
	rt.publish(ctx, eventFromState(state, EventTraceEmitted, map[string]any{"trace_id": stateString(state, stateTraceID)}))
	return nil, nil
}

func (rt *RecommendationRuntime) nodeBuildResponse(ctx context.Context, state trpcgraph.State) (trpcgraph.State, error) {
	req := state[stateRequest].(RecommendRequest)
	cfg := state[stateConfig].(EffectiveRecommendConfig)
	items := cloneItems(stateItemsValue(state))
	if req.TopK > 0 && req.TopK < len(items) {
		items = items[:req.TopK]
	}
	for i := range items {
		items[i].Rank = i + 1
	}
	explanations := make([]Explanation, 0, len(items))
	if cfg.Obs.ReturnExplain {
		for _, item := range items {
			explanations = append(explanations, Explanation{ItemID: item.ID, Text: item.Reason, Source: item.Source})
		}
	}
	resp := RecommendResponse{
		RequestID:    req.RequestID,
		TraceID:      stateString(state, stateTraceID),
		PathTaken:    stateString(state, statePath),
		Items:        items,
		Explanations: explanations,
		Cost:         stateCostValue(state),
		Metrics: MetricReport{
			Path:      stateString(state, statePath),
			ItemCount: len(items),
			Fallback:  stateFallbackValue(state) != nil,
			Labels: map[string]string{
				"tenant_id":    req.TenantID,
				"user_id":      req.User.UserID,
				"anonymous_id": req.User.AnonymousID,
				"channel":      req.Channel,
				"scenario":     req.Scenario,
			},
		},
		Fallback: stateFallbackValue(state),
		Extension: map[string]any{
			"framework": "trpc-agent-go",
			"graph":     "RecommendationGraph",
			"runner":    "StateGraphExecutor",
		},
	}
	if cfg.Obs.ReturnSteps || req.Debug {
		resp.Steps = stateStepsValue(state)
	}
	return trpcgraph.State{stateResponse: resp}, nil
}

func (rt *RecommendationRuntime) errorResponse(ctx context.Context, req RecommendRequest, traceID, path string, state trpcgraph.State, start time.Time, err error, stream chan<- StreamEvent) (RecommendResponse, error) {
	resp := RecommendResponse{
		RequestID: req.RequestID,
		TraceID:   traceID,
		PathTaken: path,
		Steps:     stateStepsValue(state),
		Fallback:  &FallbackReport{Triggered: true, Reason: err.Error(), Source: "error"},
		Metrics: MetricReport{
			LatencyMillis: time.Since(start).Milliseconds(),
			Path:          path,
			Fallback:      true,
		},
	}
	rt.publish(ctx, eventFromValues(req, traceID, path, EventError, map[string]any{"error": err.Error()}))
	emitStream(stream, StreamEvent{Type: "error", TraceID: traceID, RequestID: req.RequestID, Response: &resp, Error: err.Error()})
	return resp, err
}

func normalizeRequest(req RecommendRequest) (RecommendRequest, error) {
	if req.RequestID == "" {
		req.RequestID = "rec_" + randID()
	}
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.User.TenantID = strings.TrimSpace(req.User.TenantID)
	if req.TenantID == "" {
		req.TenantID = req.User.TenantID
	}
	if req.TenantID == "" {
		req.TenantID = "default"
	}
	if req.User.TenantID != "" && req.User.TenantID != req.TenantID {
		return req, fmt.Errorf("recommendation v2: tenant_id mismatch between request and user")
	}
	req.User.TenantID = req.TenantID
	if strings.TrimSpace(req.User.UserID) == "" && strings.TrimSpace(req.User.AnonymousID) == "" {
		return req, fmt.Errorf("recommendation v2: user_id or anonymous_id is required")
	}
	if req.Channel == "" {
		req.Channel = req.User.Channel
	}
	if req.Channel == "" {
		req.Channel = "home_feed"
	}
	req.User.Channel = req.Channel
	if req.Scenario == "" {
		req.Scenario = "recommend"
	}
	if req.TopK <= 0 {
		req.TopK = 10
	}
	if req.User.Locale == "" {
		req.User.Locale = "zh-CN"
	}
	return req, nil
}

func (rt *RecommendationRuntime) publishBlocking(ctx context.Context, event BehaviorEvent) (bool, []string) {
	if rt == nil || rt.events == nil {
		return true, nil
	}
	return rt.events.Publish(ctx, event)
}

func (rt *RecommendationRuntime) publish(ctx context.Context, event BehaviorEvent) {
	if rt == nil || rt.events == nil {
		return
	}
	ok, failures := rt.events.Publish(ctx, event)
	if ok || len(failures) == 0 || event.EventType == EventError {
		return
	}
	rt.events.Publish(ctx, eventFromValues(event.UserRequest(), event.TraceID, event.PathTaken, EventError, map[string]any{
		"event_type": event.EventType,
		"failures":   failures,
	}))
}

func eventFromState(state trpcgraph.State, typ string, meta map[string]any) BehaviorEvent {
	req, _ := state[stateRequest].(RecommendRequest)
	return eventFromValues(req, stateString(state, stateTraceID), stateString(state, statePath), typ, meta)
}

func eventFromValues(req RecommendRequest, traceID, path, typ string, meta map[string]any) BehaviorEvent {
	ev := BehaviorEvent{
		EventID:   "evt_" + randID(),
		EventType: typ,
		RequestID: req.RequestID,
		TraceID:   traceID,
		TenantID:  req.TenantID,
		User:      req.User,
		Channel:   req.Channel,
		PathTaken: path,
		Timestamp: time.Now().UTC(),
		Metadata:  meta,
	}
	if articleID, ok := meta["article_id"].(string); ok {
		ev.ArticleID = articleID
	}
	if rank, ok := meta["rank"].(int); ok {
		ev.Rank = rank
	}
	return ev
}

func (event BehaviorEvent) UserRequest() RecommendRequest {
	return RecommendRequest{
		TenantID:  event.TenantID,
		RequestID: event.RequestID,
		Channel:   event.Channel,
		User:      event.User,
	}
}

func appendStepStart(state trpcgraph.State, name, typ string) trpcgraph.State {
	steps := stateStepsValue(state)
	now := time.Now().UTC()
	steps = append(steps, TraceStep{Name: name, Type: typ, Status: "running", StartedAt: now})
	state[stateSteps] = steps
	return state
}

func appendStepEnd(state trpcgraph.State, name, typ string, err error) trpcgraph.State {
	steps := stateStepsValue(state)
	if len(steps) == 0 {
		return state
	}
	i := len(steps) - 1
	now := time.Now().UTC()
	steps[i].FinishedAt = now
	steps[i].DurationMillis = now.Sub(steps[i].StartedAt).Milliseconds()
	steps[i].Cost = stateCostValue(state)
	steps[i].Metadata = mergeAnyMap(steps[i].Metadata, map[string]any{"node": name, "type": typ})
	if err != nil {
		steps[i].Status = "error"
		steps[i].Error = err.Error()
	} else {
		steps[i].Status = "ok"
	}
	state[stateSteps] = steps
	return state
}

func mergeState(base, update trpcgraph.State) trpcgraph.State {
	if update == nil {
		return base
	}
	for k, v := range update {
		base[k] = v
	}
	return base
}

func stateFromDelta(delta map[string][]byte) trpcgraph.State {
	if delta == nil {
		return nil
	}
	state := trpcgraph.State{}
	unmarshalDelta(delta, stateRequest, &state, RecommendRequest{})
	unmarshalDelta(delta, stateConfig, &state, EffectiveRecommendConfig{})
	unmarshalDelta(delta, stateProfile, &state, ProfileSnapshot{})
	unmarshalDelta(delta, statePath, &state, "")
	unmarshalDelta(delta, stateItems, &state, []RecommendItem{})
	unmarshalDelta(delta, stateResponse, &state, RecommendResponse{})
	unmarshalDelta(delta, stateTraceID, &state, "")
	unmarshalDelta(delta, stateSteps, &state, []TraceStep{})
	unmarshalDelta(delta, stateCost, &state, CostReport{})
	unmarshalDelta(delta, stateFallback, &state, (*FallbackReport)(nil))
	return state
}

func unmarshalDelta[T any](delta map[string][]byte, key string, state *trpcgraph.State, zero T) {
	raw, ok := delta[key]
	if !ok || len(raw) == 0 {
		return
	}
	var out T
	if err := json.Unmarshal(raw, &out); err == nil {
		(*state)[key] = out
		return
	}
	(*state)[key] = zero
}

func stateString(state trpcgraph.State, key string) string {
	v, _ := state[key].(string)
	return v
}

func stateItemsValue(state trpcgraph.State) []RecommendItem {
	items, _ := state[stateItems].([]RecommendItem)
	return items
}

func stateRequestValue(state trpcgraph.State) RecommendRequest {
	req, _ := state[stateRequest].(RecommendRequest)
	return req
}

func stateStepsValue(state trpcgraph.State) []TraceStep {
	steps, _ := state[stateSteps].([]TraceStep)
	return steps
}

func stateCostValue(state trpcgraph.State) CostReport {
	cost, _ := state[stateCost].(CostReport)
	return cost
}

func stateFallbackValue(state trpcgraph.State) *FallbackReport {
	fb, _ := state[stateFallback].(*FallbackReport)
	return fb
}

func recommendationContextFromState(state trpcgraph.State) RecommendationContext {
	return RecommendationContext{
		Request: stateRequestValue(state),
		Config:  stateConfigValue(state),
		Profile: stateProfileValue(state),
		Path:    stateString(state, statePath),
		Items:   cloneItems(stateItemsValue(state)),
		Cost:    stateCostValue(state),
	}
}

func stateConfigValue(state trpcgraph.State) EffectiveRecommendConfig {
	cfg, _ := state[stateConfig].(EffectiveRecommendConfig)
	return cfg
}

func stateProfileValue(state trpcgraph.State) ProfileSnapshot {
	profile, _ := state[stateProfile].(ProfileSnapshot)
	return profile
}

func stateStreamValue(state trpcgraph.State) chan<- StreamEvent {
	stream, _ := state[stateStream].(chan<- StreamEvent)
	return stream
}

func (rt *RecommendationRuntime) registerStream(traceID string, stream chan<- StreamEvent) {
	if rt == nil || stream == nil || traceID == "" {
		return
	}
	rt.streamMu.Lock()
	defer rt.streamMu.Unlock()
	rt.streams[traceID] = stream
}

func (rt *RecommendationRuntime) unregisterStream(traceID string) {
	if rt == nil || traceID == "" {
		return
	}
	rt.streamMu.Lock()
	defer rt.streamMu.Unlock()
	delete(rt.streams, traceID)
}

func (rt *RecommendationRuntime) streamForTrace(traceID string) chan<- StreamEvent {
	if rt == nil || traceID == "" {
		return nil
	}
	rt.streamMu.RLock()
	defer rt.streamMu.RUnlock()
	return rt.streams[traceID]
}

func lastStepPtr(state trpcgraph.State) *TraceStep {
	steps := stateStepsValue(state)
	if len(steps) == 0 {
		return nil
	}
	step := steps[len(steps)-1]
	return &step
}

func cloneItems(items []RecommendItem) []RecommendItem {
	out := make([]RecommendItem, len(items))
	copy(out, items)
	return out
}

func mergeAnyMap(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func rankBoost(source string) float64 {
	switch source {
	case "graph":
		return 0.04
	case "cf":
		return 0.03
	case "content":
		return 0.02
	default:
		return 0.01
	}
}

func sanitizeID(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "query"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 24 {
			break
		}
	}
	return strings.Trim(b.String(), "_")
}

func randID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func maxLLMCallsForConfig(cfg EffectiveRecommendConfig) int {
	switch cfg.PathMode {
	case PathFast:
		return 0
	case PathSlow:
		return 8
	default:
		return 4
	}
}

func emitStream(stream chan<- StreamEvent, ev StreamEvent) {
	if stream == nil {
		return
	}
	select {
	case stream <- ev:
	default:
		stream <- ev
	}
}
