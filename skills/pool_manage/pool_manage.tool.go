package pool_manage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"sea/config"
	"sea/embedding/service"
	"sea/infra"
	"sea/storage"
	"sea/zlog"

	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"go.uber.org/zap"
)

// =========================
// 工具 1：pool_get_size
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
// 工具 2：pool_refill
// =========================

// ToolPoolRefill：当池子不足时，使用 Milvus 粗召回补充候选，并写入 PG 池表。
type ToolPoolRefill struct {
	poolRepo    *storage.PoolRepo
	articleRepo *storage.ArticleRepo
}

func NewPoolRefill(poolRepo *storage.PoolRepo, articleRepo *storage.ArticleRepo) *ToolPoolRefill {
	return &ToolPoolRefill{poolRepo: poolRepo, articleRepo: articleRepo}
}

func (t *ToolPoolRefill) Name() string { return "pool_refill" }
func (t *ToolPoolRefill) Description() string {
	return "按配置阈值补充候选池：基于 query_text 在 Milvus 做粗召回，计算 remark_score 后写入 Postgres 池表。"
}
func (t *ToolPoolRefill) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":       map[string]any{"type": "string"},
			"pool_type":     map[string]any{"type": "string", "enum": []string{"long_term", "short_term", "periodic"}},
			"period_bucket": map[string]any{"type": "string"},
			"query_text":    map[string]any{"type": "string", "description": "用于粗召回的检索文本（由意图/记忆生成）。"},
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

func decayFieldName() string {
	field := strings.TrimSpace(config.Cfg.Ranking.RecommendCoarseLinearDecay.FieldName)
	if field == "" {
		return "created_at_unix"
	}
	return field
}

func buildRecommendCoarseLinearDecay() *entity.Function {
	cfg := config.Cfg.Ranking.RecommendCoarseLinearDecay
	// 配置缺失时默认启用 linear decay，这样替换代码后无需额外修改现有 config.yaml 就能生效。
	// 若确实想显式关闭，可在配置里把 enabled=false 且至少提供一个非零参数用于区分“缺省值”。
	if !cfg.Enabled && (cfg.FieldName != "" || cfg.OffsetSeconds != 0 || cfg.ScaleSeconds != 0 || cfg.Decay != 0) {
		return nil
	}
	field := decayFieldName()
	offset := cfg.OffsetSeconds
	if offset < 0 {
		offset = 0
	}
	scale := cfg.ScaleSeconds
	if scale <= 0 {
		scale = 30 * 24 * 60 * 60
	}
	decay := cfg.Decay
	if decay <= 0 || decay >= 1 {
		decay = 0.5
	}
	return entity.NewFunction().
		WithName("recommend_coarse_linear_decay").
		WithInputFields(field).
		WithType(entity.FunctionTypeRerank).
		WithParam("reranker", "decay").
		WithParam("function", "linear").
		WithParam("origin", time.Now().Unix()).
		WithParam("offset", offset).
		WithParam("decay", decay).
		WithParam("scale", scale)
}

