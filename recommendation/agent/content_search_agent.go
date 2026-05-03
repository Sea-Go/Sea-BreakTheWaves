package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"sea/config"
	embeddingservice "sea/embedding/service"
	"sea/infra"
	searchsvc "sea/service"
	"sea/skillsys"
	"sea/storage"
	"sea/zlog"

	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"github.com/openai/openai-go/v3"
	"go.uber.org/zap"
)

// ContentSearchRequest 内容搜索接口请求。
// 说明：
//   - 不依赖用户记忆；只对用户输入做意图分析。
//   - RecallK 表示向量召回候选 chunk 数；TopK 表示最终返回文章数。
type ContentSearchRequest struct {
	SearchRequestID string  `json:"search_request_id,omitempty"`
	RequestID       string  `json:"request_id,omitempty"`
	Query           string  `json:"query" binding:"required"`
	TopK            int     `json:"topk,omitempty"`
	TopKLegacy      int     `json:"top_k,omitempty"`
	RecallK         int     `json:"recall_k,omitempty"`
	CoarseRecallK   int     `json:"coarse_recall_k,omitempty"`
	MinPassScore    float32 `json:"min_pass_score,omitempty"`
	Explain         bool    `json:"explain,omitempty"`
}

// ContentSearchIntent 表示“问题 -> 检索意图”的结构化结果。
type ContentSearchIntent struct {
	Label      string   `json:"label"`
	Confidence float64  `json:"confidence"`
	SearchText string   `json:"search_text"`
	Keywords   []string `json:"keywords,omitempty"`
	Signals    []string `json:"signals,omitempty"`
}

// ContentSearchItem 最终返回的文章结果。
type ContentSearchItem struct {
	ArticleID    string  `json:"article_id"`
	Title        string  `json:"title,omitempty"`
	Cover        string  `json:"cover,omitempty"`
	TypeTags     string  `json:"type_tags,omitempty"`
	Tags         string  `json:"tags,omitempty"`
	H2           string  `json:"h2,omitempty"`
	Snippet      string  `json:"snippet,omitempty"`
	ChunkID      string  `json:"chunk_id,omitempty"`
	ArticleScore float32 `json:"article_score,omitempty"`
	VectorScore  float32 `json:"vector_score,omitempty"`
	RerankScore  float32 `json:"rerank_score,omitempty"`
	MatchScore   float32 `json:"match_score,omitempty"`
}

// ContentSearchResponse 内容搜索接口响应。
type ContentSearchResponse struct {
	TraceID         string              `json:"trace_id"`
	SearchRequestID string              `json:"search_request_id"`
	Status          string              `json:"status"`
	Intent          ContentSearchIntent `json:"intent"`
	Items           []ContentSearchItem `json:"items"`
	Explanation     string              `json:"explanation,omitempty"`
	ExplainTrace    any                 `json:"explain_trace,omitempty"`
}

// ContentSearchAgent 独立内容搜索 Agent。
// 约束：
//   - 不读取用户记忆。
//   - 只做 query intent analysis -> embedding recall -> 应用层阿里百炼 rerank。
type ContentSearchAgent struct {
	ai          *openai.Client
	registry    *skillsys.Registry
	articleRepo *storage.ArticleRepo
}

func NewContentSearchAgent(ai *openai.Client, reg *skillsys.Registry, articleRepo *storage.ArticleRepo) *ContentSearchAgent {
	return &ContentSearchAgent{
		ai:          ai,
		registry:    reg,
		articleRepo: articleRepo,
	}
}

type contentVectorCandidate struct {
	ID          string
	ArticleID   string
	ChunkID     string
	H2          string
	VectorScore float32
}

type contentRerankHit struct {
	ID          string
	ArticleID   string
	ChunkID     string
	H2          string
	Document    string
	RerankScore float32
}

type dashscopeTextRerankOutput struct {
	Model       string `json:"model"`
	RequestID   string `json:"request_id,omitempty"`
	TotalTokens int    `json:"total_tokens,omitempty"`
	Results     []struct {
		Index          int     `json:"index"`
		RelevanceScore float32 `json:"relevance_score"`
	} `json:"results"`
}

