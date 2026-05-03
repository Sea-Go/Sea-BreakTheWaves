package user_history

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"sea/embedding/service"
	"sea/storage"

	"go.uber.org/zap"
	"sea/zlog"
)

// =========================
// tool: user_history_add
// =========================

type ToolUserHistoryAdd struct {
	repo *storage.UserHistoryRepo
}

func NewAdd(repo *storage.UserHistoryRepo) *ToolUserHistoryAdd {
	return &ToolUserHistoryAdd{repo: repo}
}

func (t *ToolUserHistoryAdd) Name() string { return "user_history_add" }
func (t *ToolUserHistoryAdd) Description() string {
	return "写入一条用户推荐历史记录（含 clicked/preference，可选向量用于 Milvus 相似检索）。"
}
func (t *ToolUserHistoryAdd) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":    map[string]any{"type": "string"},
			"article_id": map[string]any{"type": "string"},
			"clicked":    map[string]any{"type": "boolean"},
			"preference": map[string]any{"type": "number", "description": "喜好程度（0~1）"},
			"embed_text": map[string]any{"type": "string", "description": "用于生成历史向量的文本（可选，例如标题/摘要）。"},
		},
		"required": []string{"user_id", "article_id"},
	}
}

type addArgs struct {
	UserID     string  `json:"user_id"`
	ArticleID  string  `json:"article_id"`
	Clicked    bool    `json:"clicked"`
	Preference float32 `json:"preference"`
	EmbedText  string  `json:"embed_text"`
}

type addResult struct {
	OK bool `json:"ok"`
}

func (t *ToolUserHistoryAdd) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args addArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}
	if t.repo == nil {
		return nil, errors.New("UserHistoryRepo 未注入")
	}

	var vec []float32
	if strings.TrimSpace(args.EmbedText) != "" {
		v, err := service.TextVector(ctx, args.EmbedText)
		if err != nil {
			return nil, err
		}
		vec = v
	}

	if err := t.repo.Add(ctx, storage.UserHistoryItem{
		UserID:     args.UserID,
		ArticleID:  args.ArticleID,
		Clicked:    args.Clicked,
		Preference: args.Preference,
		Embed:      vec,
	}); err != nil {
		return nil, err
	}

	// 观测：写一条 side_effect
	_, sp := zlog.StartSpan(ctx, "side_effect.user_history_add")
	sp.End(zlog.StatusOK, nil, zap.Any("side_effect", map[string]any{
		"type":       "user_history_add",
		"article_id": args.ArticleID,
		"clicked":    args.Clicked,
		"preference": args.Preference,
	}))
	return addResult{OK: true}, nil
}

// =========================
// tool: user_history_recent
// =========================

type ToolUserHistoryRecent struct{ repo *storage.UserHistoryRepo }

func NewRecent(repo *storage.UserHistoryRepo) *ToolUserHistoryRecent {
	return &ToolUserHistoryRecent{repo: repo}
}

func (t *ToolUserHistoryRecent) Name() string { return "user_history_recent" }
func (t *ToolUserHistoryRecent) Description() string {
	return "读取用户最近 N 条推荐历史记录（按时间倒序）。"
}
func (t *ToolUserHistoryRecent) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id": map[string]any{"type": "string"},
			"limit":   map[string]any{"type": "integer", "default": 50},
		},
		"required": []string{"user_id"},
	}
}

type recentArgs struct {
	UserID string `json:"user_id"`
	Limit  int    `json:"limit"`
}

func (t *ToolUserHistoryRecent) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args recentArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}
	if t.repo == nil {
		return nil, errors.New("UserHistoryRepo 未注入")
	}
	if args.Limit <= 0 {
		args.Limit = 50
	}
	items, err := t.repo.ListRecent(ctx, args.UserID, args.Limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"count": len(items),
		"items": items,
	}, nil
}

// =========================
// tool: user_history_similar
// =========================

type ToolUserHistorySimilar struct{ repo *storage.UserHistoryRepo }

func NewSimilar(repo *storage.UserHistoryRepo) *ToolUserHistorySimilar {
	return &ToolUserHistorySimilar{repo: repo}
}

func (t *ToolUserHistorySimilar) Name() string { return "user_history_similar" }
func (t *ToolUserHistorySimilar) Description() string {
	return "使用 Milvus 在用户历史中做相似检索（用于去重/偏好分析）。"
}
func (t *ToolUserHistorySimilar) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":    map[string]any{"type": "string"},
			"query_text": map[string]any{"type": "string", "description": "检索文本（内部会向量化）"},
			"topk":       map[string]any{"type": "integer", "default": 10},
		},
		"required": []string{"user_id", "query_text"},
	}
}

type similarArgs struct {
	UserID    string `json:"user_id"`
	QueryText string `json:"query_text"`
	TopK      int    `json:"topk"`
}

func (t *ToolUserHistorySimilar) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args similarArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}
	if t.repo == nil {
		return nil, errors.New("UserHistoryRepo 未注入")
	}
	if args.TopK <= 0 {
		args.TopK = 10
	}

	vec, err := service.TextVector(ctx, args.QueryText)
	if err != nil {
		return nil, err
	}

	items, err := t.repo.SearchSimilar(ctx, args.UserID, vec, args.TopK)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"count": len(items),
		"items": items,
	}, nil
}
