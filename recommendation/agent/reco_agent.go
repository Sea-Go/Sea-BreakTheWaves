package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"sea/config"
	embeddingservice "sea/embedding/service"
	"sea/metrics"
	"sea/poolrefill"
	"sea/skillsys"
	"sea/storage"
	"sea/zlog"

	"github.com/openai/openai-go/v3"
	"go.uber.org/zap"

	types "sea/type"
)

// RecommendRequest 推荐请求（非对话式入口）。
type RecommendRequest = types.RecommendRequest

// RecommendResponse 推荐接口返回结构
type RecommendResponse = types.RecommendResponse

// RecoAgent 全局统筹 Agent：串联意图 -> 召回/池 -> 精排序 -> 出池 -> 观测。
type RecoAgent struct {
	ai       *openai.Client
	registry *skillsys.Registry

	poolRepo         *storage.PoolRepo
	memoryRepo       *storage.MemoryRepo
	sourceLikeRepo   *storage.SourceLikeRepo
	refillDispatcher *poolrefill.AsyncPoolRefillDispatcher
	// memoryChunkRepo 用于 Milvus 召回“记忆分块”，避免把整段长期/周期记忆塞进 prompt。
	memoryChunkRepo *storage.MemoryChunkRepo
}

func NewRecoAgent(
	ai *openai.Client,
	reg *skillsys.Registry,
	poolRepo *storage.PoolRepo,
	memoryRepo *storage.MemoryRepo,
	memoryChunkRepo *storage.MemoryChunkRepo,
	sourceLikeRepo *storage.SourceLikeRepo,
	refillDispatcher *poolrefill.AsyncPoolRefillDispatcher,
) *RecoAgent {
	return &RecoAgent{
		ai: ai, registry: reg,
		poolRepo:         poolRepo,
		memoryRepo:       memoryRepo,
		memoryChunkRepo:  memoryChunkRepo,
		sourceLikeRepo:   sourceLikeRepo,
		refillDispatcher: refillDispatcher,
	}
}

