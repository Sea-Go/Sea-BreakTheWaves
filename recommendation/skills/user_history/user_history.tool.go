package user_history

import (
	"context"
	"errors"
	"strings"

	"sea/embedding/service"
	"sea/storage"
	"sea/zlog"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
	"go.uber.org/zap"
)

// =========================
// tool: user_history_add
// =========================

type UserHistoryAddInput struct {
	UserID    string  `json:"user_id" jsonschema:"description=用户 ID,required"`
	ArticleID string  `json:"article_id" jsonschema:"description=文章 ID,required"`
	Clicked   bool    `json:"clicked" jsonschema:"description=是否点击"`
	Preference float32 `json:"preference" jsonschema:"description=喜好程度（0~1）"`
	EmbedText string  `json:"embed_text" jsonschema:"description=用于生成历史向量的文本（可选，例如标题/摘要）"`
}

type UserHistoryAddOutput struct {
	OK bool `json:"ok"`
}

func NewAdd(repo *storage.UserHistoryRepo) tool.CallableTool {
	return function.NewFunctionTool(
		func(ctx context.Context, args UserHistoryAddInput) (UserHistoryAddOutput, error) {
			if repo == nil {
				return UserHistoryAddOutput{}, errors.New("UserHistoryRepo 未注入")
			}
			var vec []float32
			if strings.TrimSpace(args.EmbedText) != "" {
				v, err := service.TextVector(ctx, args.EmbedText)
				if err != nil {
					return UserHistoryAddOutput{}, err
				}
				vec = v
			}
			if err := repo.Add(ctx, storage.UserHistoryItem{
				UserID:     args.UserID,
				ArticleID:  args.ArticleID,
				Clicked:    args.Clicked,
				Preference: args.Preference,
				Embed:      vec,
			}); err != nil {
				return UserHistoryAddOutput{}, err
			}
			_, sp := zlog.StartSpan(ctx, "side_effect.user_history_add")
			sp.End(zlog.StatusOK, nil, zap.Any("side_effect", map[string]any{
				"type":       "user_history_add",
				"article_id": args.ArticleID,
				"clicked":    args.Clicked,
				"preference": args.Preference,
			}))
			return UserHistoryAddOutput{OK: true}, nil
		},
		function.WithName("user_history_add"),
		function.WithDescription("写入一条用户推荐历史记录（含 clicked/preference，可选向量用于 Milvus 相似检索）。"),
	)
}

// =========================
// tool: user_history_recent
// =========================

type UserHistoryRecentInput struct {
	UserID string `json:"user_id" jsonschema:"description=用户 ID,required"`
	Limit  int    `json:"limit" jsonschema:"description=返回条数,default=50"`
}

type UserHistoryRecentOutput struct {
	Count int                  `json:"count"`
	Items []storage.UserHistoryItem `json:"items"`
}

func NewRecent(repo *storage.UserHistoryRepo) tool.CallableTool {
	return function.NewFunctionTool(
		func(ctx context.Context, args UserHistoryRecentInput) (UserHistoryRecentOutput, error) {
			if repo == nil {
				return UserHistoryRecentOutput{}, errors.New("UserHistoryRepo 未注入")
			}
			if args.Limit <= 0 {
				args.Limit = 50
			}
			items, err := repo.ListRecent(ctx, args.UserID, args.Limit)
			if err != nil {
				return UserHistoryRecentOutput{}, err
			}
			return UserHistoryRecentOutput{Count: len(items), Items: items}, nil
		},
		function.WithName("user_history_recent"),
		function.WithDescription("读取用户最近 N 条推荐历史记录（按时间倒序）。"),
	)
}

// =========================
// tool: user_history_similar
// =========================

type UserHistorySimilarInput struct {
	UserID    string `json:"user_id" jsonschema:"description=用户 ID,required"`
	QueryText string `json:"query_text" jsonschema:"description=检索文本（内部会向量化）,required"`
	TopK      int    `json:"topk" jsonschema:"description=返回条数,default=10"`
}

type UserHistorySimilarOutput struct {
	Count int                  `json:"count"`
	Items []storage.UserHistoryItem `json:"items"`
}

func NewSimilar(repo *storage.UserHistoryRepo) tool.CallableTool {
	return function.NewFunctionTool(
		func(ctx context.Context, args UserHistorySimilarInput) (UserHistorySimilarOutput, error) {
			if repo == nil {
				return UserHistorySimilarOutput{}, errors.New("UserHistoryRepo 未注入")
			}
			if args.TopK <= 0 {
				args.TopK = 10
			}
			vec, err := service.TextVector(ctx, args.QueryText)
			if err != nil {
				return UserHistorySimilarOutput{}, err
			}
			items, err := repo.SearchSimilar(ctx, args.UserID, vec, args.TopK)
			if err != nil {
				return UserHistorySimilarOutput{}, err
			}
			return UserHistorySimilarOutput{Count: len(items), Items: items}, nil
		},
		function.WithName("user_history_similar"),
		function.WithDescription("使用 Milvus 在用户历史中做相似检索（用于去重/偏好分析）。"),
	)
}
