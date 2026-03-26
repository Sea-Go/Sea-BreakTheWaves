package pool_manage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"sea/config"
	"sea/embedding/service"
	"sea/retrieval"
	"sea/storage"
	"sea/zlog"

	"go.uber.org/zap"
)

// =========================
// Tool 1: pool_get_size
// =========================

type ToolPoolGetSize struct {
	poolRepo *storage.PoolRepo
}

func NewPoolGetSize(poolRepo *storage.PoolRepo) *ToolPoolGetSize {
	return &ToolPoolGetSize{poolRepo: poolRepo}
}

func (t *ToolPoolGetSize) Name() string { return "pool_get_size" }
func (t *ToolPoolGetSize) Description() string {
	return "查询指定用户在某个候选池中的元素数量（long_term/short_term/periodic）。"
}

func (t *ToolPoolGetSize) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":       map[string]any{"type": "string"},
			"pool_type":     map[string]any{"type": "string", "enum": []string{"long_term", "short_term", "periodic"}},
			"period_bucket": map[string]any{"type": "string", "description": "周期桶（periodic 时可用，其他可传空）"},
		},
		"required": []string{"user_id", "pool_type"},
	}
}

type poolGetSizeArgs struct {
	UserID       string `json:"user_id"`
	PoolType     string `json:"pool_type"`
	PeriodBucket string `json:"period_bucket"`
}

type poolGetSizeResult struct {
	Count int `json:"count"`
}

func (t *ToolPoolGetSize) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args poolGetSizeArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}
	if t.poolRepo == nil {
		return nil, errors.New("PoolRepo 未注入")
	}
	cnt, err := t.poolRepo.GetPoolSize(ctx, args.UserID, storage.PoolType(args.PoolType), args.PeriodBucket)
	if err != nil {
		return nil, err
	}
	return poolGetSizeResult{Count: cnt}, nil
}

// =========================
// Tool 2: pool_refill
// =========================

type ToolPoolRefill struct {
	poolRepo    *storage.PoolRepo
	articleRepo *storage.ArticleRepo
	reranker    retrieval.RerankInvoker
}

func NewPoolRefill(poolRepo *storage.PoolRepo, articleRepo *storage.ArticleRepo, reranker retrieval.RerankInvoker) *ToolPoolRefill {
	return &ToolPoolRefill{poolRepo: poolRepo, articleRepo: articleRepo, reranker: reranker}
}

func (t *ToolPoolRefill) Name() string { return "pool_refill" }
func (t *ToolPoolRefill) Description() string {
	return "按配置阈值补充候选池：基于 query_text 执行 coarse->fine->rerank->pass 组合排序后写入 Postgres 池表。"
}

func (t *ToolPoolRefill) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":       map[string]any{"type": "string"},
			"pool_type":     map[string]any{"type": "string", "enum": []string{"long_term", "short_term", "periodic"}},
			"period_bucket": map[string]any{"type": "string"},
			"query_text":    map[string]any{"type": "string", "description": "用于召回的搜索文本"},
		},
		"required": []string{"user_id", "pool_type", "query_text"},
	}
}

type poolRefillArgs struct {
	UserID       string `json:"user_id"`
	PoolType     string `json:"pool_type"`
	PeriodBucket string `json:"period_bucket"`
	QueryText    string `json:"query_text"`
}

type poolRefillResult struct {
	PoolType         string  `json:"pool_type"`
	PeriodBucket     string  `json:"period_bucket"`
	Inserted         int     `json:"inserted"`
	Considered       int     `json:"considered"`
	PoolSizeAfter    int     `json:"pool_size_after"`
	ReturnedDocCount int     `json:"returned_doc_count"`
	CoverageScore    float32 `json:"coverage_score"`
}

