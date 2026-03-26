package memory_manage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"sea/chunk"
	"sea/config"
	"sea/embedding/service"
	"sea/infra"
	"sea/storage"
	"sea/zlog"

	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"go.uber.org/zap"
)

// -----------------------------------------------------------------------------
// Tool: user_memory_get
// -----------------------------------------------------------------------------

type ToolUserMemoryGet struct{ repo *storage.MemoryRepo }

func NewGet(repo *storage.MemoryRepo) *ToolUserMemoryGet { return &ToolUserMemoryGet{repo: repo} }

func (t *ToolUserMemoryGet) Name() string { return "user_memory_get" }
func (t *ToolUserMemoryGet) Description() string {
	return "读取用户记忆（short_term / long_term / periodic）。"
}

func (t *ToolUserMemoryGet) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":       map[string]any{"type": "string"},
			"memory_type":   map[string]any{"type": "string", "enum": []string{"short_term", "long_term", "periodic"}},
			"period_bucket": map[string]any{"type": "string", "description": "当 memory_type=periodic 时可选（例如 d1/w1/weekend）。其他类型忽略。"},
		},
		"required": []string{"user_id", "memory_type"},
	}
}

type getArgs struct {
	UserID       string `json:"user_id"`
	MemoryType   string `json:"memory_type"`
	PeriodBucket string `json:"period_bucket"`
}

func (t *ToolUserMemoryGet) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args getArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}
	if t.repo == nil {
		return nil, errors.New("MemoryRepo 未注入")
	}

	mt, err := parseMemoryType(args.MemoryType)
	if err != nil {
		return nil, err
	}
	if mt != storage.MemoryPeriodic {
		args.PeriodBucket = ""
	}

	m, ok, err := t.repo.Get(ctx, strings.TrimSpace(args.UserID), mt, strings.TrimSpace(args.PeriodBucket))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"found":         ok,
		"user_id":       args.UserID,
		"memory_type":   string(mt),
		"period_bucket": args.PeriodBucket,
		"content":       m.Content,
		"updated_at":    m.UpdatedAt,
	}, nil
}

// -----------------------------------------------------------------------------
// Tool: user_memory_upsert
// -----------------------------------------------------------------------------

type ToolUserMemoryUpsert struct {
	repo      *storage.MemoryRepo
	chunkRepo *storage.MemoryChunkRepo
}

func NewUpsert(repo *storage.MemoryRepo, chunkRepo *storage.MemoryChunkRepo) *ToolUserMemoryUpsert {
	return &ToolUserMemoryUpsert{repo: repo, chunkRepo: chunkRepo}
}

func (t *ToolUserMemoryUpsert) Name() string { return "user_memory_upsert" }
func (t *ToolUserMemoryUpsert) Description() string {
	return "写入/覆盖用户记忆。对 long_term/periodic 会做 tokenize 分块 + 向量化写入 user_memory_chunks（Milvus）。"
}

func (t *ToolUserMemoryUpsert) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":       map[string]any{"type": "string"},
			"memory_type":   map[string]any{"type": "string", "enum": []string{"short_term", "long_term", "periodic"}},
			"period_bucket": map[string]any{"type": "string", "description": "当 memory_type=periodic 时可选（例如 d1/w1/weekend）。其他类型忽略。"},
			"content":       map[string]any{"type": "string"},
		},
		"required": []string{"user_id", "memory_type", "content"},
	}
}

type upsertArgs struct {
	UserID       string `json:"user_id"`
	MemoryType   string `json:"memory_type"`
	PeriodBucket string `json:"period_bucket"`
	Content      string `json:"content"`
}

