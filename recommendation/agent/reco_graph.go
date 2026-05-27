package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"sea/config"
	"sea/embedding/service"
	"sea/metrics"
	"sea/storage"

	graph "trpc.group/trpc-go/trpc-agent-go/graph"
)

// Context keys for per-request runtime state visible to graph nodes.
type recoCtxKey string

const (
	ctxKeyExplain  recoCtxKey = "explain"
	ctxKeyDegraded recoCtxKey = "degraded"
	ctxKeyFallback recoCtxKey = "fallback"
)

func getExpl(ctx context.Context) *explainBuilder {
	return ctx.Value(ctxKeyExplain).(*explainBuilder)
}

func getDegraded(ctx context.Context) *bool {
	return ctx.Value(ctxKeyDegraded).(*bool)
}

func getFallback(ctx context.Context) *bool {
	return ctx.Value(ctxKeyFallback).(*bool)
}

// recoState is the typed state that flows between graph nodes.
// JSON round-trip through graph.State (map[string]any).
type recoState struct {
	// Request
	Query        string `json:"query"`
	UserID       string `json:"user_id"`
	Surface      string `json:"surface"`
	RecRequestID string `json:"rec_request_id"`
	PeriodBucket string `json:"period_bucket"`

	// Step outputs
	Intent        *IntentResult  `json:"intent,omitempty"`
	Route         *RouteDecision `json:"route,omitempty"`
	LongHint      string         `json:"long_hint,omitempty"`
	ShortHint     string         `json:"short_hint,omitempty"`
	PeriodicHint  string         `json:"periodic_hint,omitempty"`
	RecallQueries []string       `json:"recall_queries,omitempty"`
	Candidates    []string       `json:"candidates,omitempty"`
	Reranked      []RerankItem   `json:"reranked,omitempty"`
	FinalIDs      []string       `json:"final_ids,omitempty"`
}

func (s *recoState) toFwk() graph.State {
	b, _ := json.Marshal(s)
	m := make(graph.State)
	json.Unmarshal(b, &m)
	return m
}

func recoStateFrom(state graph.State) *recoState {
	b, _ := json.Marshal(state)
	var s recoState
	json.Unmarshal(b, &s)
	return &s
}

// BuildRecoGraph 编译推荐流水线 StateGraph。
func (a *RecoAgent) BuildRecoGraph() *graph.Graph {
	g := graph.NewStateGraph(nil)

	// ── 1. LLM 意图识别 ──
	// 输入: Query  →  输出: Intent (label/confidence/signals)
	g.AddNode("intent_parse", wrapNode(a.stepIntentParse))

	// ── 2. 规则路由 ──
	// 输入: Intent  →  输出: RouteDecision (策略选择)
	g.AddNode("policy_route", wrapNode(a.stepPolicyRoute))

	// ── 3. 加载用户记忆 + 记忆分块检索 ──
	// 从 PG 加载三组记忆（长期/短期/周期），
	// 若 memoryChunkRepo 可用则进一步做向量召回获取最相关片段
	// 输入: UserID, Query  →  输出: LongHint, ShortHint, PeriodicHint
	g.AddNode("load_profile", wrapNode(a.stepLoadProfile))

	// ── 4. 多方向召回 query 生成 ──
	// 基于 Query + 三组记忆，用 LLM 生成 3 条不同方向的召回 query
	// 输入: Query, 三组 Hint  →  输出: RecallQueries
	g.AddNode("gen_queries", wrapNode(a.stepGenQueries))

	// ── 5. 异步填充候选池（长/短/周期池） ──
	// 检查三个候选池的容量，不足时异步触发 refill 任务
	// 输入: RecallQueries  →  输出: (通过 refillDispatcher 异步填充)
	g.AddNode("ensure_pools", wrapNode(a.stepEnsurePools))

	// ── 6. 从候选池收集候选文章 ──
	// 从三个池子取 topK、去重、过滤不喜欢的文章
	// 输入: UserID  →  输出: Candidates (去重后的 article_id 列表)
	g.AddNode("collect", wrapNode(a.stepCollect))

	// ── 7. LLM 精排序 ──
	// 用 ai_rerank_articles tool 对候选做语义排序，截取 topN
	// 输入: Candidates, Intent.Label  →  输出: Reranked, FinalIDs
	g.AddNode("rerank", wrapNode(a.stepRerank))

	// ── 8. 输出质量校验 ──
	// 检查最终结果是否为空、引用是否合理，标记 degraded
	// 输入: Candidates, FinalIDs  →  输出: (设置 degraded flag)
	g.AddNode("validate", wrapNode(a.stepValidate))

	// ── 9. 推荐后清理 ──
	// 若配置开启，从池子移除已推荐文章
	// 输入: FinalIDs  →  输出: (side effect)
	g.AddNode("side_effect", wrapNode(a.stepSideEffect))

	g.SetEntryPoint("intent_parse")
	g.AddEdge("intent_parse", "policy_route")
	g.AddEdge("policy_route", "load_profile")
	g.AddEdge("load_profile", "gen_queries")
	g.AddEdge("gen_queries", "ensure_pools")
	g.AddEdge("ensure_pools", "collect")
	g.AddEdge("collect", "rerank")
	g.AddEdge("rerank", "validate")
	g.AddEdge("validate", "side_effect")
	g.SetFinishPoint("side_effect")

	return g.MustCompile()
}