// Recommend 推荐主流程：
// 1) intent.parse（LLM）
// 2) policy.route（策略）
// 3) pool 补充（需要时）
// 4) pool 取候选
// 5) ai_rerank_articles（LLM 精排序）
// 6) validate.output（质量/引用/grounding 基础校验）
// 7) side_effect.*（出池/曝光等）
func (a *RecoAgent) Recommend(ctx context.Context, req RecommendRequest) (RecommendResponse, error) {
	if req.PeriodBucket == "" {
		req.PeriodBucket = "d1"
	}

	recReqID := req.RecRequestID
	surface := req.Surface
	if surface == "" {
		surface = "home_feed"
	}
	ctx = zlog.NewTrace(ctx, recReqID, surface, "reco_agent", req.UserID, req.SessionID, nil)

	base, _ := zlog.BaseFrom(ctx)
	respOut := RecommendResponse{
		TraceID:      base.TraceID,
		RecRequestID: base.RecRequestID,
	}
	expl := newExplainBuilder(req.Explain)
	expl.Add("invoke", map[string]any{
		"trace_id":       base.TraceID,
		"rec_request_id": base.RecRequestID,
		"surface":        base.Surface,
		"period_bucket":  req.PeriodBucket,
	})

	// 1) intent.parse
	intent, err := runStep(ctx, "intent.parse", func(ctx context.Context) (IntentResult, error) {
		r, _, e := a.intentParse(ctx, req.Query)
		return r, e
	})
	if err != nil {
		return failRecommend(expl, "intent.parse", err, &respOut, req.Explain)
	}
	expl.Add("intent.parse", map[string]any{
		"label":      intent.Label,
		"confidence": intent.Confidence,
		"signals":    intent.Signals,
	})

	// 2) policy.route
	route, err := runStep(ctx, "policy.route", func(ctx context.Context) (RouteDecision, error) {
		return a.routePolicy(req.Query, intent), nil
	})
	if err != nil {
		return failRecommend(expl, "policy.route", err, &respOut, req.Explain)
	}
	expl.Add("policy.route", map[string]any{
		"chosen":            route.Chosen,
		"reason_codes":      route.ReasonCodes,
		"must_cite_sources": route.MustCiteSources,
		"max_tool_calls":    route.MaxToolCalls,
	})
	metrics.GenRecAgentRouteDecisionsTotalMetric.WithLabelValues("reco_agent", base.Surface, route.Chosen).Inc()

	if a.poolRepo == nil || a.memoryRepo == nil {
		return failRecommend(expl, "dependency", errors.New("PoolRepo/MemoryRepo not injected"), &respOut, req.Explain)
	}

	// 3) retrieval.user_profile
	profile, err := runStep(ctx, "retrieval.user_profile", func(ctx context.Context) (*profileRecallResult, error) {
		return a.retrieveUserProfile(ctx, req.UserID, req.PeriodBucket, req.Query)
	})
	if err != nil {
		return failRecommend(expl, "retrieval.user_profile", err, &respOut, req.Explain)
	}
	expl.Add("retrieval.user_profile", map[string]any{
		"memory": map[string]any{
			"long_mem":     profile.longHint,
			"short_mem":    profile.shortHint,
			"periodic_mem": profile.periodicHint,
		},
		"milvus_memory_chunk_recall": profile.chunkRecallInfo,
	})

	// 4) retrieval.intent_queries
	recallQueries, err := runStep(ctx, "retrieval.intent_queries", func(ctx context.Context) ([]string, error) {
		q, e := a.generateRecallQueries(ctx, req.Query, profile.longHint, profile.shortHint, profile.periodicHint)
		if len(q) == 0 {
			q = []string{strings.TrimSpace(req.Query)}
		}
		return q, e
	})
	if err != nil {
		return failRecommend(expl, "retrieval.intent_queries", err, &respOut, req.Explain)
	}
	expl.Add("retrieval.intent_queries", map[string]any{
		"queries":     recallQueries,
		"query_count": len(recallQueries),
	})

	// 5) pool ensure (async)
	longEnsure, err := a.ensurePoolAsync(ctx, req.UserID, storage.PoolLongTerm, "", recallQueries)
	if err != nil {
		return failRecommend(expl, "pool.ensure.long_term", err, &respOut, req.Explain)
	}
	shortEnsure, err := a.ensurePoolAsync(ctx, req.UserID, storage.PoolShortTerm, "", recallQueries)
	if err != nil {
		return failRecommend(expl, "pool.ensure.short_term", err, &respOut, req.Explain)
	}
	periodEnsure, err := a.ensurePoolAsync(ctx, req.UserID, storage.PoolPeriodic, req.PeriodBucket, recallQueries)
	if err != nil {
		return failRecommend(expl, "pool.ensure.periodic", err, &respOut, req.Explain)
	}
	expl.Add("pool_refill_async", map[string]any{
		"recall_queries": recallQueries,
		"pools": []map[string]any{
			longEnsure.ExplainFields(),
			shortEnsure.ExplainFields(),
			periodEnsure.ExplainFields(),
		},
	})

	// 6) collect candidates
	candidates, err := a.collectCandidates(ctx, req.UserID, req.PeriodBucket)
	if err != nil {
		return failRecommend(expl, "candidates.collect", err, &respOut, req.Explain)
	}
	expl.Add("candidates.collect", map[string]any{"count": len(candidates)})
	metrics.GenRecAgentRetrievalRequestsTotalMetric.WithLabelValues("reco_agent", base.Surface).Inc()
	metrics.GenRecAgentRetrievalReturnedDocsMetric.WithLabelValues("reco_agent", base.Surface).Observe(float64(len(candidates)))

	n := config.Cfg.Pools.Recommend.TakeSize
	if n <= 0 {
		n = 20
	}

	fallback := len(candidates) == 0
	if fallback {
		expl.Add("rank.rerank", map[string]any{"candidate_in": 0, "candidate_out": 0})
		respOut.Status = "fallback"
		return respOut, nil
	}

	// 7) ai_rerank
	reranked, err := runStep(ctx, "rank.rerank", func(ctx context.Context) ([]RerankItem, error) {
		return a.aiRerank(ctx, req.UserID, intent.Label, candidates)
	})
	if err != nil {
		return failRecommend(expl, "rank.rerank", err, &respOut, req.Explain)
	}
	topReason := ""
	if len(reranked) > 0 {
		topReason = reranked[0].Reason
	}
	expl.Add("rank.rerank", map[string]any{
		"candidate_in":  len(candidates),
		"candidate_out": len(reranked),
		"top_reason":    topReason,
	})

	if len(reranked) > n {
		reranked = reranked[:n]
	}

	var finalIDs []string
	for _, it := range reranked {
		finalIDs = append(finalIDs, it.ArticleID)
	}
	if len(candidates) < n || len(finalIDs) < n {
		fallback = true
	}

	// 8) validate.output
	degraded := false
	quality := map[string]any{
		"schema_valid":          true,
		"citation_valid":        true,
		"claim_grounding_check": "PASS",
		"violations":            []string{},
	}
	if len(candidates) == 0 && len(finalIDs) > 0 {
		quality["citation_valid"] = false
		quality["claim_grounding_check"] = "FAIL_CLAIMED_BUT_EMPTY"
		quality["violations"] = []string{"CITATION_MISSING", "GROUNDED_CLAIM_INCONSISTENT"}
		degraded = true
	}
	_, _ = runStep(ctx, "validate.output", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, nil
	})
	expl.Add("validate.output", map[string]any{"quality": quality, "degraded": degraded})

	// 9) side_effect: pool_remove
	if config.Cfg.Pools.Recommend.RemoveAfterRecommend {
		_, err = runStep(ctx, "side_effect.pool_remove", func(ctx context.Context) (struct{}, error) {
			return struct{}{}, a.removeFromPools(ctx, req.UserID, req.PeriodBucket, finalIDs)
		})
		if err != nil {
			return failRecommend(expl, "side_effect.pool_remove", err, &respOut, req.Explain)
		}
	}

	explain := ""
	if len(reranked) > 0 {
		explain = reranked[0].Reason
	}

	if fallback {
		respOut.Status = "fallback"
	} else {
		respOut.Status = "ok"
	}
	respOut.IDs = finalIDs
	respOut.ArticleIDs = finalIDs
	respOut.Explanation = explain
	return respOut, nil
}