func (a *ContentSearchAgent) Search(ctx context.Context, req ContentSearchRequest) (ContentSearchResponse, error) {
	if a.ai == nil {
		return ContentSearchResponse{}, errors.New("AI Client 未注入")
	}
	if a.registry == nil {
		return ContentSearchResponse{}, errors.New("Skill Registry 未注入")
	}
	if a.articleRepo == nil {
		return ContentSearchResponse{}, errors.New("ArticleRepo 未注入")
	}

	req.Query = strings.TrimSpace(req.Query)
	if req.SearchRequestID == "" {
		req.SearchRequestID = strings.TrimSpace(req.RequestID)
	}
	if req.TopK <= 0 {
		req.TopK = req.TopKLegacy
	}
	if req.Query == "" {
		return ContentSearchResponse{}, errors.New("query 不能为空")
	}
	if req.SearchRequestID == "" {
		req.SearchRequestID = "cs_" + randID()
	}
	if req.TopK <= 0 {
		req.TopK = 10
	}
	if req.TopK > 50 {
		req.TopK = 50
	}
	if req.RecallK <= 0 {
		req.RecallK = config.Cfg.Search.FineRecallK
		if req.RecallK <= 0 {
			req.RecallK = maxInt(req.TopK*4, 40)
		}
	}
	if req.RecallK < req.TopK {
		req.RecallK = req.TopK
	}
	if req.RecallK > 100 {
		req.RecallK = 100
	}
	if req.CoarseRecallK <= 0 {
		req.CoarseRecallK = config.Cfg.Search.CoarseRecallK
		if req.CoarseRecallK <= 0 {
			req.CoarseRecallK = maxInt(req.TopK*8, 80)
		}
	}
	if req.MinPassScore <= 0 {
		req.MinPassScore = float32(config.Cfg.Search.MinPassScore)
		if req.MinPassScore <= 0 {
			req.MinPassScore = 0.55
		}
	}

	ctx = zlog.NewTrace(ctx, req.SearchRequestID, "content_search", "content_search_agent", "", "", nil)
	base, _ := zlog.BaseFrom(ctx)

	respOut := ContentSearchResponse{
		TraceID:         base.TraceID,
		SearchRequestID: req.SearchRequestID,
		Status:          "error",
		Items:           []ContentSearchItem{},
	}

	expl := newExplainBuilder(req.Explain)
	expl.Add("invoke", map[string]any{
		"trace_id":          base.TraceID,
		"search_request_id": req.SearchRequestID,
		"topk":              req.TopK,
		"recall_k":          req.RecallK,
		"coarse_recall_k":   req.CoarseRecallK,
		"min_pass_score":    req.MinPassScore,
	})

	zlog.L().Info("invoke_agent",
		zap.String("event_type", "invoke_agent"),
		zap.String("trace_id", base.TraceID),
		zap.String("search_request_id", req.SearchRequestID),
		zap.String("agent", "content_search_agent"),
		zap.String("status", "OK"),
	)

	ctxIntent, spIntent := zlog.StartSpan(ctx, "intent.parse")
	intent, intentLat, err := a.intentParse(ctxIntent, req.Query)
	if err != nil {
		spIntent.End(zlog.StatusError, err, zap.Int64("latency_ms", intentLat))
		expl.Add("intent.parse.error", map[string]any{"error": err.Error(), "latency_ms": intentLat})
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}
	spIntent.End(zlog.StatusOK, nil,
		zap.Int64("latency_ms", intentLat),
		zap.Any("intent", intent),
	)
	respOut.Intent = intent
	expl.Add("intent.parse", map[string]any{
		"label":       intent.Label,
		"confidence":  intent.Confidence,
		"search_text": intent.SearchText,
		"keywords":    intent.Keywords,
		"signals":     intent.Signals,
		"latency_ms":  intentLat,
	})

	semanticQuery := buildSemanticQuery(req.Query, intent)
	ctxEmbed, spEmbed := zlog.StartSpan(ctx, "retrieval.embed_query")
	embedStart := time.Now()
	vec, err := embeddingservice.TextVector(ctxEmbed, semanticQuery)
	embedMs := time.Since(embedStart).Milliseconds()
	if err != nil {
		spEmbed.End(zlog.StatusError, err, zap.Int64("latency_ms", embedMs))
		expl.Add("retrieval.embed_query.error", map[string]any{"error": err.Error(), "latency_ms": embedMs})
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}
	spEmbed.End(zlog.StatusOK, nil,
		zap.Int64("latency_ms", embedMs),
		zap.Int("vector_dim", len(vec)),
	)
	expl.Add("retrieval.embed_query", map[string]any{
		"semantic_query": semanticQuery,
		"vector_dim":     len(vec),
		"latency_ms":     embedMs,
	})

	ranker := searchsvc.NewPrecisionRanker(a.articleRepo, a.registry)

	ctxCoarse, spCoarse := zlog.StartSpan(ctx, "retrieval.coarse_recall")
	coarseStart := time.Now()
	coarseCandidates, err := ranker.RecallCoarseArticleCandidates(ctxCoarse, vec, req.CoarseRecallK)
	coarseMs := time.Since(coarseStart).Milliseconds()
	if err != nil {
		spCoarse.End(zlog.StatusError, err, zap.Int64("latency_ms", coarseMs))
		expl.Add("retrieval.coarse_recall.error", map[string]any{"error": err.Error(), "latency_ms": coarseMs})
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}
	spCoarse.End(zlog.StatusOK, nil,
		zap.Int64("latency_ms", coarseMs),
		zap.Int("candidate_count", len(coarseCandidates)),
	)
	coarseCandidates = searchsvc.BoostCoarseCandidatesByKeywords(coarseCandidates, semanticQuery, intent.Keywords)
	expl.Add("retrieval.coarse_recall", map[string]any{
		"candidate_count":    len(coarseCandidates),
		"top_article_ids":    takeCoarseArticleIDs(coarseCandidates, 8),
		"top_keyword_scores": takeCoarseKeywordScores(coarseCandidates, 8),
		"latency_ms":         coarseMs,
	})

	articleIDs, coarseRankByArticle := searchsvc.PickArticleCandidates(
		coarseCandidates,
		config.Cfg.Search.MaxArticleCandidates,
	)
	if len(articleIDs) == 0 {
		respOut.Status = "ok"
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, nil
	}

	ctxFine, spFine := zlog.StartSpan(ctx, "retrieval.fine_recall")
	fineStart := time.Now()
	fineCandidates, err := ranker.RecallFineCandidatesByArticleIDs(ctxFine, vec, articleIDs, req.RecallK)
	fineMs := time.Since(fineStart).Milliseconds()
	if err != nil {
		spFine.End(zlog.StatusError, err, zap.Int64("latency_ms", fineMs))
		expl.Add("retrieval.fine_recall.error", map[string]any{"error": err.Error(), "latency_ms": fineMs})
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}
	spFine.End(zlog.StatusOK, nil,
		zap.Int64("latency_ms", fineMs),
		zap.Int("candidate_count", len(fineCandidates)),
	)
	expl.Add("retrieval.fine_recall", map[string]any{
		"candidate_count": len(fineCandidates),
		"top_chunk_ids":   takeFineChunkIDs(fineCandidates, 8),
		"top_article_ids": takeFineArticleIDs(fineCandidates, 8),
		"latency_ms":      fineMs,
	})
	if len(fineCandidates) == 0 {
		respOut.Status = "ok"
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, nil
	}

	vectorScoreByChunk := make(map[string]float32, len(fineCandidates))
	fineRankByChunk := make(map[string]int, len(fineCandidates))
	supportByArticle := make(map[string]int, len(articleIDs))
	candidateIDs := make([]string, 0, len(fineCandidates))
	for i, c := range fineCandidates {
		chunkID := strings.TrimSpace(c.ChunkID)
		if chunkID == "" {
			chunkID = strings.TrimSpace(c.ID)
		}
		if chunkID == "" {
			continue
		}
		candidateIDs = append(candidateIDs, chunkID)
		vectorScoreByChunk[chunkID] = c.VectorScore
		fineRankByChunk[chunkID] = i + 1
		if c.ArticleID != "" {
			supportByArticle[c.ArticleID]++
		}
	}

	ctxRerank, spRerank := zlog.StartSpan(ctx, "rerank.dashscope_skill")
	rerankStart := time.Now()
	rerankTopK := minInt(req.RecallK, maxInt(req.TopK*3, 20))
	rerankedHits, skillMeta, err := ranker.RerankCandidates(ctxRerank, semanticQuery, candidateIDs, rerankTopK)
	rerankMs := time.Since(rerankStart).Milliseconds()
	if err != nil {
		spRerank.End(zlog.StatusError, err, zap.Int64("latency_ms", rerankMs))
		expl.Add("rerank.dashscope_skill.error", map[string]any{"error": err.Error(), "latency_ms": rerankMs})
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}
	spRerank.End(zlog.StatusOK, nil,
		zap.Int64("latency_ms", rerankMs),
		zap.Int("hit_count", len(rerankedHits)),
		zap.String("model", skillMeta.Model),
		zap.String("request_id", skillMeta.RequestID),
	)
	expl.Add("rerank.dashscope_skill", map[string]any{
		"model":         skillMeta.Model,
		"request_id":    skillMeta.RequestID,
		"total_tokens":  skillMeta.TotalTokens,
		"hit_count":     len(rerankedHits),
		"top_chunk_ids": takeMatchedChunkIDs(rerankedHits, 8),
		"latency_ms":    rerankMs,
	})

	passedHits := searchsvc.FilterPassedHits(
		rerankedHits,
		coarseRankByArticle,
		fineRankByChunk,
		supportByArticle,
		float32(config.Cfg.Search.MinRerankScore),
		req.MinPassScore,
		float32(config.Cfg.Search.SupportBonus),
	)
	expl.Add("rank.pass_filter", map[string]any{
		"candidate_in":     len(rerankedHits),
		"candidate_out":    len(passedHits),
		"min_rerank_score": config.Cfg.Search.MinRerankScore,
		"min_pass_score":   req.MinPassScore,
	})
	if len(passedHits) == 0 {
		respOut.Status = "ok"
		respOut.Items = []ContentSearchItem{}
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, nil
	}

	items, err := a.buildResponseItems(ctx, passedHits, vectorScoreByChunk, req.TopK)
	if err != nil {
		expl.Add("assemble.response.error", map[string]any{"error": err.Error()})
		respOut.Explanation = expl.Text()
		if req.Explain {
			respOut.ExplainTrace = expl.Trace()
		}
		return respOut, err
	}

	expl.Add("assemble.response", map[string]any{
		"returned_article_count": len(items),
		"article_ids":            takeItemArticleIDs(items, 8),
	})

	respOut.Status = "ok"
	respOut.Items = items
	respOut.Explanation = expl.Text()
	if req.Explain {
		respOut.ExplainTrace = expl.Trace()
	}
	return respOut, nil
}