// wrapNode converts a typed step function to a framework NodeFunc.
// It deserializes the map state, calls fn, then merges result fields back.
func wrapNode(fn func(ctx context.Context, state *recoState) (*recoState, error)) graph.NodeFunc {
	return func(ctx context.Context, state graph.State) (any, error) {
		rs := recoStateFrom(state)
		result, err := fn(ctx, rs)
		if err != nil {
			return nil, err
		}
		// Merge updated fields into the original state map.
		for k, v := range result.toFwk() {
			state[k] = v
		}
		return state, nil
	}
}

// stepIntentParse — LLM 意图识别。
func (a *RecoAgent) stepIntentParse(ctx context.Context, rs *recoState) (*recoState, error) {
	intent, lat, err := a.intentParse(ctx, rs.Query)
	if err != nil {
		getExpl(ctx).Add("intent.parse.error", map[string]any{"error": err.Error(), "latency_ms": lat})
		return nil, err
	}
	getExpl(ctx).Add("intent.parse", map[string]any{
		"label": intent.Label, "confidence": intent.Confidence,
		"signals": intent.Signals, "latency_ms": lat,
		"user_query": rs.Query, "model": config.Cfg.Agent.Model,
		"temperature": 0.0,
	})
	rs.Intent = &intent
	return rs, nil
}

// stepPolicyRoute — 规则路由。
func (a *RecoAgent) stepPolicyRoute(ctx context.Context, rs *recoState) (*recoState, error) {
	route := a.routePolicy(rs.Query, *rs.Intent)

	getExpl(ctx).Add("policy.route", map[string]any{
		"chosen": route.Chosen, "reason_codes": route.ReasonCodes,
		"must_cite_sources": route.MustCiteSources, "max_tool_calls": route.MaxToolCalls,
	})
	metrics.GenRecAgentRouteDecisionsTotalMetric.WithLabelValues("reco_agent", rs.Surface, route.Chosen).Inc()

	rs.Route = &route
	return rs, nil
}