func (t *ToolUserMemoryUpsert) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args upsertArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}
	if t.repo == nil {
		return nil, errors.New("MemoryRepo 未注入")
	}
	if t.chunkRepo == nil {
		return nil, errors.New("MemoryChunkRepo 未注入")
	}

	mt, err := parseMemoryType(args.MemoryType)
	if err != nil {
		return nil, err
	}
	if mt != storage.MemoryPeriodic {
		args.PeriodBucket = ""
	}
	args.UserID = strings.TrimSpace(args.UserID)
	args.PeriodBucket = strings.TrimSpace(args.PeriodBucket)
	args.Content = strings.TrimSpace(args.Content)
	if args.UserID == "" {
		return nil, errors.New("user_id 不能为空")
	}
	if args.Content == "" {
		return nil, errors.New("content 不能为空")
	}

	updatedAt := time.Now()
	if err := t.repo.Upsert(ctx, storage.UserMemory{
		UserID:       args.UserID,
		MemoryType:   mt,
		PeriodBucket: args.PeriodBucket,
		Content:      args.Content,
		UpdatedAt:    updatedAt,
	}); err != nil {
		return nil, err
	}

	chunkCount, err := replaceMemoryChunks(ctx, t.chunkRepo, args.UserID, mt, args.PeriodBucket, updatedAt, args.Content)
	if err != nil {
		return nil, err
	}

	_, sp := zlog.StartSpan(ctx, "side_effect.user_memory_upsert")
	sp.End(zlog.StatusOK, nil, zap.Any("side_effect", map[string]any{
		"type":          "user_memory_upsert",
		"memory_type":   string(mt),
		"period_bucket": args.PeriodBucket,
		"chunk_count":   chunkCount,
	}))

	return map[string]any{
		"ok":            true,
		"user_id":       args.UserID,
		"memory_type":   string(mt),
		"period_bucket": args.PeriodBucket,
		"updated_at":    updatedAt,
		"chunk_count":   chunkCount,
	}, nil
}

// -----------------------------------------------------------------------------
// Tool: memory_maintain_window
// -----------------------------------------------------------------------------

type ToolMemoryMaintainWindow struct {
	historyRepo *storage.UserHistoryRepo
	articleRepo *storage.ArticleRepo
	memoryRepo  *storage.MemoryRepo
	chunkRepo   *storage.MemoryChunkRepo
}

func NewMaintainWindow(historyRepo *storage.UserHistoryRepo, articleRepo *storage.ArticleRepo, memoryRepo *storage.MemoryRepo, chunkRepo *storage.MemoryChunkRepo) *ToolMemoryMaintainWindow {
	return &ToolMemoryMaintainWindow{historyRepo: historyRepo, articleRepo: articleRepo, memoryRepo: memoryRepo, chunkRepo: chunkRepo}
}

func (t *ToolMemoryMaintainWindow) Name() string { return "memory_maintain_window" }
func (t *ToolMemoryMaintainWindow) Description() string {
	return "基于用户过去 1 天 / 7 天行为生成偏好摘要，并写回 user_memory（short_term/long_term/periodic）。"
}

func (t *ToolMemoryMaintainWindow) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":            map[string]any{"type": "string"},
			"window":             map[string]any{"type": "string", "enum": []string{"1d", "7d"}},
			"target_memory_type": map[string]any{"type": "string", "enum": []string{"short_term", "long_term", "periodic"}},
			"period_bucket":      map[string]any{"type": "string", "description": "当 target_memory_type=periodic 时可选（例如 d1/w1/weekend）。其他类型忽略。"},
			"topk":               map[string]any{"type": "integer", "default": 5, "description": "偏好标签/类型 TopK"},
		},
		"required": []string{"user_id", "window", "target_memory_type"},
	}
}

type maintainArgs struct {
	UserID           string `json:"user_id"`
	Window           string `json:"window"`
	TargetMemoryType string `json:"target_memory_type"`
	PeriodBucket     string `json:"period_bucket"`
	TopK             int    `json:"topk"`
}

