package pool_manage

import (
	"context"
	"encoding/json"
	"errors"

	"sea/config"
	"sea/poolrefill"
	searchsvc "sea/service"
	"sea/storage"

	types "sea/type"
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
	return "查看指定用户在 long_term/short_term/periodic 候选池中的当前数量。"
}

func (t *ToolPoolGetSize) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":       map[string]any{"type": "string"},
			"pool_type":     map[string]any{"type": "string", "enum": []string{"long_term", "short_term", "periodic"}},
			"period_bucket": map[string]any{"type": "string", "description": "periodic 池使用的时间桶，例如 d1 / w1 / weekend"},
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
	runner poolrefill.PoolRefillRunner
}

func NewPoolRefill(
	poolRepo *storage.PoolRepo,
	articleRepo *storage.ArticleRepo,
	sourceLikeRepo *storage.SourceLikeRepo,
	reranker searchsvc.RerankInvoker,
) *ToolPoolRefill {
	return &ToolPoolRefill{
		runner: poolrefill.NewPoolRefillExecutionRunner(poolRepo, articleRepo, sourceLikeRepo, reranker),
	}
}

func (t *ToolPoolRefill) Name() string { return "pool_refill" }
func (t *ToolPoolRefill) Description() string {
	return "按 query_text 执行 coarse->fine->rerank->pass 的补池流程，并把结果写入 Postgres 候选池。"
}

func (t *ToolPoolRefill) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":       map[string]any{"type": "string"},
			"pool_type":     map[string]any{"type": "string", "enum": []string{"long_term", "short_term", "periodic"}},
			"period_bucket": map[string]any{"type": "string"},
			"query_text":    map[string]any{"type": "string", "description": "用于补池的召回 query 文本"},
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
	if t.runner == nil {
		return nil, errors.New("PoolRefillRunner 未注入")
	}

	result, err := t.runner.Run(ctx, types.PoolRefillJob{
		UserID:       args.UserID,
		PoolType:     args.PoolType,
		PeriodBucket: args.PeriodBucket,
		QueryTexts:   []string{args.QueryText},
	})
	if err != nil {
		return nil, err
	}

	return poolRefillResult{
		PoolType:         string(result.PoolType),
		PeriodBucket:     result.PeriodBucket,
		Inserted:         result.Inserted,
		Considered:       result.Considered,
		PoolSizeAfter:    result.PoolSizeAfter,
		ReturnedDocCount: result.ReturnedDocCount,
		CoverageScore:    result.CoverageScore,
	}, nil
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
	return "从候选池中按 remark_score 取出 topK 候选，可选是否出池。"
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