func (t *ToolPoolRefill) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args poolRefillArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}
	if t.poolRepo == nil || t.articleRepo == nil || t.reranker == nil {
		return nil, errors.New("PoolRepo/ArticleRepo/SkillRegistry 未注入")
	}

	policy := pickPolicy(storage.PoolType(args.PoolType))
	if policy.RefillSize <= 0 {
		policy.RefillSize = 200
	}

	queryText := strings.TrimSpace(args.QueryText)
	if queryText == "" {
		return nil, errors.New("query_text 不能为空")
	}
	vec, err := service.TextVector(ctx, queryText)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	matchOpt := retrieval.QueryMatchOptions{
		CoarseRecallK:        maxInt(policy.RefillSize*4, defaultInt(config.Cfg.Search.CoarseRecallK, 80)),
		FineRecallK:          maxInt(policy.RefillSize*2, defaultInt(config.Cfg.Search.FineRecallK, 40)),
		MaxArticleCandidates: maxInt(policy.RefillSize*2, defaultInt(config.Cfg.Search.MaxArticleCandidates, 20)),
		MinRerankScore:       float32(defaultFloat(config.Cfg.Search.MinRerankScore, 0.10)),
		MinPassScore:         float32(defaultFloat(config.Cfg.Search.MinPassScore, 0.55)),
		SupportBonus:         float32(defaultFloat(config.Cfg.Search.SupportBonus, 0.03)),
		RerankTopK:           maxInt(policy.RefillSize*2, 20),
		QueryText:            queryText,
	}

	ranker := retrieval.NewPrecisionRanker(t.articleRepo, t.reranker)
	match, err := ranker.MatchQuery(ctx, queryText, vec, matchOpt)
	lat := time.Since(start).Milliseconds()
	if err != nil {
		return nil, err
	}

	scoreMap, err := t.articleRepo.GetArticleScores(ctx, match.ArticleIDs)
	if err != nil {
		return nil, err
	}

	items, considered, coverage := buildPoolItems(args, policy.RefillSize, match, scoreMap)
	if err := t.poolRepo.AddItems(ctx, items); err != nil {
		return nil, err
	}

	sizeAfter, err := t.poolRepo.GetPoolSize(ctx, args.UserID, storage.PoolType(args.PoolType), args.PeriodBucket)
	if err != nil {
		return nil, err
	}

	_, sp := zlog.StartSpan(ctx, "side_effect.pool_refill")
	sp.End(zlog.StatusOK, nil,
		zap.Int64("latency_ms", lat),
		zap.Any("side_effect", map[string]any{
			"type":            "pool_refill",
			"pool_type":       args.PoolType,
			"period_bucket":   args.PeriodBucket,
			"inserted":        len(items),
			"considered":      considered,
			"pool_size_after": sizeAfter,
		}),
		zap.Any("retrieval", map[string]any{
			"source":                 config.Cfg.Milvus.Collections.Coarse + " -> " + config.Cfg.Milvus.Collections.Fine,
			"returned_doc_count":     len(match.ArticleIDs),
			"passed_chunk_count":     len(match.PassedHits),
			"coverage_score":         coverage,
			"empty":                  len(items) == 0,
			"coarse_candidate_count": len(match.CoarseCandidates),
			"fine_candidate_count":   len(match.FineCandidates),
			"rerank_request_id":      match.SkillMeta.RequestID,
			"rerank_model":           match.SkillMeta.Model,
		}),
	)

	return poolRefillResult{
		PoolType:         args.PoolType,
		PeriodBucket:     args.PeriodBucket,
		Inserted:         len(items),
		Considered:       considered,
		PoolSizeAfter:    sizeAfter,
		ReturnedDocCount: len(match.ArticleIDs),
		CoverageScore:    coverage,
	}, nil
}