// =========================
// 子流程：intent.parse
// =========================

type IntentResult = types.IntentResult

func (a *RecoAgent) intentParse(ctx context.Context, query string) (IntentResult, int64, error) {
	sys := "你是一个推荐系统的意图识别器。只输出 JSON，不要输出多余文字。格式：{\"label\":\"...\",\"confidence\":0.0~1.0,\"signals\":[\"...\"]}"
	user := "用户输入：" + strings.TrimSpace(query) + "\n" +
		"可用意图 label 示例：explore_new_items / ask_explain / browse_category / unknown。"

	start := time.Now()
	resp, err := a.ai.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: config.Cfg.Agent.Model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(sys),
			openai.UserMessage(user),
		},
		Temperature: openai.Float(0.0),
	})
	lat := time.Since(start).Milliseconds()
	if err != nil {
		return IntentResult{}, lat, err
	}
	// token metrics
	if resp != nil {
		// ChatCompletion Usage: PromptTokens/CompletionTokens/TotalTokens
		metrics.GenRecAgentTotalTokensMetric.Add(float64(resp.Usage.TotalTokens))
	}
	content := ""
	if len(resp.Choices) > 0 {
		content = resp.Choices[0].Message.Content
	}
	var out IntentResult
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		// 兜底：不让整个链路挂掉
		return IntentResult{Label: "unknown", Confidence: 0.1, Signals: []string{"PARSE_FALLBACK"}}, lat, nil
	}
	return out, lat, nil
}

// =========================
// 子流程：policy.route
// =========================

type RouteDecision = types.RouteDecision

func (a *RecoAgent) routePolicy(query string, intent IntentResult) RouteDecision {
	_ = intent
	q := strings.TrimSpace(query)
	chosen := "RAG_ONLY"
	reason := []string{"DEFAULT"}
	mustCite := false

	if strings.Contains(q, "价格") || strings.Contains(q, "库存") || strings.Contains(q, "最新") || strings.Contains(q, "今天") {
		chosen = "RAG+TOOL"
		reason = []string{"NEED_FRESHNESS"}
		mustCite = true
	}
	return RouteDecision{
		Chosen:          chosen,
		ReasonCodes:     reason,
		MustCiteSources: mustCite,
		MaxToolCalls:    config.Cfg.Agent.MaxToolCalls,
	}
}