func (a *ContentSearchAgent) intentParse(ctx context.Context, query string) (ContentSearchIntent, int64, error) {
	sys := "你是内容搜索 Agent 的意图分析器。只做检索意图分析，不回答用户问题，不使用用户记忆。只输出 JSON，不要输出多余文字。输出格式：{\"label\":\"...\",\"confidence\":0.0,\"search_text\":\"...\",\"keywords\":[\"...\"],\"signals\":[\"...\"]}"
	user := "用户问题：" + strings.TrimSpace(query) + "\n" +
		"任务：\n" +
		"1. 判断内容搜索意图 label，例如：tutorial / definition / comparison / list / troubleshooting / case_study / trend / opinion / unknown。\n" +
		"2. 产出适合语义向量检索的 search_text，保留核心实体、约束条件、主题词，去掉寒暄废话。\n" +
		"3. keywords 最多 6 个，signals 最多 6 个。\n" +
		"4. confidence 输出 0~1。\n"

	start := time.Now()
	resp, err := a.ai.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: config.Cfg.Agent.Model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(sys),
			openai.UserMessage(user),
		},
		Temperature: openai.Float(0.0),
	})
	lat := time.Since(start).Milliseconds()
	if err != nil {
		return ContentSearchIntent{}, lat, err
	}

	content := ""
	if len(resp.Choices) > 0 {
		content = resp.Choices[0].Message.Content
	}

	var out ContentSearchIntent
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return ContentSearchIntent{
			Label:      "unknown",
			Confidence: 0.1,
			SearchText: strings.TrimSpace(query),
			Keywords:   extractFallbackKeywords(query, 6),
			Signals:    []string{"PARSE_FALLBACK"},
		}, lat, nil
	}

	out.Label = strings.TrimSpace(out.Label)
	out.SearchText = strings.TrimSpace(out.SearchText)
	if out.SearchText == "" {
		out.SearchText = strings.TrimSpace(query)
	}
	out.Keywords = uniqueNonEmpty(out.Keywords, 6)
	out.Signals = uniqueNonEmpty(out.Signals, 6)
	if out.Confidence <= 0 {
		out.Confidence = 0.5
	}
	if out.Confidence > 1 {
		out.Confidence = 1
	}
	return out, lat, nil
}