// stepLoadProfile — 加载用户记忆并检索记忆分块。
func (a *RecoAgent) stepLoadProfile(ctx context.Context, rs *recoState) (*recoState, error) {
	if a.memoryRepo == nil {
		return nil, errors.New("memoryRepo 未注入")
	}

	longMem, _, _ := a.memoryRepo.Get(ctx, rs.UserID, storage.MemoryLongTerm, "")
	shortMem, _, _ := a.memoryRepo.Get(ctx, rs.UserID, storage.MemoryShortTerm, "")
	periodicMem, _, _ := a.memoryRepo.Get(ctx, rs.UserID, storage.MemoryPeriodic, rs.PeriodBucket)

	longHint := longMem.Content
	shortHint := shortMem.Content
	periodicHint := periodicMem.Content

	var retrievedCnt int
	var embedMs int64
	var searchMs int64

	if a.memoryChunkRepo != nil {
		st := time.Now()
		vec, err := service.TextVector(ctx, rs.Query)
		embedMs = time.Since(st).Milliseconds()

		if err == nil && len(vec) > 0 {
			st2 := time.Now()
			longChunks, _ := a.memoryChunkRepo.SearchMemoryChunks(ctx, rs.UserID, storage.MemoryLongTerm, "", longMem.UpdatedAt, vec, 5)
			shortChunks, _ := a.memoryChunkRepo.SearchMemoryChunks(ctx, rs.UserID, storage.MemoryShortTerm, "", shortMem.UpdatedAt, vec, 5)
			periodChunks, _ := a.memoryChunkRepo.SearchMemoryChunks(ctx, rs.UserID, storage.MemoryPeriodic, rs.PeriodBucket, periodicMem.UpdatedAt, vec, 5)
			searchMs = time.Since(st2).Milliseconds()

			retrievedCnt = len(longChunks) + len(shortChunks) + len(periodChunks)
			if len(longChunks) > 0 {
				longHint = strings.Join(longChunks, "\n")
			}
			if len(shortChunks) > 0 {
				shortHint = strings.Join(shortChunks, "\n")
			}
			if len(periodChunks) > 0 {
				periodicHint = strings.Join(periodChunks, "\n")
			}
		}
	}

	getExpl(ctx).Add("retrieval.user_profile", map[string]any{
		"memory": map[string]any{
			"long_mem": longHint, "short_mem": shortHint, "periodic_mem": periodicHint,
		},
		"milvus_memory_chunk_recall": map[string]any{
			"returned_doc_count": retrievedCnt, "embed_ms": embedMs,
			"chunk_search_ms": searchMs, "enabled": a.memoryChunkRepo != nil,
		},
	})

	rs.LongHint = longHint
	rs.ShortHint = shortHint
	rs.PeriodicHint = periodicHint
	return rs, nil
}

// stepGenQueries — 多方向召回 query 生成。
func (a *RecoAgent) stepGenQueries(ctx context.Context, rs *recoState) (*recoState, error) {
	recallQueries, err := a.generateRecallQueries(ctx, rs.Query, rs.LongHint, rs.ShortHint, rs.PeriodicHint)
	if err != nil {
		return nil, err
	}
	if len(recallQueries) == 0 {
		recallQueries = []string{strings.TrimSpace(rs.Query)}
	}

	getExpl(ctx).Add("retrieval.intent_queries", map[string]any{
		"queries": recallQueries, "query_count": len(recallQueries),
	})

	rs.RecallQueries = recallQueries
	return rs, nil
}

// stepEnsurePools — 异步填充三个候选池。
func (a *RecoAgent) stepEnsurePools(ctx context.Context, rs *recoState) (*recoState, error) {
	if a.poolRepo == nil {
		return nil, errors.New("PoolRepo 未注入")
	}

	longEnsure, err := a.ensurePoolAsync(ctx, rs.UserID, storage.PoolLongTerm, "", rs.RecallQueries)
	if err != nil {
		return nil, err
	}
	shortEnsure, err := a.ensurePoolAsync(ctx, rs.UserID, storage.PoolShortTerm, "", rs.RecallQueries)
	if err != nil {
		return nil, err
	}
	periodEnsure, err := a.ensurePoolAsync(ctx, rs.UserID, storage.PoolPeriodic, rs.PeriodBucket, rs.RecallQueries)
	if err != nil {
		return nil, err
	}

	getExpl(ctx).Add("pool_refill_async", map[string]any{
		"recall_queries": rs.RecallQueries,
		"pools": []map[string]any{
			longEnsure.ExplainFields(),
			shortEnsure.ExplainFields(),
			periodEnsure.ExplainFields(),
		},
	})
	return rs, nil
}