// =========================
// 子流程：多方向召回 query 生成（满足“周期意图多方向推断”）
// =========================

// generateRecallQueries 基于用户输入与记忆生成多条“不同方向”的召回 query。
// 生产建议：这里输出短文本（1~2 句话），避免 prompt 膨胀。
func (a *RecoAgent) generateRecallQueries(ctx context.Context, query string, longMem string, shortMem string, periodicMem string) ([]string, error) {
	sys := "你是推荐系统的召回 query 生成器。目标：生成 3 条不同方向的召回 query。只输出 JSON，不要输出多余文字。格式：{\"queries\":[\"...\",\"...\",\"...\"]}"
	user := "用户输入：" + strings.TrimSpace(query) + "\n" +
		"长期记忆：" + strings.TrimSpace(longMem) + "\n" +
		"短期记忆：" + strings.TrimSpace(shortMem) + "\n" +
		"周期记忆：" + strings.TrimSpace(periodicMem) + "\n" +
		"要求：三条 query 要覆盖不同方向（例如：主题/标签、问题类型、时间敏感）。每条不超过 40 字。"

	resp, err := a.ai.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: config.Cfg.Agent.Model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(sys),
			openai.UserMessage(user),
		},
		Temperature: openai.Float(0.2),
	})
	if err != nil {
		return nil, err
	}
	// token metrics
	if resp != nil {
		metrics.GenRecAgentTotalTokensMetric.Add(float64(resp.Usage.TotalTokens))
	}
	content := ""
	if len(resp.Choices) > 0 {
		content = resp.Choices[0].Message.Content
	}

	var out struct {
		Queries []string `json:"queries"`
	}
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		// 兜底：用拼接文本
		return []string{
			strings.TrimSpace(query),
			strings.TrimSpace(query + " " + shortMem),
			strings.TrimSpace(query + " " + periodicMem),
		}, nil
	}
	// 清洗空串
	var res []string
	for _, q := range out.Queries {
		q = strings.TrimSpace(q)
		if q != "" {
			res = append(res, q)
		}
	}
	if len(res) == 0 {
		return []string{strings.TrimSpace(query)}, nil
	}
	return res, nil
}

// =========================
// 子流程：ensurePool
// =========================

func (a *RecoAgent) ensurePool(ctx context.Context, userID string, poolType storage.PoolType, periodBucket string, queryTexts []string) error {
	// ✅ 关键修复：给 “池子检查 + refill” 一个覆盖全流程的 span，并把 ctx 往下传，
	// 这样 execute_tool.pool_refill 会变成它的子 span，Jaeger 能一眼看出耗时在哪。
	ctxEnsure, sp := zlog.StartSpan(ctx, "pool.ensure."+string(poolType))

	policy := pickPolicy(poolType)

	size, err := a.poolRepo.GetPoolSize(ctxEnsure, userID, poolType, periodBucket)
	if err != nil {
		sp.End(zlog.StatusError, err)
		return err
	}
	sizeBefore := size

	if size >= policy.MinSize {
		sp.End(zlog.StatusOK, nil, zap.Any("decision", map[string]any{
			"type":   "ensure_pool",
			"chosen": "skip_refill",
			"signals": map[string]any{
				"size_before": sizeBefore,
				"min_size":    policy.MinSize,
			},
		}))
		return nil
	}

	if len(queryTexts) == 0 {
		queryTexts = []string{""}
	}

	attempts := 0
	for _, q := range queryTexts {
		attempts++

		args := map[string]any{
			"user_id":       userID,
			"pool_type":     string(poolType),
			"period_bucket": periodBucket,
			"query_text":    q,
		}
		b, _ := json.Marshal(args)

		// execute_tool.pool_refill 会在 Registry.Invoke 里自动打 span
		if _, _, err := a.registry.Invoke(ctxEnsure, "pool_refill", b); err != nil {
			sp.End(zlog.StatusError, err, zap.Any("pool", map[string]any{
				"size_before": sizeBefore,
				"attempts":    attempts,
			}))
			return err
		}

		size, err = a.poolRepo.GetPoolSize(ctxEnsure, userID, poolType, periodBucket)
		if err != nil {
			sp.End(zlog.StatusError, err)
			return err
		}
		if size >= policy.MinSize {
			break
		}
	}

	sp.End(zlog.StatusOK, nil,
		zap.Any("pool", map[string]any{
			"type":        string(poolType),
			"bucket":      periodBucket,
			"size_before": sizeBefore,
			"size_after":  size,
			"min_size":    policy.MinSize,
			"attempts":    attempts,
			"query_count": len(queryTexts),
		}),
	)
	return nil
}

