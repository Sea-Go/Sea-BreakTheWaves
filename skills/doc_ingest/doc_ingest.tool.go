package doc_ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"sea/chunk"
	"sea/config"
	graphschema "sea/embedding/schema/graph"
	"sea/embedding/service"
	"sea/infra"
	"sea/storage"
	"sea/zlog"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"go.uber.org/zap"
)

// ToolDocIngest：文档入库工具（切分 -> 向量化 -> Milvus + PG + GraphRAG）。
type ToolDocIngest struct {
	articleRepo *storage.ArticleRepo
}

func New(dbRepo *storage.ArticleRepo) *ToolDocIngest {
	return &ToolDocIngest{articleRepo: dbRepo}
}

func (t *ToolDocIngest) Name() string {
	return "doc_ingest"
}

func (t *ToolDocIngest) Description() string {
	return "把文章文档入库：按约定切分为粗召回/精召回文本，生成向量并写入 Milvus，同时把原文与 chunk 记录写入 Postgres；若 Neo4j 可用则写入 GraphRAG（parent/child/edges）。"
}

func (t *ToolDocIngest) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"article_id":   map[string]any{"type": "string", "description": "文章ID（可选；为空则自动生成）"},
			"score":        map[string]any{"type": "number", "description": "文章基础分（入池前分数）", "default": 0},
			"article_json": map[string]any{"type": "string", "description": "文章 JSON（与 chunk.Article 对齐）。优先级高于 markdown"},
			"markdown":     map[string]any{"type": "string", "description": "符合约定的 Markdown 文档"},
		},
		"required": []string{},
	}
}

type ingestArgs struct {
	ArticleID   string  `json:"article_id"`
	Score       float64 `json:"score"`
	ArticleJSON string  `json:"article_json"`
	Markdown    string  `json:"markdown"`
}

type ingestResult struct {
	ArticleID            string `json:"article_id"`
	CoarseVectorInserted bool   `json:"coarse_vector_inserted"`
	FineVectorInserted   int    `json:"fine_vector_inserted"`
	FineChunkCount       int    `json:"fine_chunk_count"`
	GraphEnabled         bool   `json:"graph_enabled"`
	GraphWriteOK         bool   `json:"graph_write_ok"`
}

func (t *ToolDocIngest) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args ingestArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}

	var a chunk.Article
	if strings.TrimSpace(args.ArticleJSON) != "" {
		if err := json.Unmarshal([]byte(args.ArticleJSON), &a); err != nil {
			return nil, fmt.Errorf("解析 article_json 失败: %w", err)
		}
	} else if strings.TrimSpace(args.Markdown) != "" {
		parsed, err := chunk.ParseMarkdownArticle(args.ArticleID, args.Markdown)
		if err != nil {
			return nil, err
		}
		a = parsed
	} else {
		return nil, errors.New("必须提供 article_json 或 markdown")
	}

	if strings.TrimSpace(args.ArticleID) != "" {
		a.ArticleID = strings.TrimSpace(args.ArticleID)
	}
	a.ArticleID = chunk.NormalizeArticleID(a.ArticleID, a)
	a.Score = args.Score

	splitRes, err := chunk.SplitArticle(
		a,
		config.Cfg.Split.ChunkMaxTokens,
		config.Cfg.Split.ChunkOverlapTokens,
		config.Cfg.Split.KeywordTopK,
	)
	if err != nil {
		return nil, err
	}

	// 1) 写入 PG（文章元信息 + chunks 原文）
	if t.articleRepo == nil {
		return nil, errors.New("ArticleRepo 未注入")
	}
	if err := t.articleRepo.UpsertArticle(ctx, a); err != nil {
		return nil, err
	}
	if err := t.articleRepo.UpsertChunks(ctx, splitRes.FineChunks); err != nil {
		return nil, err
	}

	// 2) 写入 Milvus（coarse + fine）
	cli := infra.Milvus()
	if cli == nil {
		return nil, errors.New("Milvus 客户端未初始化")
	}

	coarseInserted, err := insertCoarse(ctx, cli, a, splitRes.CoarseText, splitRes.Keywords)
	if err != nil {
		return nil, err
	}
	fineInserted, fineVectors, err := insertFine(ctx, cli, a, splitRes.FineChunks, splitRes.Keywords)
	if err != nil {
		return nil, err
	}

	// 3) GraphRAG（Neo4j 可用时写入）
	graphEnabled := false
	graphWriteOK := false
	if drv := infra.Neo4j(); drv != nil {
		graphEnabled = true
		store := graphschema.NewStore(drv)
		if store != nil {
			if err := ingestGraphRAG(ctx, store, a, splitRes, fineVectors); err == nil {
				graphWriteOK = true
			} else {
				zlog.L().Warn("GraphRAG 写入失败（已降级）", zap.Error(err), zap.String("article_id", a.ArticleID))
			}
		}
	}

	// 观测：入库成功
	_, sp := zlog.StartSpan(ctx, "side_effect.doc_ingest")
	sp.End(zlog.StatusOK, nil, zap.Any("side_effect", map[string]any{
		"type":             "doc_ingest",
		"outcome":          "OK",
		"article_id":       a.ArticleID,
		"fine_chunk_count": len(splitRes.FineChunks),
		"graph_enabled":    graphEnabled,
		"graph_write_ok":   graphWriteOK,
	}))

	return ingestResult{
		ArticleID:            a.ArticleID,
		CoarseVectorInserted: coarseInserted,
		FineVectorInserted:   fineInserted,
		FineChunkCount:       len(splitRes.FineChunks),
		GraphEnabled:         graphEnabled,
		GraphWriteOK:         graphWriteOK,
	}, nil
}

