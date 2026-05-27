package pool_manage

import (
	"context"
	"errors"

	"sea/config"
	"sea/poolrefill"
	searchsvc "sea/service"
	"sea/storage"
	types "sea/type"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

// =========================
// Tool 1: pool_get_size
// =========================

type PoolGetSizeInput struct {
	UserID       string `json:"user_id" jsonschema:"description=用户 ID,required"`
	PoolType     string `json:"pool_type" jsonschema:"description=候选池类型,required,enum=long_term,enum=short_term,enum=periodic"`
	PeriodBucket string `json:"period_bucket" jsonschema:"description=periodic 池使用的时间桶，例如 d1 / w1 / weekend"`
}

type PoolGetSizeOutput struct {
	Count int `json:"count" jsonschema:"description=当前池中候选项数量"`
}

func NewPoolGetSize(poolRepo *storage.PoolRepo) tool.CallableTool {
	return function.NewFunctionTool(
		func(ctx context.Context, args PoolGetSizeInput) (PoolGetSizeOutput, error) {
			if poolRepo == nil {
				return PoolGetSizeOutput{}, errors.New("PoolRepo 未注入")
			}
			cnt, err := poolRepo.GetPoolSize(ctx, args.UserID, storage.PoolType(args.PoolType), args.PeriodBucket)
			if err != nil {
				return PoolGetSizeOutput{}, err
			}
			return PoolGetSizeOutput{Count: cnt}, nil
		},
		function.WithName("pool_get_size"),
		function.WithDescription("查看指定用户在 long_term/short_term/periodic 候选池中的当前数量。"),
	)
}

// =========================
// Tool 2: pool_refill
// =========================

type PoolRefillInput struct {
	UserID       string `json:"user_id" jsonschema:"description=用户 ID,required"`
	PoolType     string `json:"pool_type" jsonschema:"description=候选池类型,required,enum=long_term,enum=short_term,enum=periodic"`
	PeriodBucket string `json:"period_bucket" jsonschema:"description=periodic 池使用的时间桶"`
	QueryText    string `json:"query_text" jsonschema:"description=用于补池的召回 query 文本,required"`
}

type PoolRefillOutput struct {
	PoolType         string  `json:"pool_type"`
	PeriodBucket     string  `json:"period_bucket"`
	Inserted         int     `json:"inserted"`
	Considered       int     `json:"considered"`
	PoolSizeAfter    int     `json:"pool_size_after"`
	ReturnedDocCount int     `json:"returned_doc_count"`
	CoverageScore    float32 `json:"coverage_score"`
}

func NewPoolRefill(
	poolRepo *storage.PoolRepo,
	articleRepo *storage.ArticleRepo,
	sourceLikeRepo *storage.SourceLikeRepo,
	reranker searchsvc.RerankInvoker,
) tool.CallableTool {
	runner := poolrefill.NewPoolRefillExecutionRunner(poolRepo, articleRepo, sourceLikeRepo, reranker)
	return function.NewFunctionTool(
		func(ctx context.Context, args PoolRefillInput) (PoolRefillOutput, error) {
			if runner == nil {
				return PoolRefillOutput{}, errors.New("PoolRefillRunner 未注入")
			}
			result, err := runner.Run(ctx, types.PoolRefillJob{
				UserID:       args.UserID,
				PoolType:     args.PoolType,
				PeriodBucket: args.PeriodBucket,
				QueryTexts:   []string{args.QueryText},
			})
			if err != nil {
				return PoolRefillOutput{}, err
			}
			return PoolRefillOutput{
				PoolType:         string(result.PoolType),
				PeriodBucket:     result.PeriodBucket,
				Inserted:         result.Inserted,
				Considered:       result.Considered,
				PoolSizeAfter:    result.PoolSizeAfter,
				ReturnedDocCount: result.ReturnedDocCount,
				CoverageScore:    result.CoverageScore,
			}, nil
		},
		function.WithName("pool_refill"),
		function.WithDescription("按 query_text 执行 coarse->fine->rerank->pass 的补池流程，并把结果写入 Postgres 候选池。"),
	)
}

// =========================
// Tool 3: pool_pop_topk
// =========================

type PoolPopInput struct {
	UserID       string `json:"user_id" jsonschema:"description=用户 ID,required"`
	PoolType     string `json:"pool_type" jsonschema:"description=候选池类型,required,enum=long_term,enum=short_term,enum=periodic"`
	PeriodBucket string `json:"period_bucket" jsonschema:"description=periodic 池使用的时间桶"`
	TopK         int    `json:"topk" jsonschema:"description=取出数量,default=20"`
	Remove       bool   `json:"remove" jsonschema:"description=是否从池中移除,default=true"`
}

type PoolPopOutput struct {
	Items []storage.PoolItem `json:"items"`
}

func NewPoolPopTopK(poolRepo *storage.PoolRepo) tool.CallableTool {
	return function.NewFunctionTool(
		func(ctx context.Context, args PoolPopInput) (PoolPopOutput, error) {
			if poolRepo == nil {
				return PoolPopOutput{}, errors.New("PoolRepo 未注入")
			}
			if args.TopK <= 0 {
				args.TopK = config.Cfg.Pools.Recommend.TakeSize
			}
			items, err := poolRepo.PopTopK(ctx, args.UserID, storage.PoolType(args.PoolType), args.PeriodBucket, args.TopK, args.Remove)
			if err != nil {
				return PoolPopOutput{}, err
			}
			return PoolPopOutput{Items: items}, nil
		},
		function.WithName("pool_pop_topk"),
		function.WithDescription("从候选池中按 remark_score 取出 topK 候选，可选是否出池。"),
	)
}