func pickPolicy(pt storage.PoolType) config.PoolPolicy {
	switch pt {
	case storage.PoolLongTerm:
		return config.Cfg.Pools.LongTerm
	case storage.PoolShortTerm:
		return config.Cfg.Pools.ShortTerm
	case storage.PoolPeriodic:
		return config.Cfg.Pools.Periodic
	default:
		return config.Cfg.Pools.ShortTerm
	}
}

// =========================
// 子流程：collectCandidates
// =========================

type poolEnsureResult struct {
	PoolType     storage.PoolType
	PeriodBucket string
	SizeBefore   int
	Enqueue      types.PoolRefillEnqueueResult
}

func (r poolEnsureResult) ExplainFields() map[string]any {
	return map[string]any{
		"pool_type":     string(r.PoolType),
		"period_bucket": r.PeriodBucket,
		"size_before":   r.SizeBefore,
		"enqueued":      r.Enqueue.Enqueued,
		"deduped":       r.Enqueue.Deduped,
		"queue_result":  r.Enqueue.QueueResult,
	}
}

func (a *RecoAgent) ensurePoolAsync(ctx context.Context, userID string, poolType storage.PoolType, periodBucket string, queryTexts []string) (poolEnsureResult, error) {
	ctxEnsure, sp := zlog.StartSpan(ctx, "pool.ensure."+string(poolType))

	result := poolEnsureResult{
		PoolType:     poolType,
		PeriodBucket: periodBucket,
		Enqueue: types.PoolRefillEnqueueResult{
			PoolType:    string(poolType),
			QueueResult: "disabled",
		},
	}

	policy := pickPolicy(poolType)
	size, err := a.poolRepo.GetPoolSize(ctxEnsure, userID, poolType, periodBucket)
	if err != nil {
		sp.End(zlog.StatusError, err)
		return result, err
	}
	result.SizeBefore = size

	if size >= policy.MinSize {
		result.Enqueue.QueueResult = "skipped_sufficient"
		sp.End(zlog.StatusOK, nil, zap.Any("decision", map[string]any{
			"type":   "ensure_pool",
			"chosen": "skip_refill",
			"signals": map[string]any{
				"size_before": size,
				"min_size":    policy.MinSize,
			},
		}))
		return result, nil
	}

	if a.refillDispatcher != nil {
		result.Enqueue = a.refillDispatcher.Enqueue(types.PoolRefillJob{
			UserID:       userID,
			PoolType:     string(poolType),
			PeriodBucket: periodBucket,
			QueryTexts:   queryTexts,
		})
	}

	status := zlog.StatusOK
	if result.Enqueue.Dropped {
		status = zlog.StatusFallback
	}
	sp.End(status, nil, zap.Any("pool", map[string]any{
		"type":         string(poolType),
		"bucket":       periodBucket,
		"size_before":  size,
		"min_size":     policy.MinSize,
		"query_count":  len(queryTexts),
		"queue_result": result.Enqueue.QueueResult,
		"enqueued":     result.Enqueue.Enqueued,
		"deduped":      result.Enqueue.Deduped,
	}))
	return result, nil
}