func insertCoarse(
	ctx context.Context,
	cli *milvusclient.Client,
	a chunk.Article,
	coarseText string,
	keywords []string,
) (bool, error) {
	vec, err := service.TextVector(ctx, coarseText)
	if err != nil {
		return false, err
	}

	collection := config.Cfg.Milvus.Collections.Coarse
	dim := config.Cfg.Milvus.Collections.Dim
	tags := strings.Join(append(append([]string{}, a.TypeTags...), keywords...), ",")
	now := time.Now().Unix()

	// ✅ 关键修复：id 字段 schema 是 VarChar，插入也必须用 VarChar Column
	idCol := column.NewColumnVarChar("id", []string{a.ArticleID})
	vecCol := column.NewColumnFloatVector("vector", dim, [][]float32{vec})
	articleIDCol := column.NewColumnVarChar("article_id", []string{a.ArticleID})
	scoreCol := column.NewColumnFloat("score", []float32{float32(a.Score)})
	tagsCol := column.NewColumnVarChar("tags", []string{tags})
	createdCol := column.NewColumnInt64("created_at_unix", []int64{now})

	opt := milvusclient.NewColumnBasedInsertOption(
		collection,
		idCol, vecCol, articleIDCol, scoreCol, tagsCol, createdCol,
	)

	if _, err := cli.Insert(ctx, opt); err != nil {
		return false, err
	}
	return true, nil
}

func insertFine(
	ctx context.Context,
	cli *milvusclient.Client,
	a chunk.Article,
	chunks []chunk.Chunk,
	keywords []string,
) (int, map[string][]float32, error) {
	collection := config.Cfg.Milvus.Collections.Fine
	dim := config.Cfg.Milvus.Collections.Dim
	tags := strings.Join(append(append([]string{}, a.TypeTags...), keywords...), ",")
	now := time.Now().Unix()

	vectors := make(map[string][]float32, len(chunks))
	inserted := 0
	for _, c := range chunks {
		vec, err := service.TextVector(ctx, c.Content)
		if err != nil {
			return inserted, vectors, err
		}
		vectors[c.ChunkID] = vec

		// ✅ 关键修复：fine schema 要求 document 字段，插入时必须同步传值
		idCol := column.NewColumnVarChar("id", []string{c.ChunkID})
		vecCol := column.NewColumnFloatVector("vector", dim, [][]float32{vec})
		articleIDCol := column.NewColumnVarChar("article_id", []string{c.ArticleID})
		chunkIDCol := column.NewColumnVarChar("chunk_id", []string{c.ChunkID})
		h2Col := column.NewColumnVarChar("h2", []string{c.H2})
		documentCol := column.NewColumnVarChar("document", []string{c.Content})
		tagsCol := column.NewColumnVarChar("tags", []string{tags})
		scoreCol := column.NewColumnFloat("score", []float32{float32(a.Score)})
		createdCol := column.NewColumnInt64("created_at_unix", []int64{now})

		opt := milvusclient.NewColumnBasedInsertOption(
			collection,
			idCol, vecCol, articleIDCol, chunkIDCol, h2Col, documentCol, tagsCol, scoreCol, createdCol,
		)

		if _, err := cli.Insert(ctx, opt); err != nil {
			return inserted, vectors, err
		}
		inserted++
	}

	return inserted, vectors, nil
}

func ingestGraphRAG(
	ctx context.Context,
	store *graphschema.Store,
	a chunk.Article,
	splitRes chunk.SplitResult,
	fineVectors map[string][]float32,
) error {
	if store == nil {
		return errors.New("graph store is nil")
	}

	tag := strings.Join(a.TypeTags, ",")

	// parent_node：粗召回信息（标题/封面/type/关键词/二级标题集合）
	if err := store.UpsertParent(ctx, graphschema.ParentNode{
		NodeID:    "parent:" + a.ArticleID,
		ArticleID: a.ArticleID,
		Title:     a.Title,
		Tag:       tag,
		Keywords:  splitRes.Keywords,
	}); err != nil {
		return err
	}

	// child_node：精召回 chunk
	for _, ch := range splitRes.FineChunks {
		if err := store.UpsertChild(ctx, graphschema.ChildNode{
			NodeID:   "child:" + ch.ChunkID,
			ChunkID:  ch.ChunkID,
			Title:    ch.H2,
			Tag:      tag,
			Keywords: splitRes.Keywords,
		}); err != nil {
			return err
		}

		if err := store.LinkHasChild(ctx, a.ArticleID, ch.ChunkID); err != nil {
			return err
		}
	}

	// NEXT 边：同一文章 chunk 相邻
	for i := 0; i+1 < len(splitRes.FineChunks); i++ {
		from := splitRes.FineChunks[i].ChunkID
		to := splitRes.FineChunks[i+1].ChunkID
		_ = store.LinkNext(ctx, from, to)
		_ = store.LinkNext(ctx, to, from)
	}

	// SIMILAR 边：按向量余弦相似度建边（图稀疏化）
	edges := graphschema.BuildSimilarEdges(fineVectors, 4, 0.82)
	for from, outs := range edges {
		for _, e := range outs {
			_ = store.LinkSimilar(ctx, from, e.ToNodeID, e.Weight)
		}
	}

	return nil
}