func (t *ToolMemoryMaintainWindow) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	if time.Now().Unix() >= 0 {
		return t.invokeV2(ctx, argsRaw)
	}

	var args maintainArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}
	if t.historyRepo == nil || t.articleRepo == nil || t.memoryRepo == nil {
		return nil, errors.New("依赖未注入（historyRepo/articleRepo/memoryRepo）")
	}

	mt, err := parseMemoryType(args.TargetMemoryType)
	if err != nil {
		return nil, err
	}
	if mt != storage.MemoryPeriodic {
		args.PeriodBucket = ""
	}
	args.UserID = strings.TrimSpace(args.UserID)
	args.PeriodBucket = strings.TrimSpace(args.PeriodBucket)
	if args.UserID == "" {
		return nil, errors.New("user_id 不能为空")
	}
	if args.TopK <= 0 {
		args.TopK = 5
	}
	win, err := parseWindow(args.Window)
	if err != nil {
		return nil, err
	}

	// 1) 拉历史（PG）
	hist, err := t.historyRepo.ListRecent(ctx, args.UserID, 500)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-win)
	var filtered []storage.UserHistoryItem
	articleSet := map[string]struct{}{}
	clickedCnt := 0
	for _, it := range hist {
		if it.TS.Before(cutoff) {
			continue
		}
		filtered = append(filtered, it)
		articleSet[it.ArticleID] = struct{}{}
		if it.Clicked {
			clickedCnt++
		}
	}
	if len(filtered) == 0 {
		content := fmt.Sprintf("过去 %s 内暂无可用的行为记录。", args.Window)
		updatedAt := time.Now()
		_ = t.memoryRepo.Upsert(ctx, storage.UserMemory{UserID: args.UserID, MemoryType: mt, PeriodBucket: args.PeriodBucket, Content: content, UpdatedAt: updatedAt})

		_, sp := zlog.StartSpan(ctx, "side_effect.memory_maintain_window")
		sp.End(zlog.StatusOK, nil, zap.Any("side_effect", map[string]any{
			"type":          "memory_maintain_window",
			"window":        args.Window,
			"target":        string(mt),
			"period_bucket": args.PeriodBucket,
			"empty":         true,
		}))

		return map[string]any{
			"ok":                 true,
			"empty":              true,
			"window":             args.Window,
			"target_memory_type": string(mt),
			"period_bucket":      args.PeriodBucket,
			"content":            content,
			"updated_at":         updatedAt,
		}, nil
	}

	ids := make([]string, 0, len(articleSet))
	for id := range articleSet {
		ids = append(ids, id)
	}
	metas, err := t.articleRepo.GetArticlesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	metaByID := map[string]storage.ArticleMeta{}
	for _, a := range metas {
		metaByID[a.ArticleID] = a
	}

	// 2) 聚合偏好（按点击次数 TopK；你也可以把 Preference 融进 score）
	typeCnt := map[string]int{}
	tagCnt := map[string]int{}
	for _, it := range filtered {
		m, ok := metaByID[it.ArticleID]
		if !ok {
			continue
		}
		if !it.Clicked {
			continue
		}
		for _, tt := range splitCSV(m.TypeTags) {
			typeCnt[tt]++
		}
		for _, tg := range splitCSV(m.Tags) {
			tagCnt[tg]++
		}
	}
	preferTypes := topKCounts(typeCnt, args.TopK)
	preferTags := topKCounts(tagCnt, args.TopK)

	// 3) 生成摘要并写回记忆
	content := buildWindowSummary(args.Window, len(filtered), clickedCnt, preferTypes, preferTags)
	updatedAt := time.Now()
	if err := t.memoryRepo.Upsert(ctx, storage.UserMemory{
		UserID:       args.UserID,
		MemoryType:   mt,
		PeriodBucket: args.PeriodBucket,
		Content:      content,
		UpdatedAt:    updatedAt,
	}); err != nil {
		return nil, err
	}

	_, sp := zlog.StartSpan(ctx, "side_effect.memory_maintain_window")
	sp.End(zlog.StatusOK, nil, zap.Any("side_effect", map[string]any{
		"type":            "memory_maintain_window",
		"window":          args.Window,
		"target":          string(mt),
		"period_bucket":   args.PeriodBucket,
		"history_count":   len(filtered),
		"clicked_count":   clickedCnt,
		"preferred_types": preferTypes,
		"preferred_tags":  preferTags,
	}))

	return map[string]any{
		"ok":                 true,
		"empty":              false,
		"window":             args.Window,
		"history_count":      len(filtered),
		"clicked_count":      clickedCnt,
		"preferred_types":    preferTypes,
		"preferred_tags":     preferTags,
		"target_memory_type": string(mt),
		"period_bucket":      args.PeriodBucket,
		"content":            content,
		"updated_at":         updatedAt,
	}, nil
}