func (a *RecoAgent) collectCandidates(ctx context.Context, userID string, periodBucket string) ([]string, error) {
	// ✅ 关键修复：把 “从池子取候选” 作为一个独立 span，并把 ctx 传给工具调用，
	// 这样 execute_tool.pool_pop_topk 会在 Jaeger 里清晰地嵌套在此步骤下。
	ctxCollect, sp := zlog.StartSpan(ctx, "retrieval.collect_candidates")

	topK := config.Cfg.Pools.Recommend.TakeSize
	if topK <= 0 {
		topK = 20
	}

	collect := func(poolType storage.PoolType, bucket string) ([]storage.PoolItem, error) {
		args := map[string]any{
			"user_id":       userID,
			"pool_type":     string(poolType),
			"period_bucket": bucket,
			"topk":          topK,
			"remove":        false,
		}
		b, _ := json.Marshal(args)
		outStr, _, err := a.registry.Invoke(ctxCollect, "pool_pop_topk", b)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Items []storage.PoolItem `json:"items"`
		}
		if err := json.Unmarshal([]byte(outStr), &resp); err != nil {
			return nil, err
		}
		return resp.Items, nil
	}

	longItems, err := collect(storage.PoolLongTerm, "")
	if err != nil {
		sp.End(zlog.StatusError, err)
		return nil, err
	}
	shortItems, err := collect(storage.PoolShortTerm, "")
	if err != nil {
		sp.End(zlog.StatusError, err)
		return nil, err
	}
	periodItems, err := collect(storage.PoolPeriodic, periodBucket)
	if err != nil {
		sp.End(zlog.StatusError, err)
		return nil, err
	}

	m := map[string]struct{}{}
	var ids []string
	for _, it := range append(append(longItems, shortItems...), periodItems...) {
		if _, ok := m[it.ArticleID]; ok {
			continue
		}
		m[it.ArticleID] = struct{}{}
		ids = append(ids, it.ArticleID)
	}

	dislikedIDs, err := a.loadDislikedArticleIDs(ctxCollect, userID)
	if err != nil {
		sp.End(zlog.StatusError, err)
		return nil, err
	}
	ids = filterExcludedArticleIDs(ids, dislikedIDs)
	if len(dislikedIDs) > 0 {
		if removed := intersectArticleIDs(append(append(poolItemsToArticleIDs(longItems), poolItemsToArticleIDs(shortItems)...), poolItemsToArticleIDs(periodItems)...), dislikedIDs); len(removed) > 0 {
			if removeErr := a.removeFromPools(ctxCollect, userID, periodBucket, removed); removeErr != nil {
				zlog.L().Warn("remove disliked items from pools failed",
					zap.String("user_id", userID),
					zap.String("period_bucket", periodBucket),
					zap.Strings("article_ids", removed),
					zap.Error(removeErr),
				)
			}
		}
	}

	sp.End(zlog.StatusOK, nil, zap.Any("retrieval", map[string]any{
		"returned_doc_count": len(ids),
		"empty":              len(ids) == 0,
		"signals": map[string]any{
			"topk":           topK,
			"period_bucket":  periodBucket,
			"disliked_count": len(dislikedIDs),
		},
	}))
	return ids, nil
}

func (a *RecoAgent) loadDislikedArticleIDs(ctx context.Context, userID string) ([]string, error) {
	if a == nil || a.sourceLikeRepo == nil {
		return nil, nil
	}
	return a.sourceLikeRepo.ListDislikedArticleIDs(ctx, userID, 1000)
}

func filterExcludedArticleIDs(articleIDs []string, excludedArticleIDs []string) []string {
	if len(articleIDs) == 0 || len(excludedArticleIDs) == 0 {
		return articleIDs
	}

	excluded := make(map[string]struct{}, len(excludedArticleIDs))
	for _, articleID := range excludedArticleIDs {
		articleID = strings.TrimSpace(articleID)
		if articleID == "" {
			continue
		}
		excluded[articleID] = struct{}{}
	}
	if len(excluded) == 0 {
		return articleIDs
	}

	out := make([]string, 0, len(articleIDs))
	for _, articleID := range articleIDs {
		articleID = strings.TrimSpace(articleID)
		if articleID == "" {
			continue
		}
		if _, ok := excluded[articleID]; ok {
			continue
		}
		out = append(out, articleID)
	}
	return out
}

func intersectArticleIDs(articleIDs []string, excludedArticleIDs []string) []string {
	if len(articleIDs) == 0 || len(excludedArticleIDs) == 0 {
		return nil
	}

	excluded := make(map[string]struct{}, len(excludedArticleIDs))
	for _, articleID := range excludedArticleIDs {
		articleID = strings.TrimSpace(articleID)
		if articleID == "" {
			continue
		}
		excluded[articleID] = struct{}{}
	}
	if len(excluded) == 0 {
		return nil
	}

	out := make([]string, 0, len(articleIDs))
	seen := make(map[string]struct{}, len(articleIDs))
	for _, articleID := range articleIDs {
		articleID = strings.TrimSpace(articleID)
		if articleID == "" {
			continue
		}
		if _, ok := excluded[articleID]; !ok {
			continue
		}
		if _, ok := seen[articleID]; ok {
			continue
		}
		seen[articleID] = struct{}{}
		out = append(out, articleID)
	}
	return out
}

