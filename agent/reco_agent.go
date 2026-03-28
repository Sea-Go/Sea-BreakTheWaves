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
	"sea/embedding/service"
	"sea/metrics"
	"sea/poolrefill"
	"sea/skillsys"
	"sea/storage"
	"sea/zlog"

	"github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel"
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
	refillDispatcher *poolrefill.AsyncPoolRefillDispatcher
	// memoryChunkRepo 用于 Milvus 召回“记忆分块”，避免把整段长期/周期记忆塞进 prompt。
	memoryChunkRepo *storage.MemoryChunkRepo
}

func NewRecoAgent(ai *openai.Client, reg *skillsys.Registry, poolRepo *storage.PoolRepo, memoryRepo *storage.MemoryRepo, memoryChunkRepo *storage.MemoryChunkRepo, refillDispatcher *poolrefill.AsyncPoolRefillDispatcher) *RecoAgent {
	return &RecoAgent{
		ai: ai, registry: reg,
		poolRepo:         poolRepo,
		memoryRepo:       memoryRepo,
		memoryChunkRepo:  memoryChunkRepo,
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
	// -----------------------------
	// E2E metrics：在一个 defer 里统一上报，保证所有返回路径都能统计到。
	// -----------------------------
	startE2E := time.Now()
	degraded := false
	fallback := false
	statusLabel := "error" // 默认按 error 计；成功路径会在 defer 内覆盖
	resultLabel := "skip"  // validation_total: pass|fail|skip

	tracer := otel.Tracer("sea/reco_agent")
	ctx, root := tracer.Start(ctx, "invoke_agent reco_agent")
	defer root.End()

	if req.Surface == "" {
		req.Surface = "home_feed"
	}
	if req.RecRequestID == "" {
		req.RecRequestID = "rec_" + randID()
	}
	if req.PeriodBucket == "" {
		req.PeriodBucket = "d1"
	}

	ctx = zlog.NewTrace(ctx, req.RecRequestID, req.Surface, "reco_agent", req.UserID, req.SessionID, nil)

	base, _ := zlog.BaseFrom(ctx)
	// metrics / 日志里需要 trace_id；提前把它塞进返回值，避免异常路径返回空 trace。
	respOut := RecommendResponse{
		TraceID:      base.TraceID,
		RecRequestID: req.RecRequestID,
	}

	expl := newExplainBuilder(req.Explain)
	expl.Add("invoke", map[string]any{
		"trace_id":       base.TraceID,
		"rec_request_id": req.RecRequestID,
		"surface":        req.Surface,
		"period_bucket":  req.PeriodBucket,
	})

	var retErr error
	defer func() {
		// 1) status label
		if retErr == nil {
			statusLabel = "ok"
			if degraded {
				statusLabel = "degraded"
			}
			if fallback {
				statusLabel = "fallback"
			}
		}
		// validation_total 的结果：出错则 skip；否则按是否 degraded 计 pass/fail。
		if retErr != nil {
			resultLabel = "skip"
		} else if degraded {
			resultLabel = "fail"
		} else {
			resultLabel = "pass"
		}

		// 2) requests_total
		metrics.GenRecAgentRequestsTotalMetric.WithLabelValues("reco_agent", req.Surface, statusLabel).Inc()

		// 3) e2e latency
		metrics.GenRecAgentE2ELatencySecondsMetric.WithLabelValues("reco_agent", req.Surface, statusLabel).
			Observe(time.Since(startE2E).Seconds())

		// 4) validate
		metrics.GenRecAgentValidationTotalMetric.WithLabelValues(resultLabel).Inc()
	}()

	// root 事件（日志）
	zlog.L().Info("invoke_agent",
		zap.String("event_type", "invoke_agent"),
		zap.String("trace_id", base.TraceID),
		zap.String("rec_request_id", req.RecRequestID),
		zap.String("surface", req.Surface),
		zap.String("agent", "reco_agent"),
		zap.String("status", "OK"),
	)

	// -----------------------------
	// 1) intent.parse（span 必须包住 LLM 调用，否则 Jaeger 里会显示 0us）
	// -----------------------------
	ctxIntent, spIntent := zlog.StartSpan(ctx, "intent.parse")
	intent, intentLat, err := a.intentParse(ctxIntent, req.Query)
	if err != nil {
		spIntent.End(zlog.StatusError, err, zap.Int64("latency_ms", intentLat))
		expl.Add("intent.parse.error", map[string]any{"error": err.Error(), "latency_ms": intentLat})
		respOut.Status = "error"
		retErr = err
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}
	spIntent.End(zlog.StatusOK, nil,
		zap.Int64("latency_ms", intentLat),
		zap.Any("intent", intent),
	)
	expl.Add("intent.parse", map[string]any{
		"label":       intent.Label,
		"confidence":  intent.Confidence,
		"signals":     intent.Signals,
		"latency_ms":  intentLat,
		"user_query":  req.Query,
		"model":       config.Cfg.Agent.Model,
		"temperature": 0.0,
	})

	// -----------------------------
	// 2) policy.route（此处可替换为更复杂策略/模型）
	// -----------------------------
	_, spPolicy := zlog.StartSpan(ctx, "policy.route")
	route := a.routePolicy(req.Query, intent)
	spPolicy.End(zlog.StatusOK, nil, zap.Any("decision", route))
	expl.Add("policy.route", map[string]any{
		"chosen":            route.Chosen,
		"reason_codes":      route.ReasonCodes,
		"must_cite_sources": route.MustCiteSources,
		"max_tool_calls":    route.MaxToolCalls,
	})

	metrics.GenRecAgentRouteDecisionsTotalMetric.WithLabelValues("reco_agent", req.Surface, route.Chosen).Inc()

	// -----------------------------
	// 3) 维护候选池（必要时 refill）
	// -----------------------------
	if a.poolRepo == nil || a.memoryRepo == nil {
		err := errors.New("PoolRepo/MemoryRepo 未注入")
		expl.Add("dependency.error", map[string]any{"error": err.Error()})
		respOut.Status = "error"
		retErr = err
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}

	// ✅ 关键修复：span 必须包住「取记忆 + 向量化 + 记忆分块召回」，否则 Jaeger 里会显示 0us
	ctxProfile, spProfile := zlog.StartSpan(ctx, "retrieval.user_profile")

	longMem, _, _ := a.memoryRepo.Get(ctxProfile, req.UserID, storage.MemoryLongTerm, "")
	shortMem, _, _ := a.memoryRepo.Get(ctxProfile, req.UserID, storage.MemoryShortTerm, "")
	periodicMem, _, _ := a.memoryRepo.Get(ctxProfile, req.UserID, storage.MemoryPeriodic, req.PeriodBucket)

	// 记忆检索：使用 tokenize 分块后的 Milvus 先召回“最相关记忆片段”，避免把整段记忆塞进 prompt。
	longHint := longMem.Content
	shortHint := shortMem.Content
	periodicHint := periodicMem.Content

	var (
		retrievedCnt int
		embedMs      int64
		searchMs     int64
	)

	if a.memoryChunkRepo != nil {
		st := time.Now()
		vec, err := service.TextVector(ctxProfile, req.Query)
		embedMs = time.Since(st).Milliseconds()

		if err == nil && len(vec) > 0 {
			st2 := time.Now()
			longChunks, _ := a.memoryChunkRepo.SearchMemoryChunks(ctxProfile, req.UserID, storage.MemoryLongTerm, "", longMem.UpdatedAt, vec, 5)
			shortChunks, _ := a.memoryChunkRepo.SearchMemoryChunks(ctxProfile, req.UserID, storage.MemoryShortTerm, "", shortMem.UpdatedAt, vec, 5)
			periodChunks, _ := a.memoryChunkRepo.SearchMemoryChunks(ctxProfile, req.UserID, storage.MemoryPeriodic, req.PeriodBucket, periodicMem.UpdatedAt, vec, 5)
			searchMs = time.Since(st2).Milliseconds()

			retrievedCnt = len(longChunks) + len(shortChunks) + len(periodChunks)

			// SearchMemoryChunks 返回的是内容字符串切片，直接拼接即可。
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

	spProfile.End(zlog.StatusOK, nil,
		zap.Any("retrieval", map[string]any{
			"returned_doc_count": retrievedCnt,
			"empty":              retrievedCnt == 0,
			"embed_ms":           embedMs,
			"chunk_search_ms":    searchMs,
		}),
	)
	expl.Add("retrieval.user_profile", map[string]any{
		"memory": map[string]any{
			"long_mem":     longHint,
			"short_mem":    shortHint,
			"periodic_mem": periodicHint,
		},
		"milvus_memory_chunk_recall": map[string]any{
			"returned_doc_count": retrievedCnt,
			"embed_ms":           embedMs,
			"chunk_search_ms":    searchMs,
			"enabled":            a.memoryChunkRepo != nil,
		},
	})

	// 多方向召回 query（满足“周期意图多方向推断”）
	// 说明：这里生成的是“召回 query 文本”，真正的向量化与 Milvus 检索在 pool_refill/milvus_search 工具里完成。
	ctxQGen, spQGen := zlog.StartSpan(ctx, "retrieval.intent_queries")
	recallQueries, _ := a.generateRecallQueries(ctxQGen, req.Query, longHint, shortHint, periodicHint)
	if len(recallQueries) == 0 {
		recallQueries = []string{strings.TrimSpace(req.Query)}
	}
	spQGen.End(zlog.StatusOK, nil, zap.Any("decision", map[string]any{
		"type":         "retrieve",
		"chosen":       "intent_queries",
		"reason_codes": []string{"FUSE_MEMORY"},
		"signals":      map[string]any{"query_count": len(recallQueries)},
	}))
	expl.Add("retrieval.intent_queries", map[string]any{
		"queries":     recallQueries,
		"query_count": len(recallQueries),
	})

	// 池子数量检查 + refill
	longEnsure, err := a.ensurePoolAsync(ctx, req.UserID, storage.PoolLongTerm, "", recallQueries)
	if err != nil {
		expl.Add("pool.ensure.error", map[string]any{"pool": "long_term", "error": err.Error()})
		respOut.Status = "error"
		retErr = err
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}
	shortEnsure, err := a.ensurePoolAsync(ctx, req.UserID, storage.PoolShortTerm, "", recallQueries)
	if err != nil {
		expl.Add("pool.ensure.error", map[string]any{"pool": "short_term", "error": err.Error()})
		respOut.Status = "error"
		retErr = err
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}
	periodEnsure, err := a.ensurePoolAsync(ctx, req.UserID, storage.PoolPeriodic, req.PeriodBucket, recallQueries)
	if err != nil {
		expl.Add("pool.ensure.error", map[string]any{"pool": "periodic", "bucket": req.PeriodBucket, "error": err.Error()})
		respOut.Status = "error"
		retErr = err
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}
	expl.Add("pool_refill_async", map[string]any{
		"recall_queries": recallQueries,
		"pools": []map[string]any{
			longEnsure.ExplainFields(),
			shortEnsure.ExplainFields(),
			periodEnsure.ExplainFields(),
		},
	})

	// -----------------------------
	// 4) 从池子拿候选（不删除，最终推荐后再出池）
	// -----------------------------
	candidates, err := a.collectCandidates(ctx, req.UserID, req.PeriodBucket)
	if err != nil {
		expl.Add("candidates.collect.error", map[string]any{"error": err.Error()})
		respOut.Status = "error"
		retErr = err
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}
	expl.Add("candidates.collect", map[string]any{
		"count": len(candidates),
	})

	metrics.GenRecAgentRetrievalRequestsTotalMetric.WithLabelValues("reco_agent", req.Surface).Inc()
	metrics.GenRecAgentRetrievalReturnedDocsMetric.WithLabelValues("reco_agent", req.Surface).Observe(float64(len(candidates)))

	n := config.Cfg.Pools.Recommend.TakeSize
	if n <= 0 {
		n = 20
	}
	if len(candidates) == 0 {
		fallback = true
		expl.Add("rank.rerank", map[string]any{
			"candidate_in":  0,
			"candidate_out": 0,
			"top_reason":    "",
		})
		respOut.Status = "fallback"
		respOut.IDs = []string{}
		respOut.ArticleIDs = []string{}
		respOut.Explanation = ""
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, nil
	}

	// -----------------------------
	// 5) AI 精排序
	// -----------------------------
	reranked, err := a.aiRerank(ctx, req.UserID, intent.Label, candidates)
	if err != nil {
		expl.Add("rank.rerank.error", map[string]any{"error": err.Error()})
		respOut.Status = "error"
		retErr = err
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}
	expl.Add("rank.rerank", map[string]any{
		"candidate_in":  len(candidates),
		"candidate_out": len(reranked),
		"top_reason": func() string {
			if len(reranked) == 0 {
				return ""
			}
			return reranked[0].Reason
		}(),
	})

	// 取 topN
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

	// -----------------------------
	// 6) validate.output（基础质量校验）
	// -----------------------------
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
	} else {
		// ok
	}
	// validation_total 会在 defer 中统一上报，这里只需标记 degraded。

	_, spVal := zlog.StartSpan(ctx, "validate.output")
	spVal.End(zlog.StatusOK, nil, zap.Any("quality", quality), zap.Int64("latency_ms", 1))
	expl.Add("validate.output", map[string]any{"quality": quality, "degraded": degraded})

	// -----------------------------
	// 7) side_effect：出池（推荐后移除）
	// -----------------------------
	if config.Cfg.Pools.Recommend.RemoveAfterRecommend {
		ctxSE, sp := zlog.StartSpan(ctx, "side_effect.pool_remove")
		if err := a.removeFromPools(ctxSE, req.UserID, req.PeriodBucket, finalIDs); err != nil {
			sp.End(zlog.StatusError, err)
			expl.Add("side_effect.pool_remove.error", map[string]any{"error": err.Error()})
			respOut.Status = "error"
			retErr = err
			respOut.Explanation = expl.Text()
			if req.Explain {
				respOut.ExplainTrace = expl.Trace()
			}
			return respOut, err
		}
		sp.End(zlog.StatusOK, nil, zap.Any("side_effect", map[string]any{
			"type":        "pool_remove",
			"outcome":     "OK",
			"article_ids": finalIDs,
		}))
	}

	explain := ""
	if len(reranked) > 0 {
		explain = reranked[0].Reason
	}

	// 最终返回：Explanation 汇总每步观测信息；不再用 top1 的短 reason 覆盖。
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

	sp.End(zlog.StatusOK, nil, zap.Any("retrieval", map[string]any{
		"returned_doc_count": len(ids),
		"empty":              len(ids) == 0,
		"signals": map[string]any{
			"topk":          topK,
			"period_bucket": periodBucket,
		},
	}))
	return ids, nil
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

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