func buildWindowSummary(window string, historyCnt, clickedCnt int, types, tags []string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("过去 %s 行为摘要：\n", window))
	b.WriteString(fmt.Sprintf("- 行为记录：%d 条（点击 %d 条）\n", historyCnt, clickedCnt))
	if len(types) > 0 {
		b.WriteString("- 偏好类型（TopK）：" + strings.Join(types, "、") + "\n")
	}
	if len(tags) > 0 {
		b.WriteString("- 偏好标签（TopK）：" + strings.Join(tags, "、") + "\n")
	}
	return strings.TrimSpace(b.String())
}

func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		res = append(res, p)
	}
	return res
}

func parseMemoryType(s string) (storage.MemoryType, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case string(storage.MemoryShortTerm):
		return storage.MemoryShortTerm, nil
	case string(storage.MemoryLongTerm):
		return storage.MemoryLongTerm, nil
	case string(storage.MemoryPeriodic):
		return storage.MemoryPeriodic, nil
	default:
		return "", errors.New("memory_type 必须是 short_term / long_term / periodic")
	}
}

func parseWindow(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "1d":
		return 24 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	default:
		return 0, errors.New("window 必须是 1d 或 7d")
	}
}

// -----------------------------------------------------------------------------
// Tool: user_memory_chunk_hybrid_search
//   - 使用 Milvus Go SDK HybridSearch + Function(RERANK) weighted reranker
//   - 对齐官方 Python 示例：FunctionType.RERANK + reranker=weighted + weights + norm_score
// -----------------------------------------------------------------------------

func (t *ToolMemoryMaintainWindow) invokeV2(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args maintainArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}
	if t.historyRepo == nil || t.articleRepo == nil || t.memoryRepo == nil || t.chunkRepo == nil {
		return nil, errors.New("依赖未注入（historyRepo/articleRepo/memoryRepo/chunkRepo）")
	}

	mt, err := parseMemoryType(args.TargetMemoryType)
	if err != nil {
		return nil, err
	}
	if mt != storage.MemoryPeriodic {
		args.PeriodBucket = ""
	}
	args.UserID = strings.TrimSpace(args.UserID)
	args.PeriodBucket = strings.TrimSpace(args.PeriodBucket)
	if args.UserID == "" {
		return nil, errors.New("user_id 不能为空")
	}
	if args.TopK <= 0 {
		args.TopK = 5
	}
	win, err := parseWindow(args.Window)
	if err != nil {
		return nil, err
	}

	hist, err := t.historyRepo.ListRecent(ctx, args.UserID, 500)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-win)
	filtered := make([]storage.UserHistoryItem, 0, len(hist))
	articleSet := map[string]struct{}{}
	clickedCnt := 0
	for _, it := range hist {
		if it.TS.Before(cutoff) {
			continue
		}
		filtered = append(filtered, it)
		articleSet[it.ArticleID] = struct{}{}
		if it.Clicked {
			clickedCnt++
		}
	}

	if len(filtered) == 0 {
		content := fmt.Sprintf("过去 %s 内暂无可用行为记录。", args.Window)
		updatedAt := time.Now()
		chunkCount, err := upsertMemoryContent(ctx, t.memoryRepo, t.chunkRepo, args.UserID, mt, args.PeriodBucket, content, updatedAt)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":                 true,
			"empty":              true,
			"window":             args.Window,
			"target_memory_type": string(mt),
			"period_bucket":      args.PeriodBucket,
			"content":            content,
			"updated_at":         updatedAt,
			"chunk_count":        chunkCount,
		}, nil
	}

	ids := make([]string, 0, len(articleSet))
	for id := range articleSet {
		ids = append(ids, id)
	}
	metas, err := t.articleRepo.GetArticlesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	metaByID := make(map[string]storage.ArticleMeta, len(metas))
	for _, meta := range metas {
		metaByID[meta.ArticleID] = meta
	}

	typeCnt := map[string]int{}
	tagCnt := map[string]int{}
	articleCnt := map[string]int{}
	recentTitles := make([]string, 0, minInt(6, len(filtered)))
	seenTitle := map[string]struct{}{}
	for _, it := range filtered {
		meta, ok := metaByID[it.ArticleID]
		if !ok {
			continue
		}
		if title := strings.TrimSpace(meta.Title); title != "" {
			articleCnt[title]++
			if _, ok := seenTitle[title]; !ok && len(recentTitles) < 6 {
				seenTitle[title] = struct{}{}
				recentTitles = append(recentTitles, title)
			}
		}
		if !it.Clicked {
			continue
		}
		weight := maxInt(1, int(it.Preference)+1)
		for _, tt := range splitCSV(meta.TypeTags) {
			typeCnt[tt] += weight
		}
		for _, tg := range splitCSV(meta.Tags) {
			tagCnt[tg] += weight
		}
	}

	recentFocus := topKCountPairs(articleCnt, minInt(args.TopK, 5))
	preferTypes := topKCountPairs(typeCnt, args.TopK)
	preferTags := topKCountPairs(tagCnt, args.TopK)
	content := buildWindowSummaryV2(args.Window, len(filtered), clickedCnt, recentTitles, recentFocus, preferTypes, preferTags)

	updatedAt := time.Now()
	chunkCount, err := upsertMemoryContent(ctx, t.memoryRepo, t.chunkRepo, args.UserID, mt, args.PeriodBucket, content, updatedAt)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"ok":                 true,
		"empty":              false,
		"window":             args.Window,
		"history_count":      len(filtered),
		"clicked_count":      clickedCnt,
		"recent_titles":      recentTitles,
		"recent_focus":       recentFocus,
		"preferred_types":    preferTypes,
		"preferred_tags":     preferTags,
		"target_memory_type": string(mt),
		"period_bucket":      args.PeriodBucket,
		"content":            content,
		"updated_at":         updatedAt,
		"chunk_count":        chunkCount,
	}, nil
}