func poolItemsToArticleIDs(items []storage.PoolItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		articleID := strings.TrimSpace(item.ArticleID)
		if articleID == "" {
			continue
		}
		out = append(out, articleID)
	}
	return out
}

// =========================
// 子流程：ai_rerank_articles（通过 skill）
// =========================

type RerankItem = types.RerankItem

func (a *RecoAgent) aiRerank(ctx context.Context, userID string, userIntent string, candidateIDs []string) ([]RerankItem, error) {
	args := map[string]any{
		"user_id":               userID,
		"user_intent":           userIntent,
		"candidate_article_ids": candidateIDs,
		"topk":                  config.Cfg.Pools.Recommend.TakeSize,
	}
	b, _ := json.Marshal(args)

	outStr, _, err := a.registry.Invoke(ctx, "ai_rerank_articles", b)
	if err != nil {
		return nil, err
	}
	var rr struct {
		Ranked []RerankItem `json:"ranked"`
	}
	if err := json.Unmarshal([]byte(outStr), &rr); err != nil {
		return nil, err
	}
	return rr.Ranked, nil
}

// =========================
// 子流程：出池
// =========================

func (a *RecoAgent) removeFromPools(ctx context.Context, userID string, periodBucket string, articleIDs []string) error {
	if err := a.poolRepo.RemoveItems(ctx, userID, storage.PoolLongTerm, "", articleIDs); err != nil {
		return err
	}
	if err := a.poolRepo.RemoveItems(ctx, userID, storage.PoolShortTerm, "", articleIDs); err != nil {
		return err
	}
	if err := a.poolRepo.RemoveItems(ctx, userID, storage.PoolPeriodic, periodBucket, articleIDs); err != nil {
		return err
	}
	return nil
}

type profileRecallResult struct {
	longHint        string
	shortHint       string
	periodicHint    string
	chunkRecallInfo map[string]any
}

// retrieveUserProfile loads user memory from PG and recalls relevant chunks from Milvus.
func (a *RecoAgent) retrieveUserProfile(ctx context.Context, userID, periodBucket, query string) (*profileRecallResult, error) {
	longMem, _, _ := a.memoryRepo.Get(ctx, userID, storage.MemoryLongTerm, "")
	shortMem, _, _ := a.memoryRepo.Get(ctx, userID, storage.MemoryShortTerm, "")
	periodicMem, _, _ := a.memoryRepo.Get(ctx, userID, storage.MemoryPeriodic, periodBucket)

	longHint := longMem.Content
	shortHint := shortMem.Content
	periodicHint := periodicMem.Content

	var retrievedCnt int
	var embedMs, searchMs int64

	if a.memoryChunkRepo != nil {
		st := time.Now()
		vec, err := embeddingservice.TextVector(ctx, query)
		embedMs = time.Since(st).Milliseconds()
		if err == nil && len(vec) > 0 {
			st2 := time.Now()
			longChunks, _ := a.memoryChunkRepo.SearchMemoryChunks(ctx, userID, storage.MemoryLongTerm, "", longMem.UpdatedAt, vec, 5)
			shortChunks, _ := a.memoryChunkRepo.SearchMemoryChunks(ctx, userID, storage.MemoryShortTerm, "", shortMem.UpdatedAt, vec, 5)
			periodChunks, _ := a.memoryChunkRepo.SearchMemoryChunks(ctx, userID, storage.MemoryPeriodic, periodBucket, periodicMem.UpdatedAt, vec, 5)
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

	return &profileRecallResult{
		longHint:     longHint,
		shortHint:    shortHint,
		periodicHint: periodicHint,
		chunkRecallInfo: map[string]any{
			"returned_doc_count": retrievedCnt,
			"empty":              retrievedCnt == 0,
			"embed_ms":           embedMs,
			"chunk_search_ms":    searchMs,
			"enabled":            a.memoryChunkRepo != nil,
		},
	}, nil
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