func (t *ToolPoolRefill) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args poolRefillArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}
	if t.poolRepo == nil || t.articleRepo == nil {
		return nil, errors.New("PoolRepo/ArticleRepo 未注入")
	}

	// 计算阈值与补充数量（来自 config.yaml）
	policy := pickPolicy(storage.PoolType(args.PoolType))
	if policy.RefillSize <= 0 {
		policy.RefillSize = 200
	}

	// 先做粗召回
	vec, err := service.TextVector(ctx, strings.TrimSpace(args.QueryText))
	if err != nil {
		return nil, err
	}

	cli := infra.Milvus()
	if cli == nil {
		return nil, errors.New("Milvus 客户端未初始化")
	}

	searchTopK := policy.RefillSize * 2

	// 推荐接口的粗排改为：Milvus coarse vector search + linear decay reranker。
	// 使用 coarse collection 中的 created_at_unix 作为数值字段，让“越新的文章越容易保留到前面”。
	opt := milvusclient.NewSearchOption(
		config.Cfg.Milvus.Collections.Coarse,
		searchTopK,
		[]entity.Vector{entity.FloatVector(vec)},
	).WithANNSField("vector")

	if decayFn := buildRecommendCoarseLinearDecay(); decayFn != nil {
		opt = opt.WithOutputFields("article_id", "score", decayFieldName())
		opt = opt.WithFunctionReranker(decayFn)
	} else {
		opt = opt.WithOutputFields("article_id", "score")
	}

	start := time.Now()
	rs, err := cli.Search(ctx, opt)
	lat := time.Since(start).Milliseconds()
	if err != nil {
		return nil, err
	}

	hits := make([]string, 0)
	sims := make([]float32, 0)
	if len(rs) > 0 {
		set := rs[0]
		for i := 0; i < set.ResultCount; i++ {
			id, _ := set.IDs.GetAsString(i)
			hits = append(hits, id)
			sims = append(sims, set.Scores[i])
		}
	}

	// coverage
	var cov float32
	for _, s := range sims {
		cov += s
	}
	if len(sims) > 0 {
		cov = cov / float32(len(sims))
	}

	// 拉取文章基础分
	scoreMap, err := t.articleRepo.GetArticleScores(ctx, hits)
	if err != nil {
		return nil, err
	}

	inserted := 0
	considered := 0
	items := make([]storage.PoolItem, 0, policy.RefillSize)

	for i, articleID := range hits {
		considered++
		finalScore := float32(0)
		if i < len(sims) {
			finalScore = sims[i]
		}
		score := scoreMap[articleID]

		// 旧逻辑是：remark = 相似度 * 权重 + article_score * 权重。
		// 现在推荐接口的粗排直接改为使用 Milvus linear decay reranker 输出的最终顺序与分数，
		// 因此 remark_score 直接落 finalScore。article_score 仍保留在池表中，供后续观测与精排使用。
		items = append(items, storage.PoolItem{
			UserID:       args.UserID,
			PoolType:     storage.PoolType(args.PoolType),
			PeriodBucket: args.PeriodBucket,
			ArticleID:    articleID,
			Score:        score,
			Similarity:   finalScore,
			RemarkScore:  finalScore,
		})
		if len(items) >= policy.RefillSize {
			break
		}
	}

	if err := t.poolRepo.AddItems(ctx, items); err != nil {
		return nil, err
	}
	inserted = len(items)

	sizeAfter, err := t.poolRepo.GetPoolSize(ctx, args.UserID, storage.PoolType(args.PoolType), args.PeriodBucket)
	if err != nil {
		return nil, err
	}

	// 观测：pool_refill 事件（包含检索摘要）
	_, sp := zlog.StartSpan(ctx, "side_effect.pool_refill")
	sp.End(zlog.StatusOK, nil,
		zap.Int64("latency_ms", lat),
		zap.Any("side_effect", map[string]any{
			"type":            "pool_refill",
			"pool_type":       args.PoolType,
			"period_bucket":   args.PeriodBucket,
			"inserted":        inserted,
			"considered":      considered,
			"pool_size_after": sizeAfter,
		}),
		zap.Any("retrieval", map[string]any{
			"source":             config.Cfg.Milvus.Collections.Coarse,
			"returned_doc_count": len(hits),
			"coverage_score":     cov,
			"empty":              len(hits) == 0,
		}),
	)

	return poolRefillResult{
		PoolType:         args.PoolType,
		PeriodBucket:     args.PeriodBucket,
		Inserted:         inserted,
		Considered:       considered,
		PoolSizeAfter:    sizeAfter,
		ReturnedDocCount: len(hits),
		CoverageScore:    cov,
	}, nil
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

func parseMetric(metricStr string) entity.MetricType {
	ms := strings.ToUpper(strings.TrimSpace(metricStr))
	switch ms {
	case "L2":
		return entity.L2
	case "IP":
		return entity.IP
	case "COSINE":
		return entity.COSINE
	default:
		return entity.COSINE
	}
}

// =========================
// 工具 3：pool_pop_topk
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