// stepCollect — 从池子取候选并去重/过滤。
func (a *RecoAgent) stepCollect(ctx context.Context, rs *recoState) (*recoState, error) {
	candidates, err := a.collectCandidates(ctx, rs.UserID, rs.PeriodBucket)
	if err != nil {
		return nil, err
	}

	metrics.GenRecAgentRetrievalRequestsTotalMetric.WithLabelValues("reco_agent", rs.Surface).Inc()
	metrics.GenRecAgentRetrievalReturnedDocsMetric.WithLabelValues("reco_agent", rs.Surface).Observe(float64(len(candidates)))

	getExpl(ctx).Add("candidates.collect", map[string]any{"count": len(candidates)})

	rs.Candidates = candidates
	return rs, nil
}

// stepRerank — AI 精排序。
func (a *RecoAgent) stepRerank(ctx context.Context, rs *recoState) (*recoState, error) {
	n := config.Cfg.Pools.Recommend.TakeSize
	if n <= 0 {
		n = 20
	}

	if len(rs.Candidates) == 0 {
		*getFallback(ctx) = true
		getExpl(ctx).Add("rank.rerank", map[string]any{
			"candidate_in": 0, "candidate_out": 0, "top_reason": "",
		})
		rs.FinalIDs = nil
		return rs, nil
	}

	reranked, err := a.aiRerank(ctx, rs.UserID, rs.Intent.Label, rs.Candidates)
	if err != nil {
		return nil, err
	}

	if len(reranked) > n {
		reranked = reranked[:n]
	}

	finalIDs := make([]string, len(reranked))
	for i, it := range reranked {
		finalIDs[i] = it.ArticleID
	}

	if len(rs.Candidates) < n || len(finalIDs) < n {
		*getFallback(ctx) = true
	}

	var topReason string
	if len(reranked) > 0 {
		topReason = reranked[0].Reason
	}
	getExpl(ctx).Add("rank.rerank", map[string]any{
		"candidate_in": len(rs.Candidates), "candidate_out": len(reranked),
		"top_reason": topReason,
	})

	rs.Reranked = reranked
	rs.FinalIDs = finalIDs
	return rs, nil
}

// stepValidate — 输出质量校验。
func (a *RecoAgent) stepValidate(ctx context.Context, rs *recoState) (*recoState, error) {
	quality := map[string]any{
		"schema_valid": true, "citation_valid": true,
		"claim_grounding_check": "PASS", "violations": []string{},
	}
	if len(rs.Candidates) == 0 && len(rs.FinalIDs) > 0 {
		quality["citation_valid"] = false
		quality["claim_grounding_check"] = "FAIL_CLAIMED_BUT_EMPTY"
		quality["violations"] = []string{"CITATION_MISSING", "GROUNDED_CLAIM_INCONSISTENT"}
		*getDegraded(ctx) = true
	}

	getExpl(ctx).Add("validate.output", map[string]any{
		"quality": quality, "degraded": *getDegraded(ctx),
	})
	return rs, nil
}

// stepSideEffect — 推荐后从池子移除已推荐文章。
func (a *RecoAgent) stepSideEffect(ctx context.Context, rs *recoState) (*recoState, error) {
	if !config.Cfg.Pools.Recommend.RemoveAfterRecommend || len(rs.FinalIDs) == 0 {
		return rs, nil
	}

	if err := a.removeFromPools(ctx, rs.UserID, rs.PeriodBucket, rs.FinalIDs); err != nil {
		return nil, err
	}

	getExpl(ctx).Add("side_effect.pool_remove", map[string]any{
		"type": "pool_remove", "outcome": "OK", "article_ids": rs.FinalIDs,
	})
	return rs, nil
}