func buildPoolItems(args poolRefillArgs, limit int, match retrieval.QueryMatchResult, articleScores map[string]float32) ([]storage.PoolItem, int, float32) {
	if limit <= 0 {
		limit = 200
	}

	type articleEntry struct {
		ArticleID   string
		Similarity  float32
		RemarkScore float32
	}

	bestByArticle := make(map[string]articleEntry, len(match.PassedHits))
	ordered := make([]string, 0, len(match.PassedHits))
	for _, hit := range match.PassedHits {
		articleID := strings.TrimSpace(hit.ArticleID)
		if articleID == "" {
			continue
		}
		entry := articleEntry{
			ArticleID:   articleID,
			Similarity:  match.VectorScoreByChunk[hit.ChunkID],
			RemarkScore: hit.MatchScore,
		}
		prev, ok := bestByArticle[articleID]
		if !ok {
			bestByArticle[articleID] = entry
			ordered = append(ordered, articleID)
			continue
		}
		if entry.RemarkScore > prev.RemarkScore {
			bestByArticle[articleID] = entry
		}
	}

	if len(bestByArticle) == 0 {
		for _, c := range match.CoarseCandidates {
			articleID := strings.TrimSpace(c.ArticleID)
			if articleID == "" {
				continue
			}
			if _, ok := bestByArticle[articleID]; ok {
				continue
			}
			bestByArticle[articleID] = articleEntry{
				ArticleID:   articleID,
				Similarity:  c.CoarseScore,
				RemarkScore: coarseFallbackScore(match.CoarseRankByArticle[articleID], len(match.CoarseRankByArticle)),
			}
			ordered = append(ordered, articleID)
			if len(ordered) >= limit {
				break
			}
		}
	}

	items := make([]storage.PoolItem, 0, minInt(limit, len(ordered)))
	var coverage float32
	for _, articleID := range ordered {
		entry := bestByArticle[articleID]
		items = append(items, storage.PoolItem{
			UserID:       args.UserID,
			PoolType:     storage.PoolType(args.PoolType),
			PeriodBucket: args.PeriodBucket,
			ArticleID:    articleID,
			Score:        articleScores[articleID],
			Similarity:   entry.Similarity,
			RemarkScore:  entry.RemarkScore,
		})
		coverage += entry.RemarkScore
		if len(items) >= limit {
			break
		}
	}
	if len(items) > 0 {
		coverage /= float32(len(items))
	}
	return items, len(bestByArticle), coverage
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

func coarseFallbackScore(rank, total int) float32 {
	if rank <= 0 || total <= 0 {
		return 0
	}
	if total == 1 {
		return 1
	}
	return 1 - float32(rank-1)/float32(total-1)
}

func defaultInt(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

func defaultFloat(v, fallback float64) float64 {
	if v > 0 {
		return v
	}
	return fallback
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// =========================
// Tool 3: pool_pop_topk
// =========================

type ToolPoolPopTopK struct {
	poolRepo *storage.PoolRepo
}

func NewPoolPopTopK(poolRepo *storage.PoolRepo) *ToolPoolPopTopK {
	return &ToolPoolPopTopK{poolRepo: poolRepo}
}

func (t *ToolPoolPopTopK) Name() string { return "pool_pop_topk" }
func (t *ToolPoolPopTopK) Description() string {
	return "从池子里按 remark_score 取 topK，并按配置可选删除（推荐后出池）。"
}

func (t *ToolPoolPopTopK) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":       map[string]any{"type": "string"},
			"pool_type":     map[string]any{"type": "string", "enum": []string{"long_term", "short_term", "periodic"}},
			"period_bucket": map[string]any{"type": "string"},
			"topk":          map[string]any{"type": "integer", "default": 20},
			"remove":        map[string]any{"type": "boolean", "default": true},
		},
		"required": []string{"user_id", "pool_type"},
	}
}

type poolPopArgs struct {
	UserID       string `json:"user_id"`
	PoolType     string `json:"pool_type"`
	PeriodBucket string `json:"period_bucket"`
	TopK         int    `json:"topk"`
	Remove       bool   `json:"remove"`
}

type poolPopResult struct {
	Items []storage.PoolItem `json:"items"`
}

func (t *ToolPoolPopTopK) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args poolPopArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}
	if t.poolRepo == nil {
		return nil, errors.New("PoolRepo 未注入")
	}
	if args.TopK <= 0 {
		args.TopK = config.Cfg.Pools.Recommend.TakeSize
	}
	items, err := t.poolRepo.PopTopK(ctx, args.UserID, storage.PoolType(args.PoolType), args.PeriodBucket, args.TopK, args.Remove)
	if err != nil {
		return nil, err
	}
	return poolPopResult{Items: items}, nil
}
