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
	"github.com/openai/openai-go/v3"
	"go.uber.org/zap"
)

type ToolDocIngest struct {
	articleRepo *storage.ArticleRepo
	ai          *openai.Client
}

func New(dbRepo *storage.ArticleRepo, ai *openai.Client) *ToolDocIngest {
	return &ToolDocIngest{articleRepo: dbRepo, ai: ai}
}

func (t *ToolDocIngest) Name() string {
	return "doc_ingest"
}

func (t *ToolDocIngest) Description() string {
	return "将文章切分、向量化，并写入 PG、Milvus 与可选 GraphRAG。"
}

func (t *ToolDocIngest) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"article_id":   map[string]any{"type": "string", "description": "文章 ID，可选"},
			"score":        map[string]any{"type": "number", "description": "文章基础分", "default": 0},
			"article_json": map[string]any{"type": "string", "description": "结构化文章 JSON"},
			"markdown":     map[string]any{"type": "string", "description": "Markdown 文章"},
		},
		"required": []string{},
	}
}

type ingestArgs struct {
	ArticleID   string   `json:"article_id"`
	Score       float64  `json:"score"`
	ArticleJSON string   `json:"article_json"`
	Markdown    string   `json:"markdown"`
	Title       string   `json:"title"`
	Cover       string   `json:"cover"`
	TypeTags    []string `json:"type_tags"`
	Tags        []string `json:"tags"`
}

type ingestResult struct {
	ArticleID            string `json:"article_id"`
	CoarseVectorInserted bool   `json:"coarse_vector_inserted"`
	FineVectorInserted   int    `json:"fine_vector_inserted"`
	FineChunkCount       int    `json:"fine_chunk_count"`
	ImageVectorInserted  int    `json:"image_vector_inserted"`
	ImageChunkCount      int    `json:"image_chunk_count"`
	GraphEnabled         bool   `json:"graph_enabled"`
	GraphWriteOK         bool   `json:"graph_write_ok"`
}

func (t *ToolDocIngest) generateCoarseIntro(ctx context.Context, article chunk.Article, splitRes chunk.SplitResult) (string, string, error) {
	fallback := strings.TrimSpace(splitRes.CoarseIntro)
	if t.ai == nil {
		return fallback, "", nil
	}

	sys := "You rewrite article retrieval metadata into a short article-like intro for coarse recall. Keep every concrete title, type, keyword and heading signal grounded in the input. Do not invent facts. Return plain text only."
	user := strings.TrimSpace(strings.Join([]string{
		"title: " + strings.TrimSpace(article.Title),
		"type_tags: " + strings.Join(article.TypeTags, ", "),
		"keywords: " + strings.Join(splitRes.Keywords, ", "),
		"headings: " + strings.Join(collectHeadings(article), " | "),
		"task: write a compact intro in Chinese, within 120 Chinese characters when possible, article-like but retrieval-friendly.",
	}, "\n"))

	resp, err := t.ai.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: config.Cfg.Agent.Model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(sys),
			openai.UserMessage(user),
		},
		Temperature: openai.Float(0.1),
	})
	if err != nil {
		return fallback, config.Cfg.Agent.Model, err
	}

	content := ""
	if len(resp.Choices) > 0 {
		content = strings.TrimSpace(resp.Choices[0].Message.Content)
	}
	if content == "" {
		content = fallback
	}
	return content, config.Cfg.Agent.Model, nil
}

