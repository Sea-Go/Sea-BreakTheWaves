package memory_manage

import (
	"context"
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

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"go.uber.org/zap"
)

// -----------------------------------------------------------------------------
// Tool: user_memory_get
// -----------------------------------------------------------------------------

type UserMemoryGetInput struct {
	UserID       string `json:"user_id" jsonschema:"description=用户 ID,required"`
	MemoryType   string `json:"memory_type" jsonschema:"description=记忆类型,required,enum=short_term,enum=long_term,enum=periodic"`
	PeriodBucket string `json:"period_bucket" jsonschema:"description=当 memory_type=periodic 时可选（例如 d1/w1/weekend）。其他类型忽略。"`
}

func NewGet(repo *storage.MemoryRepo) tool.CallableTool {
	return function.NewFunctionTool(
		func(ctx context.Context, args UserMemoryGetInput) (map[string]any, error) {
			if repo == nil {
				return nil, errors.New("MemoryRepo 未注入")
			}
			mt, err := parseMemoryType(args.MemoryType)
			if err != nil {
				return nil, err
			}
			if mt != storage.MemoryPeriodic {
				args.PeriodBucket = ""
			}
			m, ok, err := repo.Get(ctx, strings.TrimSpace(args.UserID), mt, strings.TrimSpace(args.PeriodBucket))
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
		},
		function.WithName("user_memory_get"),
		function.WithDescription("读取用户记忆（short_term / long_term / periodic）。"),
	)
}

// -----------------------------------------------------------------------------
// Tool: user_memory_upsert
// -----------------------------------------------------------------------------

type UserMemoryUpsertInput struct {
	UserID       string `json:"user_id" jsonschema:"description=用户 ID,required"`
	MemoryType   string `json:"memory_type" jsonschema:"description=记忆类型,required,enum=short_term,enum=long_term,enum=periodic"`
	PeriodBucket string `json:"period_bucket" jsonschema:"description=当 memory_type=periodic 时可选（例如 d1/w1/weekend）。其他类型忽略。"`
	Content      string `json:"content" jsonschema:"description=记忆内容,required"`
}