func (a *ContentSearchAgent) recallVectorCandidates(ctx context.Context, vec []float32, limit int) ([]contentVectorCandidate, error) {
	cli := infra.Milvus()
	if cli == nil {
		return nil, errors.New("Milvus 客户端未初始化")
	}
	if limit <= 0 {
		limit = 20
	}
	opt := milvusclient.NewSearchOption(
		config.Cfg.Milvus.Collections.Fine,
		limit,
		[]entity.Vector{entity.FloatVector(vec)},
	).WithANNSField("vector").WithOutputFields("article_id", "chunk_id", "h2")

	rs, err := cli.Search(ctx, opt)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, nil
	}

	set := rs[0]
	articleCol := set.GetColumn("article_id")
	chunkCol := set.GetColumn("chunk_id")
	h2Col := set.GetColumn("h2")

	hits := make([]contentVectorCandidate, 0, set.ResultCount)
	for i := 0; i < set.ResultCount; i++ {
		id, _ := set.IDs.GetAsString(i)
		articleID := getColumnString(articleCol, i)
		chunkID := getColumnString(chunkCol, i)
		h2 := getColumnString(h2Col, i)
		if chunkID == "" {
			chunkID = id
		}
		if articleID == "" {
			articleID = articleIDFromChunkID(chunkID)
		}
		score := float32(0)
		if i < len(set.Scores) {
			score = set.Scores[i]
		}
		hits = append(hits, contentVectorCandidate{
			ID:          id,
			ArticleID:   articleID,
			ChunkID:     chunkID,
			H2:          h2,
			VectorScore: score,
		})
	}
	return hits, nil
}