func (t *ToolDocIngest) Invoke(ctx context.Context, argsRaw json.RawMessage) (any, error) {
	var args ingestArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return nil, err
	}

	article, err := parseArticle(args)
	if err != nil {
		return nil, err
	}
	article.Score = args.Score

	splitRes, err := chunk.SplitArticle(
		article,
		config.Cfg.Split.ChunkMaxTokens,
		config.Cfg.Split.ChunkOverlapTokens,
		config.Cfg.Split.KeywordTopK,
	)
	if err != nil {
		return nil, err
	}
	coarseIntro, introModel, introErr := t.generateCoarseIntro(ctx, article, splitRes)
	if introErr != nil {
		zlog.L().Warn("generate coarse intro failed, fallback to deterministic intro", zap.Error(introErr), zap.String("article_id", article.ArticleID))
	}
	if strings.TrimSpace(coarseIntro) != "" {
		splitRes.CoarseIntro = strings.TrimSpace(coarseIntro)
		splitRes.CoarseText = chunk.ComposeCoarseText(splitRes.CoarseRawText, splitRes.CoarseIntro, splitRes.KeywordScore)
	}

	if t.articleRepo == nil {
		return nil, errors.New("article repo 未注入")
	}
	if err := DeleteArticleState(ctx, t.articleRepo, article.ArticleID); err != nil {
		return nil, err
	}
	if err := t.articleRepo.UpsertArticle(ctx, article); err != nil {
		return nil, err
	}

	allChunks := append([]chunk.Chunk{}, splitRes.FineChunks...)
	allChunks = append(allChunks, splitRes.ImageChunks...)
	if err := t.articleRepo.UpsertChunks(ctx, allChunks); err != nil {
		return nil, err
	}

	cli := infra.Milvus()
	if cli == nil {
		return nil, errors.New("Milvus 客户端未初始化")
	}

	coarseInserted, err := insertCoarse(ctx, cli, article, splitRes.CoarseText, splitRes.Keywords, splitRes.KeywordScore)
	if err != nil {
		return nil, err
	}

	fineInserted, fineVectors, err := insertFine(ctx, cli, article, splitRes.FineChunks, splitRes.Keywords)
	if err != nil {
		return nil, err
	}

	imageInserted, err := insertImages(ctx, cli, article, splitRes.ImageChunks, splitRes.Keywords)
	if err != nil {
		return nil, err
	}

	graphEnabled := false
	graphWriteOK := false
	if drv := infra.Neo4j(); drv != nil {
		graphEnabled = true
		store := graphschema.NewStore(drv)
		if store != nil {
			if err := ingestGraphRAG(ctx, store, article, splitRes, fineVectors); err == nil {
				graphWriteOK = true
			} else {
				zlog.L().Warn("GraphRAG 写入失败，已降级", zap.Error(err), zap.String("article_id", article.ArticleID))
			}
		}
	}

	_, sp := zlog.StartSpan(ctx, "side_effect.doc_ingest")
	sp.End(zlog.StatusOK, nil, zap.Any("side_effect", map[string]any{
		"type":               "doc_ingest",
		"outcome":            "OK",
		"article_id":         article.ArticleID,
		"coarse_intro_model": introModel,
		"keyword_score":      splitRes.KeywordScore,
		"fine_chunk_count":   len(splitRes.FineChunks),
		"image_chunk_count":  len(splitRes.ImageChunks),
		"graph_enabled":      graphEnabled,
		"graph_write_ok":     graphWriteOK,
	}))

	return ingestResult{
		ArticleID:            article.ArticleID,
		CoarseVectorInserted: coarseInserted,
		FineVectorInserted:   fineInserted,
		FineChunkCount:       len(splitRes.FineChunks),
		ImageVectorInserted:  imageInserted,
		ImageChunkCount:      len(splitRes.ImageChunks),
		GraphEnabled:         graphEnabled,
		GraphWriteOK:         graphWriteOK,
	}, nil
}

func parseArticle(args ingestArgs) (chunk.Article, error) {
	var article chunk.Article
	if strings.TrimSpace(args.ArticleJSON) != "" {
		if err := json.Unmarshal([]byte(args.ArticleJSON), &article); err != nil {
			return chunk.Article{}, fmt.Errorf("解析 article_json 失败: %w", err)
		}
	} else if strings.TrimSpace(args.Markdown) != "" {
		parsed, err := chunk.ParseMarkdownArticle(args.ArticleID, args.Markdown, args.Title)
		if err != nil {
			return chunk.Article{}, err
		}
		article = parsed
	} else {
		return chunk.Article{}, errors.New("必须提供 article_json 或 markdown")
	}

	if strings.TrimSpace(args.ArticleID) != "" {
		article.ArticleID = strings.TrimSpace(args.ArticleID)
	}
	if strings.TrimSpace(args.Title) != "" {
		article.Title = strings.TrimSpace(args.Title)
	}
	if strings.TrimSpace(args.Cover) != "" {
		article.Cover = strings.TrimSpace(args.Cover)
	}
	if len(args.TypeTags) > 0 {
		article.TypeTags = append([]string(nil), args.TypeTags...)
	}
	if len(args.Tags) > 0 {
		article.Tags = append([]string(nil), args.Tags...)
	}
	article.ArticleID = chunk.NormalizeArticleID(article.ArticleID, article)
	return article, nil
}