func NewUpsert(repo *storage.MemoryRepo, chunkRepo *storage.MemoryChunkRepo) tool.CallableTool {
	return function.NewFunctionTool(
		func(ctx context.Context, args UserMemoryUpsertInput) (map[string]any, error) {
			if repo == nil {
				return nil, errors.New("MemoryRepo 未注入")
			}
			if chunkRepo == nil {
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
			if err := repo.Upsert(ctx, storage.UserMemory{
				UserID:       args.UserID,
				MemoryType:   mt,
				PeriodBucket: args.PeriodBucket,
				Content:      args.Content,
				UpdatedAt:    updatedAt,
			}); err != nil {
				return nil, err
			}

			chunkCount, err := replaceMemoryChunks(ctx, chunkRepo, args.UserID, mt, args.PeriodBucket, updatedAt, args.Content)
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
		},
		function.WithName("user_memory_upsert"),
		function.WithDescription("写入/覆盖用户记忆。对 long_term/periodic 会做 tokenize 分块 + 向量化写入 user_memory_chunks（Milvus）。"),
	)
}

// -----------------------------------------------------------------------------
// Tool: memory_maintain_window
// -----------------------------------------------------------------------------

type MaintainWindowInput struct {
	UserID           string `json:"user_id" jsonschema:"description=用户 ID,required"`
	Window           string `json:"window" jsonschema:"description=时间窗口,required,enum=1d,enum=7d"`
	TargetMemoryType string `json:"target_memory_type" jsonschema:"description=目标记忆类型,required,enum=short_term,enum=long_term,enum=periodic"`
	PeriodBucket     string `json:"period_bucket" jsonschema:"description=当 target_memory_type=periodic 时可选（例如 d1/w1/weekend）。其他类型忽略。"`
	TopK             int    `json:"topk" jsonschema:"description=偏好标签/类型 TopK,default=5"`
}

func NewMaintainWindow(historyRepo *storage.UserHistoryRepo, articleRepo *storage.ArticleRepo, memoryRepo *storage.MemoryRepo, chunkRepo *storage.MemoryChunkRepo) tool.CallableTool {
	return function.NewFunctionTool(
		func(ctx context.Context, args MaintainWindowInput) (map[string]any, error) {
			if historyRepo == nil || articleRepo == nil || memoryRepo == nil || chunkRepo == nil {
				return nil, errors.New("依赖未注入")
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

			hist, err := historyRepo.ListRecent(ctx, args.UserID, 500)
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
				chunkCount, err := upsertMemoryContent(ctx, memoryRepo, chunkRepo, args.UserID, mt, args.PeriodBucket, content, updatedAt)
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
			metas, err := articleRepo.GetArticlesByIDs(ctx, ids)
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
			chunkCount, err := upsertMemoryContent(ctx, memoryRepo, chunkRepo, args.UserID, mt, args.PeriodBucket, content, updatedAt)
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
		},
		function.WithName("memory_maintain_window"),
		function.WithDescription("基于用户过去 1 天 / 7 天行为生成偏好摘要，并写回 user_memory（short_term/long_term/periodic）。"),
	)
}

// -----------------------------------------------------------------------------
// Tool: user_memory_chunk_hybrid_search
// -----------------------------------------------------------------------------

type ChunkHybridSearchInput struct {
	UserID            string    `json:"user_id" jsonschema:"description=用户 ID,required"`
	MemoryType        string    `json:"memory_type" jsonschema:"description=记忆类型,required,enum=short_term,enum=long_term,enum=periodic"`
	PeriodBucket      string    `json:"period_bucket" jsonschema:"description=periodic 时使用的时间桶"`
	QueryText         string    `json:"query_text" jsonschema:"description=检索文本,required"`
	TopK              int       `json:"topk" jsonschema:"description=返回条数,default=5"`
	Collection        string    `json:"collection" jsonschema:"description=Milvus collection 名称,default=user_memory_chunks"`
	DenseVectorField  string    `json:"dense_vector_field" jsonschema:"description=dense 向量字段名,default=vector"`
	SparseVectorField string    `json:"sparse_vector_field" jsonschema:"description=sparse 向量字段名,default=sparse_vector"`
	Weights           []float64 `json:"weights" jsonschema:"description=WeightedRanker 权重"`
	NormScore         bool      `json:"norm_score" jsonschema:"description=是否归一化分数,default=true"`
}

func NewChunkHybridSearch(memoryRepo *storage.MemoryRepo, chunkRepo *storage.MemoryChunkRepo) tool.CallableTool {
	return function.NewFunctionTool(
		func(ctx context.Context, args ChunkHybridSearchInput) (memoryChunkHybridSearchResult, error) {
			if memoryRepo == nil || chunkRepo == nil {
				return memoryChunkHybridSearchResult{}, errors.New("依赖未注入")
			}
			mt, err := parseMemoryType(args.MemoryType)
			if err != nil {
				return memoryChunkHybridSearchResult{}, err
			}
			if mt != storage.MemoryPeriodic {
				args.PeriodBucket = ""
			}
			args.UserID = strings.TrimSpace(args.UserID)
			args.PeriodBucket = strings.TrimSpace(args.PeriodBucket)
			args.QueryText = strings.TrimSpace(args.QueryText)
			if args.UserID == "" {
				return memoryChunkHybridSearchResult{}, errors.New("user_id 不能为空")
			}
			if args.QueryText == "" {
				return memoryChunkHybridSearchResult{}, errors.New("query_text 不能为空")
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

			mem, ok, err := memoryRepo.Get(ctx, args.UserID, mt, args.PeriodBucket)
			if err != nil {
				return memoryChunkHybridSearchResult{}, err
			}
			if !ok || strings.TrimSpace(mem.Content) == "" {
				return memoryChunkHybridSearchResult{
					Empty:      true,
					TopK:       args.TopK,
					Hits:       nil,
					Collection: args.Collection,
					UsedHybrid: false,
					Note:       "memory 为空或不存在",
				}, nil
			}

			versionUnix := mem.UpdatedAt.Unix()
			filter := fmt.Sprintf(`user_id == "%s" && memory_type == "%s" && period_bucket == "%s" && version_unix == %d`,
				esc(args.UserID), esc(string(mt)), esc(args.PeriodBucket), versionUnix,
			)

			vec, err := service.TextVector(ctx, args.QueryText)
			if err != nil {
				return memoryChunkHybridSearchResult{}, err
			}

			denseReq := milvusclient.NewAnnRequest(args.DenseVectorField, args.TopK, entity.FloatVector(vec)).
				WithAnnParam(index.NewAutoAnnParam(1)).
				WithFilter(filter)
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

			fallbackStart := time.Now()
			chunks, err := chunkRepo.SearchMemoryChunks(ctx, args.UserID, mt, args.PeriodBucket, mem.UpdatedAt, vec, args.TopK)
			if err != nil {
				return memoryChunkHybridSearchResult{}, err
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
		},
		function.WithName("user_memory_chunk_hybrid_search"),
		function.WithDescription("对 user_memory_chunks 做混合检索（dense 向量 + sparse/text 向量），并使用 Milvus Weighted Ranker 进行重排。"),
	)
}

// -----------------------------------------------------------------------------
// 内部类型
// -----------------------------------------------------------------------------

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

// -----------------------------------------------------------------------------
// 工具函数
// -----------------------------------------------------------------------------

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
		WithInputFields().
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