func (a *ContentSearchAgent) rerankCandidates(ctx context.Context, semanticQuery string, candidateIDs []string, topK int) ([]contentRerankHit, dashscopeTextRerankOutput, error) {
	if len(candidateIDs) == 0 {
		return nil, dashscopeTextRerankOutput{Results: []struct {
			Index          int     `json:"index"`
			RelevanceScore float32 `json:"relevance_score"`
		}{}}, nil
	}

	chunks, err := a.articleRepo.GetChunksByIDs(ctx, candidateIDs)
	if err != nil {
		return nil, dashscopeTextRerankOutput{}, err
	}
	chunkByID := make(map[string]storageChunkView, len(chunks))
	for _, c := range chunks {
		chunkByID[c.ChunkID] = storageChunkView{
			ArticleID: c.ArticleID,
			ChunkID:   c.ChunkID,
			H2:        c.H2,
			Content:   c.Content,
		}
	}

	ordered := make([]storageChunkView, 0, len(candidateIDs))
	docs := make([]string, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		c, ok := chunkByID[id]
		if !ok {
			continue
		}
		if strings.TrimSpace(c.Content) == "" {
			continue
		}
		ordered = append(ordered, c)
		docs = append(docs, c.Content)
	}
	if len(ordered) == 0 {
		return nil, dashscopeTextRerankOutput{Results: []struct {
			Index          int     `json:"index"`
			RelevanceScore float32 `json:"relevance_score"`
		}{}}, nil
	}

	args := map[string]any{
		"query":     semanticQuery,
		"documents": docs,
		"topk":      topK,
	}
	if strings.TrimSpace(config.Cfg.Ali.RerankInstruct) != "" {
		args["instruct"] = strings.TrimSpace(config.Cfg.Ali.RerankInstruct)
	}
	b, _ := json.Marshal(args)
	outStr, _, err := a.registry.Invoke(ctx, "dashscope_text_rerank", b)
	if err != nil {
		return nil, dashscopeTextRerankOutput{}, err
	}

	var toolOut dashscopeTextRerankOutput
	if err := json.Unmarshal([]byte(outStr), &toolOut); err != nil {
		return nil, dashscopeTextRerankOutput{}, err
	}

	hits := make([]contentRerankHit, 0, len(toolOut.Results))
	for _, r := range toolOut.Results {
		if r.Index < 0 || r.Index >= len(ordered) {
			continue
		}
		c := ordered[r.Index]
		hits = append(hits, contentRerankHit{
			ID:          c.ChunkID,
			ArticleID:   c.ArticleID,
			ChunkID:     c.ChunkID,
			H2:          c.H2,
			Document:    c.Content,
			RerankScore: r.RelevanceScore,
		})
	}
	if len(hits) == 0 {
		for i, c := range ordered {
			hits = append(hits, contentRerankHit{
				ID:          c.ChunkID,
				ArticleID:   c.ArticleID,
				ChunkID:     c.ChunkID,
				H2:          c.H2,
				Document:    c.Content,
				RerankScore: float32(len(ordered) - i),
			})
		}
	}
	return hits, toolOut, nil
}