func insertCoarse(
	ctx context.Context,
	cli *milvusclient.Client,
	article chunk.Article,
	coarseText string,
	keywords []string,
	keywordScore float32,
) (bool, error) {
	vec, err := service.TextVector(ctx, coarseText)
	if err != nil {
		return false, err
	}

	collection := config.Cfg.Milvus.Collections.Coarse
	dim := config.Cfg.Milvus.Collections.Dim
	tags := buildTags(article, keywords)
	now := time.Now().Unix()

	opt := milvusclient.NewColumnBasedInsertOption(
		collection,
		column.NewColumnVarChar("id", []string{article.ArticleID}),
		column.NewColumnFloatVector("vector", dim, [][]float32{vec}),
		column.NewColumnVarChar("article_id", []string{article.ArticleID}),
		column.NewColumnFloat("score", []float32{float32(article.Score) + keywordScore}),
		column.NewColumnVarChar("tags", []string{tags}),
		column.NewColumnInt64("created_at_unix", []int64{now}),
	)

	if _, err := cli.Upsert(ctx, opt); err != nil {
		return false, err
	}
	return true, nil
}

func insertFine(
	ctx context.Context,
	cli *milvusclient.Client,
	article chunk.Article,
	chunks []chunk.Chunk,
	keywords []string,
) (int, map[string][]float32, error) {
	if len(chunks) == 0 {
		return 0, map[string][]float32{}, nil
	}

	tasks := make([]service.BatchTask, 0, len(chunks))
	for _, item := range chunks {
		tasks = append(tasks, service.BatchTask{
			ID:    item.ChunkID,
			Kind:  "text",
			Input: item.Content,
		})
	}

	vectors, err := service.BatchVectors(ctx, tasks)
	if err != nil {
		return 0, nil, err
	}

	collection := config.Cfg.Milvus.Collections.Fine
	dim := config.Cfg.Milvus.Collections.Dim
	tags := buildTags(article, keywords)
	now := time.Now().Unix()

	ids := make([]string, 0, len(chunks))
	vecs := make([][]float32, 0, len(chunks))
	articleIDs := make([]string, 0, len(chunks))
	chunkIDs := make([]string, 0, len(chunks))
	h2s := make([]string, 0, len(chunks))
	documents := make([]string, 0, len(chunks))
	tagList := make([]string, 0, len(chunks))
	scores := make([]float32, 0, len(chunks))
	createdAt := make([]int64, 0, len(chunks))

	for _, item := range chunks {
		vector, ok := vectors[item.ChunkID]
		if !ok {
			return 0, nil, fmt.Errorf("missing vector for chunk %s", item.ChunkID)
		}
		ids = append(ids, item.ChunkID)
		vecs = append(vecs, vector)
		articleIDs = append(articleIDs, item.ArticleID)
		chunkIDs = append(chunkIDs, item.ChunkID)
		h2s = append(h2s, item.H2)
		documents = append(documents, item.Content)
		tagList = append(tagList, tags)
		scores = append(scores, float32(article.Score))
		createdAt = append(createdAt, now)
	}

	opt := milvusclient.NewColumnBasedInsertOption(
		collection,
		column.NewColumnVarChar("id", ids),
		column.NewColumnFloatVector("vector", dim, vecs),
		column.NewColumnVarChar("article_id", articleIDs),
		column.NewColumnVarChar("chunk_id", chunkIDs),
		column.NewColumnVarChar("h2", h2s),
		column.NewColumnVarChar("document", documents),
		column.NewColumnVarChar("tags", tagList),
		column.NewColumnFloat("score", scores),
		column.NewColumnInt64("created_at_unix", createdAt),
	)

	if _, err := cli.Upsert(ctx, opt); err != nil {
		return 0, nil, err
	}
	return len(chunks), vectors, nil
}