func buildWindowSummaryV2(window string, historyCnt, clickedCnt int, recentTitles, recentFocus, types, tags []string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("过去 %s 的压缩用户画像：\n", window))
	b.WriteString(fmt.Sprintf("- 行为历史：共 %d 条记录，点击 %d 条。\n", historyCnt, clickedCnt))
	if len(recentTitles) > 0 {
		b.WriteString("- 最近关注：")
		b.WriteString(strings.Join(recentTitles, "、"))
		b.WriteString("\n")
	}
	if len(recentFocus) > 0 {
		b.WriteString("- 最近高频内容：")
		b.WriteString(strings.Join(recentFocus, "、"))
		b.WriteString("\n")
	}
	if len(types) > 0 {
		b.WriteString("- 长期偏好类型：")
		b.WriteString(strings.Join(types, "、"))
		b.WriteString("\n")
	}
	if len(tags) > 0 {
		b.WriteString("- 长期偏好标签：")
		b.WriteString(strings.Join(tags, "、"))
		b.WriteString("\n")
	}
	b.WriteString("- 检索提示：优先召回与最近关注、长期偏好类型和标签都重合的内容。")
	return strings.TrimSpace(b.String())
}

type ToolUserMemoryChunkHybridSearch struct {
	memoryRepo *storage.MemoryRepo
	chunkRepo  *storage.MemoryChunkRepo
}

func NewChunkHybridSearch(memoryRepo *storage.MemoryRepo, chunkRepo *storage.MemoryChunkRepo) *ToolUserMemoryChunkHybridSearch {
	return &ToolUserMemoryChunkHybridSearch{memoryRepo: memoryRepo, chunkRepo: chunkRepo}
}

func (t *ToolUserMemoryChunkHybridSearch) Name() string { return "user_memory_chunk_hybrid_search" }
func (t *ToolUserMemoryChunkHybridSearch) Description() string {
	return "对 user_memory_chunks 做混合检索（dense 向量 + sparse/text 向量），并使用 Milvus Weighted Ranker 进行重排。"
}