type storageChunkView struct {
	ArticleID string
	ChunkID   string
	H2        string
	Content   string
}

func (a *ContentSearchAgent) buildResponseItems(ctx context.Context, hits []searchsvc.RerankHit, vectorScoreByChunk map[string]float32, topK int) ([]ContentSearchItem, error) {
	if len(hits) == 0 || topK <= 0 {
		return []ContentSearchItem{}, nil
	}

	orderedArticleIDs := make([]string, 0, topK)
	bestHitByArticle := make(map[string]searchsvc.RerankHit, len(hits))
	for _, hit := range hits {
		if strings.TrimSpace(hit.ArticleID) == "" {
			continue
		}
		if _, exists := bestHitByArticle[hit.ArticleID]; exists {
			continue
		}
		bestHitByArticle[hit.ArticleID] = hit
		orderedArticleIDs = append(orderedArticleIDs, hit.ArticleID)
		if len(orderedArticleIDs) >= topK {
			break
		}
	}
	if len(orderedArticleIDs) == 0 {
		return []ContentSearchItem{}, nil
	}

	metas, err := a.articleRepo.GetArticlesByIDs(ctx, orderedArticleIDs)
	if err != nil {
		return nil, err
	}
	metaByID := make(map[string]storage.ArticleMeta, len(metas))
	for _, m := range metas {
		metaByID[m.ArticleID] = m
	}

	items := make([]ContentSearchItem, 0, len(orderedArticleIDs))
	for _, articleID := range orderedArticleIDs {
		hit := bestHitByArticle[articleID]
		meta := metaByID[articleID]
		vectorScore := vectorScoreByChunk[hit.ChunkID]
		if vectorScore == 0 {
			vectorScore = vectorScoreByChunk[hit.ID]
		}
		items = append(items, ContentSearchItem{
			ArticleID:    articleID,
			Title:        meta.Title,
			Cover:        meta.Cover,
			TypeTags:     meta.TypeTags,
			Tags:         meta.Tags,
			H2:           hit.H2,
			Snippet:      compactSnippet(hit.Document, 220),
			ChunkID:      hit.ChunkID,
			ArticleScore: meta.Score,
			VectorScore:  vectorScore,
			RerankScore:  hit.RerankScore,
			MatchScore:   hit.MatchScore,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].MatchScore == items[j].MatchScore {
			return items[i].RerankScore > items[j].RerankScore
		}
		return items[i].MatchScore > items[j].MatchScore
	})
	return items, nil
}

func buildSemanticQuery(raw string, intent ContentSearchIntent) string {
	base := strings.TrimSpace(intent.SearchText)
	if base == "" {
		base = strings.TrimSpace(raw)
	}
	if len(intent.Keywords) == 0 {
		return base
	}
	return strings.TrimSpace(base + "\n关键词：" + strings.Join(uniqueNonEmpty(intent.Keywords, 6), " "))
}