func insertImages(
	ctx context.Context,
	cli *milvusclient.Client,
	article chunk.Article,
	chunks []chunk.Chunk,
	keywords []string,
) (int, error) {
	if len(chunks) == 0 {
		return 0, nil
	}

	tasks := make([]service.BatchTask, 0, len(chunks))
	for _, item := range chunks {
		kind := item.ContentType
		if kind == "" {
			kind = "image"
		}
		tasks = append(tasks, service.BatchTask{
			ID:    item.ChunkID,
			Kind:  kind,
			Input: item.Content,
		})
	}

	vectors, err := service.BatchVectors(ctx, tasks)
	if err != nil {
		return 0, err
	}

	collection := strings.TrimSpace(config.Cfg.Milvus.Collections.Image)
	if collection == "" {
		collection = "recall_image"
	}
	dim := config.Cfg.Milvus.Collections.Dim
	tags := buildTags(article, keywords)
	now := time.Now().Unix()

	ids := make([]string, 0, len(chunks))
	vecs := make([][]float32, 0, len(chunks))
	articleIDs := make([]string, 0, len(chunks))
	chunkIDs := make([]string, 0, len(chunks))
	h2s := make([]string, 0, len(chunks))
	documents := make([]string, 0, len(chunks))
	tagList := make([]string, 0, len(chunks))
	scores := make([]float32, 0, len(chunks))
	createdAt := make([]int64, 0, len(chunks))

	for _, item := range chunks {
		vector, ok := vectors[item.ChunkID]
		if !ok {
			return 0, fmt.Errorf("missing vector for image chunk %s", item.ChunkID)
		}
		ids = append(ids, item.ChunkID)
		vecs = append(vecs, vector)
		articleIDs = append(articleIDs, item.ArticleID)
		chunkIDs = append(chunkIDs, item.ChunkID)
		h2s = append(h2s, item.H2)
		documents = append(documents, item.Content)
		tagList = append(tagList, tags)
		scores = append(scores, float32(article.Score))
		createdAt = append(createdAt, now)
	}

	opt := milvusclient.NewColumnBasedInsertOption(
		collection,
		column.NewColumnVarChar("id", ids),
		column.NewColumnFloatVector("vector", dim, vecs),
		column.NewColumnVarChar("article_id", articleIDs),
		column.NewColumnVarChar("chunk_id", chunkIDs),
		column.NewColumnVarChar("h2", h2s),
		column.NewColumnVarChar("document", documents),
		column.NewColumnVarChar("tags", tagList),
		column.NewColumnFloat("score", scores),
		column.NewColumnInt64("created_at_unix", createdAt),
	)

	if _, err := cli.Upsert(ctx, opt); err != nil {
		return 0, err
	}
	return len(chunks), nil
}

func buildTags(article chunk.Article, keywords []string) string {
	return strings.Join(append(append([]string{}, article.TypeTags...), keywords...), ",")
}

func collectHeadings(article chunk.Article) []string {
	out := make([]string, 0, len(article.Sections))
	for _, sec := range article.Sections {
		h2 := strings.TrimSpace(sec.H2)
		if h2 == "" {
			continue
		}
		out = append(out, h2)
	}
	return out
}

func ingestGraphRAG(
	ctx context.Context,
	store *graphschema.Store,
	article chunk.Article,
	splitRes chunk.SplitResult,
	fineVectors map[string][]float32,
) error {
	if store == nil {
		return errors.New("graph store is nil")
	}

	tag := strings.Join(article.TypeTags, ",")
	if err := store.UpsertParent(ctx, graphschema.ParentNode{
		NodeID:    "parent:" + article.ArticleID,
		ArticleID: article.ArticleID,
		Title:     article.Title,
		Tag:       tag,
		Keywords:  splitRes.Keywords,
	}); err != nil {
		return err
	}

	for _, item := range splitRes.FineChunks {
		if err := store.UpsertChild(ctx, graphschema.ChildNode{
			NodeID:   "child:" + item.ChunkID,
			ChunkID:  item.ChunkID,
			Title:    item.H2,
			Tag:      tag,
			Keywords: splitRes.Keywords,
		}); err != nil {
			return err
		}

		if err := store.LinkHasChild(ctx, article.ArticleID, item.ChunkID); err != nil {
			return err
		}
	}

	for i := 0; i+1 < len(splitRes.FineChunks); i++ {
		from := splitRes.FineChunks[i].ChunkID
		to := splitRes.FineChunks[i+1].ChunkID
		_ = store.LinkNext(ctx, from, to)
		_ = store.LinkNext(ctx, to, from)
	}

	edges := graphschema.BuildSimilarEdges(fineVectors, 4, 0.82)
	for from, outs := range edges {
		for _, edge := range outs {
			_ = store.LinkSimilar(ctx, from, edge.ToNodeID, edge.Weight)
		}
	}

	return nil
}