func (t *ToolUserMemoryChunkHybridSearch) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user_id":             map[string]any{"type": "string"},
			"memory_type":         map[string]any{"type": "string", "enum": []string{"short_term", "long_term", "periodic"}},
			"period_bucket":       map[string]any{"type": "string"},
			"query_text":          map[string]any{"type": "string"},
			"topk":                map[string]any{"type": "integer", "default": 5},
			"collection":          map[string]any{"type": "string", "default": "user_memory_chunks"},
			"dense_vector_field":  map[string]any{"type": "string", "default": "vector"},
			"sparse_vector_field": map[string]any{"type": "string", "default": "sparse_vector"},
			"weights": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "number"},
				"description": "WeightedRanker 权重，长度必须与 AnnRequest 数一致（默认 [0.5,0.5]）。",
			},
			"norm_score": map[string]any{"type": "boolean", "default": true},
		},
		"required": []string{"user_id", "memory_type", "query_text"},
	}
}

type chunkHybridArgs struct {
	UserID            string    `json:"user_id"`
	MemoryType        string    `json:"memory_type"`
	PeriodBucket      string    `json:"period_bucket"`
	QueryText         string    `json:"query_text"`
	TopK              int       `json:"topk"`
	Collection        string    `json:"collection"`
	DenseVectorField  string    `json:"dense_vector_field"`
	SparseVectorField string    `json:"sparse_vector_field"`
	Weights           []float64 `json:"weights"`
	NormScore         bool      `json:"norm_score"`
}

type memoryChunkHybridHit struct {
	ID         string  `json:"id"`
	Score      float32 `json:"score"`
	Content    string  `json:"content,omitempty"`
	ChunkIndex int64   `json:"chunk_index,omitempty"`
}

type memoryChunkHybridSearchResult struct {
	Empty      bool                   `json:"empty"`
	TopK       int                    `json:"topk"`
	Hits       []memoryChunkHybridHit `json:"hits"`
	Collection string                 `json:"collection"`
	LatencyMs  int64                  `json:"latency_ms"`
	UsedHybrid bool                   `json:"used_hybrid"`
	Note       string                 `json:"note,omitempty"`
}