func extractFallbackKeywords(query string, limit int) []string {
	parts := strings.FieldsFunc(query, func(r rune) bool {
		switch r {
		case ' ', '\n', '\t', ',', '，', '。', '；', ';', '：', ':', '？', '?', '！', '!', '、', '/', '|':
			return true
		default:
			return false
		}
	})
	return uniqueNonEmpty(parts, limit)
}

func uniqueNonEmpty(in []string, limit int) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		key := strings.ToLower(s)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func getColumnString(col any, idx int) string {
	type getter interface {
		Get(int) (any, error)
	}
	g, ok := col.(getter)
	if !ok || g == nil {
		return ""
	}
	v, err := g.Get(idx)
	if err != nil || v == nil {
		return ""
	}
	switch vv := v.(type) {
	case string:
		return vv
	case []byte:
		return string(vv)
	default:
		return fmt.Sprint(v)
	}
}

func articleIDFromChunkID(chunkID string) string {
	if idx := strings.Index(chunkID, "#"); idx > 0 {
		return chunkID[:idx]
	}
	return chunkID
}

func compactSnippet(s string, limit int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if limit > 0 {
		runes := []rune(s)
		if len(runes) > limit {
			return string(runes[:limit]) + "..."
		}
	}
	return s
}

func takeCandidateChunkIDs(in []contentVectorCandidate, n int) []string {
	out := make([]string, 0, minInt(n, len(in)))
	for i := 0; i < len(in) && i < n; i++ {
		if in[i].ChunkID != "" {
			out = append(out, in[i].ChunkID)
		}
	}
	return out
}

func takeCandidateArticleIDs(in []contentVectorCandidate, n int) []string {
	out := make([]string, 0, minInt(n, len(in)))
	seen := map[string]struct{}{}
	for i := 0; i < len(in) && len(out) < n; i++ {
		if in[i].ArticleID == "" {
			continue
		}
		if _, ok := seen[in[i].ArticleID]; ok {
			continue
		}
		seen[in[i].ArticleID] = struct{}{}
		out = append(out, in[i].ArticleID)
	}
	return out
}

func takeMatchedChunkIDs(in []searchsvc.RerankHit, n int) []string {
	out := make([]string, 0, minInt(n, len(in)))
	for i := 0; i < len(in) && i < n; i++ {
		if in[i].ChunkID != "" {
			out = append(out, in[i].ChunkID)
		}
	}
	return out
}

func takeCoarseArticleIDs(in []searchsvc.CoarseArticleCandidate, n int) []string {
	out := make([]string, 0, minInt(n, len(in)))
	for i := 0; i < len(in) && i < n; i++ {
		if in[i].ArticleID != "" {
			out = append(out, in[i].ArticleID)
		}
	}
	return out
}

func takeCoarseKeywordScores(in []searchsvc.CoarseArticleCandidate, n int) []float32 {
	out := make([]float32, 0, minInt(n, len(in)))
	for i := 0; i < len(in) && i < n; i++ {
		out = append(out, in[i].KeywordScore)
	}
	return out
}

func takeFineChunkIDs(in []searchsvc.VectorCandidate, n int) []string {
	out := make([]string, 0, minInt(n, len(in)))
	for i := 0; i < len(in) && i < n; i++ {
		if in[i].ChunkID != "" {
			out = append(out, in[i].ChunkID)
		}
	}
	return out
}

func takeFineArticleIDs(in []searchsvc.VectorCandidate, n int) []string {
	out := make([]string, 0, minInt(n, len(in)))
	seen := map[string]struct{}{}
	for i := 0; i < len(in) && len(out) < n; i++ {
		if in[i].ArticleID == "" {
			continue
		}
		if _, ok := seen[in[i].ArticleID]; ok {
			continue
		}
		seen[in[i].ArticleID] = struct{}{}
		out = append(out, in[i].ArticleID)
	}
	return out
}

func takeItemArticleIDs(in []ContentSearchItem, n int) []string {
	out := make([]string, 0, minInt(n, len(in)))
	for i := 0; i < len(in) && i < n; i++ {
		if in[i].ArticleID != "" {
			out = append(out, in[i].ArticleID)
		}
	}
	return out
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