func (t *ToolUserMemoryChunkHybridSearch) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args chunkHybridArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}
	if t.memoryRepo == nil || t.chunkRepo == nil {
		return nil, errors.New("依赖未注入（memoryRepo/chunkRepo）")
	}

	mt, err := parseMemoryType(args.MemoryType)
	if err != nil {
		return nil, err
	}
	if mt != storage.MemoryPeriodic {
		args.PeriodBucket = ""
	}
	args.UserID = strings.TrimSpace(args.UserID)
	args.PeriodBucket = strings.TrimSpace(args.PeriodBucket)
	args.QueryText = strings.TrimSpace(args.QueryText)
	if args.UserID == "" {
		return nil, errors.New("user_id 不能为空")
	}
	if args.QueryText == "" {
		return nil, errors.New("query_text 不能为空")
	}
	if args.TopK <= 0 {
		args.TopK = 5
	}
	if strings.TrimSpace(args.Collection) == "" {
		args.Collection = "user_memory_chunks"
	}
	if strings.TrimSpace(args.DenseVectorField) == "" {
		args.DenseVectorField = "vector"
	}
	if strings.TrimSpace(args.SparseVectorField) == "" {
		args.SparseVectorField = "sparse_vector"
	}
	if len(args.Weights) == 0 {
		args.Weights = []float64{0.5, 0.5}
	}

	// 1) 读取当前 memory 的 updatedAt（用它的 Unix 作为 version 过滤）
	mem, ok, err := t.memoryRepo.Get(ctx, args.UserID, mt, args.PeriodBucket)
	if err != nil {
		return nil, err
	}
	if !ok || strings.TrimSpace(mem.Content) == "" {
		return memoryChunkHybridSearchResult{Empty: true, TopK: args.TopK, Hits: nil, Collection: args.Collection, UsedHybrid: false, Note: "memory 为空或不存在"}, nil
	}

	// 2) 构造 filter（只看最新 version）
	versionUnix := mem.UpdatedAt.Unix()
	filter := fmt.Sprintf(`user_id == "%s" && memory_type == "%s" && period_bucket == "%s" && version_unix == %d`,
		esc(args.UserID), esc(string(mt)), esc(args.PeriodBucket), versionUnix,
	)

	// 3) dense 向量（项目内用 embedding/service 生成）
	vec, err := service.TextVector(ctx, args.QueryText)
	if err != nil {
		return nil, err
	}

	// 4) 组 AnnRequests（dense + sparse/text）
	denseReq := milvusclient.NewAnnRequest(args.DenseVectorField, args.TopK, entity.FloatVector(vec)).
		WithAnnParam(index.NewAutoAnnParam(1)). // level: 平衡召回与性能
		WithFilter(filter)
	// sparseReq：这里用 entity.Text(args.QueryText) 对齐 Python 的 data=["..."]。
	// 前提：你的 sparse_vector_field 是通过 Milvus 的函数/全文搜索/BM25 等方式可从文本 query 生成稀疏向量。
	sparseReq := milvusclient.NewAnnRequest(args.SparseVectorField, args.TopK, entity.Text(args.QueryText)).
		WithAnnParam(index.NewSparseAnnParam()).
		WithFilter(filter)

	cli := infra.Milvus()
	start := time.Now()
	if cli != nil {
		hits, err := topK(ctx, cli, args.Collection, []*milvusclient.AnnRequest{denseReq, sparseReq}, args.Weights, args.NormScore, args.TopK, "content", "chunk_index")
		if err == nil {
			return memoryChunkHybridSearchResult{
				Empty:      len(hits) == 0,
				TopK:       args.TopK,
				Hits:       hits,
				Collection: args.Collection,
				LatencyMs:  time.Since(start).Milliseconds(),
				UsedHybrid: true,
			}, nil
		}
	}

	// 5) fallback：仅 dense 搜索（如果 Milvus 未启或 hybrid 失败）
	fallbackStart := time.Now()
	chunks, err := t.chunkRepo.SearchMemoryChunks(ctx, args.UserID, mt, args.PeriodBucket, mem.UpdatedAt, vec, args.TopK)
	if err != nil {
		return nil, err
	}
	fallbackHits := make([]memoryChunkHybridHit, 0, len(chunks))
	for _, c := range chunks {
		fallbackHits = append(fallbackHits, memoryChunkHybridHit{Content: c})
	}
	return memoryChunkHybridSearchResult{
		Empty:      len(fallbackHits) == 0,
		TopK:       args.TopK,
		Hits:       fallbackHits,
		Collection: args.Collection,
		LatencyMs:  time.Since(fallbackStart).Milliseconds(),
		UsedHybrid: false,
		Note:       "hybrid 不可用，已降级为 dense-only 搜索（content 从 PG 返回）",
	}, nil
}

// topK：使用 Milvus Go SDK 的 HybridSearch + Function(RERANK) weighted reranker。
//
// 等价于 Python（你贴的示例）：
//
//	rerank = Function(name="weight", input_field_names=[], function_type=RERANK,
//	                  params={"reranker":"weighted","weights":[...],"norm_score":True})
//	milvus_client.hybrid_search(..., ranker=rerank)
func topK(
	ctx context.Context,
	cli *milvusclient.Client,
	collection string,
	annReqs []*milvusclient.AnnRequest,
	weights []float64,
	normScore bool,
	limit int,
	outputFields ...string,
) ([]memoryChunkHybridHit, error) {
	if cli == nil {
		return nil, errors.New("Milvus 客户端未初始化")
	}
	if len(annReqs) == 0 {
		return nil, nil
	}
	if len(weights) != len(annReqs) {
		return nil, fmt.Errorf("weights 长度(%d)必须与 AnnRequest 数(%d)一致", len(weights), len(annReqs))
	}
	if limit <= 0 {
		limit = 10
	}
	if strings.TrimSpace(collection) == "" {
		return nil, errors.New("collection 不能为空")
	}

	rerankFn := entity.NewFunction().
		WithName("weight").
		WithInputFields(). // 必须空列表
		WithType(entity.FunctionTypeRerank).
		WithParam("reranker", "weighted").
		WithParam("weights", weights).
		WithParam("norm_score", normScore)

	opt := milvusclient.NewHybridSearchOption(collection, limit, annReqs...).
		WithFunctionRerankers(rerankFn)
	if len(outputFields) > 0 {
		opt = opt.WithOutputFields(outputFields...)
	}

	rs, err := cli.HybridSearch(ctx, opt)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, nil
	}

	set := rs[0]
	contentCol := set.GetColumn("content")
	chunkIdxCol := set.GetColumn("chunk_index")

	hits := make([]memoryChunkHybridHit, 0, set.ResultCount)
	for i := 0; i < set.ResultCount; i++ {
		id, _ := set.IDs.GetAsString(i)
		score := float32(0)
		if i < len(set.Scores) {
			score = set.Scores[i]
		}

		h := memoryChunkHybridHit{ID: id, Score: score}
		if contentCol != nil {
			if v, err := contentCol.Get(i); err == nil {
				if s, ok := v.(string); ok {
					h.Content = s
				}
			}
		}
		if chunkIdxCol != nil {
			if v, err := chunkIdxCol.Get(i); err == nil {
				switch vv := v.(type) {
				case int64:
					h.ChunkIndex = vv
				case int32:
					h.ChunkIndex = int64(vv)
				case int:
					h.ChunkIndex = int64(vv)
				}
			}
		}
		hits = append(hits, h)
	}
	return hits, nil
}

// topKCounts：用于非 Milvus 场景下的简单 TopK（memory_maintain_window 里统计 type/tag 用）
func topKCounts(m map[string]int, k int) []string {
	if len(m) == 0 {
		return nil
	}
	type kv struct {
		K string
		V int
	}
	arr := make([]kv, 0, len(m))
	for k1, v := range m {
		arr = append(arr, kv{K: k1, V: v})
	}
	sort.Slice(arr, func(i, j int) bool {
		if arr[i].V == arr[j].V {
			return arr[i].K < arr[j].K
		}
		return arr[i].V > arr[j].V
	})
	if k > 0 && len(arr) > k {
		arr = arr[:k]
	}
	res := make([]string, 0, len(arr))
	for _, it := range arr {
		res = append(res, it.K)
	}
	return res
}

func topKCountPairs(m map[string]int, k int) []string {
	base := topKCounts(m, k)
	if len(base) == 0 {
		return nil
	}
	res := make([]string, 0, len(base))
	for _, key := range base {
		res = append(res, fmt.Sprintf("%s(+%d)", key, m[key]))
	}
	return res
}

func upsertMemoryContent(
	ctx context.Context,
	repo *storage.MemoryRepo,
	chunkRepo *storage.MemoryChunkRepo,
	userID string,
	mt storage.MemoryType,
	periodBucket string,
	content string,
	updatedAt time.Time,
) (int, error) {
	if err := repo.Upsert(ctx, storage.UserMemory{
		UserID:       userID,
		MemoryType:   mt,
		PeriodBucket: periodBucket,
		Content:      content,
		UpdatedAt:    updatedAt,
	}); err != nil {
		return 0, err
	}
	return replaceMemoryChunks(ctx, chunkRepo, userID, mt, periodBucket, updatedAt, content)
}

func replaceMemoryChunks(
	ctx context.Context,
	chunkRepo *storage.MemoryChunkRepo,
	userID string,
	mt storage.MemoryType,
	periodBucket string,
	updatedAt time.Time,
	content string,
) (int, error) {
	maxT := config.Cfg.Split.MemoryChunkMaxTokens
	overlapT := config.Cfg.Split.MemoryChunkOverlapTokens
	if maxT <= 0 {
		maxT = 600
	}
	if overlapT < 0 {
		overlapT = 0
	}

	chunks := chunk.SplitByTokenBudget(content, maxT, overlapT)
	vectors := make([][]float32, 0, len(chunks))
	finalChunks := make([]string, 0, len(chunks))
	for _, c := range chunks {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		vec, err := service.TextVector(ctx, c)
		if err != nil {
			return 0, err
		}
		finalChunks = append(finalChunks, c)
		vectors = append(vectors, vec)
	}

	if err := chunkRepo.ReplaceChunks(ctx, userID, mt, periodBucket, updatedAt, finalChunks, vectors); err != nil {
		return 0, err
	}
	return len(finalChunks), nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func esc(s string) string {
	return strings.ReplaceAll(s, `"`, `\\"`)
}
