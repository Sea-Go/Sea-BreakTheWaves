package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"sea/config"
	"sea/infra"
	"sea/storage"

	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

type CoarseArticleCandidate struct {
	ArticleID    string
	CoarseScore  float32
	Tags         string
	KeywordScore float32
}

type VectorCandidate struct {
	ID          string
	ArticleID   string
	ChunkID     string
	H2          string
	VectorScore float32
}

type RerankHit struct {
	ID           string
	ArticleID    string
	ChunkID      string
	H2           string
	Document     string
	RerankScore  float32
	MatchScore   float32
	CoarseScore  float32
	FineScore    float32
	SupportScore float32
	CoarseRank   int
	FineRank     int
	SupportCount int
}

type DashscopeTextRerankOutput struct {
	Model       string `json:"model"`
	RequestID   string `json:"request_id,omitempty"`
	TotalTokens int    `json:"total_tokens,omitempty"`
	Results     []struct {
		Index          int     `json:"index"`
		RelevanceScore float32 `json:"relevance_score"`
	} `json:"results"`
}

type QueryMatchOptions struct {
	CoarseRecallK        int
	FineRecallK          int
	MaxArticleCandidates int
	MinRerankScore       float32
	MinPassScore         float32
	SupportBonus         float32
	RerankTopK           int
	QueryText            string
	QueryKeywords        []string
}

type QueryMatchResult struct {
	CoarseCandidates    []CoarseArticleCandidate
	ArticleIDs          []string
	FineCandidates      []VectorCandidate
	PassedHits          []RerankHit
	SkillMeta           DashscopeTextRerankOutput
	VectorScoreByChunk  map[string]float32
	CoarseRankByArticle map[string]int
	FineRankByChunk     map[string]int
	SupportByArticle    map[string]int
}

type PrecisionRanker struct {
	articleRepo *storage.ArticleRepo
	reranker    RerankInvoker
}

type RerankInvoker interface {
	Invoke(ctx context.Context, toolName string, argsRaw json.RawMessage) (string, any, error)
}

func NewPrecisionRanker(articleRepo *storage.ArticleRepo, reranker RerankInvoker) *PrecisionRanker {
	return &PrecisionRanker{
		articleRepo: articleRepo,
		reranker:    reranker,
	}
}

func (r *PrecisionRanker) MatchQuery(ctx context.Context, semanticQuery string, vec []float32, opt QueryMatchOptions) (QueryMatchResult, error) {
	if r.articleRepo == nil {
		return QueryMatchResult{}, errors.New("ArticleRepo 未注入")
	}
	if r.reranker == nil {
		return QueryMatchResult{}, errors.New("Skill Registry 未注入")
	}

	coarseCandidates, err := r.RecallCoarseArticleCandidates(ctx, vec, opt.CoarseRecallK)
	if err != nil {
		return QueryMatchResult{}, err
	}
	coarseCandidates = BoostCoarseCandidatesByKeywords(coarseCandidates, opt.QueryText, opt.QueryKeywords)

	articleIDs, coarseRankByArticle := PickArticleCandidates(coarseCandidates, opt.MaxArticleCandidates)
	result := QueryMatchResult{
		CoarseCandidates:    coarseCandidates,
		ArticleIDs:          articleIDs,
		CoarseRankByArticle: coarseRankByArticle,
		VectorScoreByChunk:  map[string]float32{},
		FineRankByChunk:     map[string]int{},
		SupportByArticle:    map[string]int{},
	}
	if len(articleIDs) == 0 {
		return result, nil
	}

	fineCandidates, err := r.RecallFineCandidatesByArticleIDs(ctx, vec, articleIDs, opt.FineRecallK)
	if err != nil {
		return result, err
	}
	result.FineCandidates = fineCandidates
	if len(fineCandidates) == 0 {
		return result, nil
	}

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
		result.VectorScoreByChunk[chunkID] = c.VectorScore
		result.FineRankByChunk[chunkID] = i + 1
		if c.ArticleID != "" {
			result.SupportByArticle[c.ArticleID]++
		}
	}
	if len(candidateIDs) == 0 {
		return result, nil
	}

	rerankTopK := opt.RerankTopK
	if rerankTopK <= 0 {
		rerankTopK = minInt(opt.FineRecallK, maxInt(20, 12))
	}
	rerankedHits, skillMeta, err := r.RerankCandidates(ctx, semanticQuery, candidateIDs, rerankTopK)
	if err != nil {
		return result, err
	}
	result.SkillMeta = skillMeta
	if len(rerankedHits) == 0 {
		return result, nil
	}

	result.PassedHits = FilterPassedHits(
		rerankedHits,
		result.CoarseRankByArticle,
		result.FineRankByChunk,
		result.SupportByArticle,
		opt.MinRerankScore,
		opt.MinPassScore,
		opt.SupportBonus,
	)
	return result, nil
}

func (r *PrecisionRanker) RecallCoarseArticleCandidates(ctx context.Context, vec []float32, limit int) ([]CoarseArticleCandidate, error) {
	cli := infra.Milvus()
	if cli == nil {
		return nil, errors.New("Milvus 客户端未初始化")
	}
	if limit <= 0 {
		limit = 80
	}

	opt := milvusclient.NewSearchOption(
		config.Cfg.Milvus.Collections.Coarse,
		limit,
		[]entity.Vector{entity.FloatVector(vec)},
	).WithANNSField("vector").WithOutputFields("article_id", "tags")

	rs, err := cli.Search(ctx, opt)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, nil
	}

	set := rs[0]
	articleCol := set.GetColumn("article_id")
	tagsCol := set.GetColumn("tags")

	out := make([]CoarseArticleCandidate, 0, set.ResultCount)
	seen := make(map[string]struct{}, set.ResultCount)
	for i := 0; i < set.ResultCount; i++ {
		articleID := getColumnString(articleCol, i)
		if articleID == "" {
			articleID, _ = set.IDs.GetAsString(i)
		}
		articleID = strings.TrimSpace(articleID)
		if articleID == "" {
			continue
		}
		if _, ok := seen[articleID]; ok {
			continue
		}
		seen[articleID] = struct{}{}

		score := float32(0)
		if i < len(set.Scores) {
			score = set.Scores[i]
		}
		out = append(out, CoarseArticleCandidate{
			ArticleID:   articleID,
			CoarseScore: score,
			Tags:        getColumnString(tagsCol, i),
		})
	}
	return out, nil
}

func (r *PrecisionRanker) RecallFineCandidatesByArticleIDs(ctx context.Context, vec []float32, articleIDs []string, limit int) ([]VectorCandidate, error) {
	if len(articleIDs) == 0 {
		return nil, nil
	}

	cli := infra.Milvus()
	if cli == nil {
		return nil, errors.New("Milvus 客户端未初始化")
	}
	if limit <= 0 {
		limit = 40
	}

	opt := milvusclient.NewSearchOption(
		config.Cfg.Milvus.Collections.Fine,
		limit,
		[]entity.Vector{entity.FloatVector(vec)},
	).WithANNSField("vector").WithOutputFields("article_id", "chunk_id", "h2")

	if filter := buildVarCharInExpr("article_id", articleIDs); filter != "" {
		opt = opt.WithFilter(filter)
	}

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

	hits := make([]VectorCandidate, 0, set.ResultCount)
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
		hits = append(hits, VectorCandidate{
			ID:          id,
			ArticleID:   articleID,
			ChunkID:     chunkID,
			H2:          h2,
			VectorScore: score,
		})
	}
	return hits, nil
}

func (r *PrecisionRanker) RerankCandidates(ctx context.Context, semanticQuery string, candidateIDs []string, topK int) ([]RerankHit, DashscopeTextRerankOutput, error) {
	if len(candidateIDs) == 0 {
		return nil, DashscopeTextRerankOutput{Results: []struct {
			Index          int     `json:"index"`
			RelevanceScore float32 `json:"relevance_score"`
		}{}}, nil
	}

	chunks, err := r.articleRepo.GetChunksByIDs(ctx, candidateIDs)
	if err != nil {
		return nil, DashscopeTextRerankOutput{}, err
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
		return nil, DashscopeTextRerankOutput{Results: []struct {
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
	outStr, _, err := r.reranker.Invoke(ctx, "dashscope_text_rerank", b)
	if err != nil {
		return nil, DashscopeTextRerankOutput{}, err
	}

	var toolOut DashscopeTextRerankOutput
	if err := json.Unmarshal([]byte(outStr), &toolOut); err != nil {
		return nil, DashscopeTextRerankOutput{}, err
	}

	hits := make([]RerankHit, 0, len(toolOut.Results))
	for _, rr := range toolOut.Results {
		if rr.Index < 0 || rr.Index >= len(ordered) {
			continue
		}
		c := ordered[rr.Index]
		hits = append(hits, RerankHit{
			ID:          c.ChunkID,
			ArticleID:   c.ArticleID,
			ChunkID:     c.ChunkID,
			H2:          c.H2,
			Document:    c.Content,
			RerankScore: rr.RelevanceScore,
		})
	}
	if len(hits) == 0 {
		for i, c := range ordered {
			hits = append(hits, RerankHit{
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

func PickArticleCandidates(in []CoarseArticleCandidate, limit int) ([]string, map[string]int) {
	if len(in) == 0 {
		return nil, map[string]int{}
	}
	if limit <= 0 || limit > len(in) {
		limit = len(in)
	}

	ids := make([]string, 0, limit)
	rankByArticle := make(map[string]int, limit)
	for _, c := range in {
		articleID := strings.TrimSpace(c.ArticleID)
		if articleID == "" {
			continue
		}
		if _, ok := rankByArticle[articleID]; ok {
			continue
		}
		rankByArticle[articleID] = len(ids) + 1
		ids = append(ids, articleID)
		if len(ids) >= limit {
			break
		}
	}
	return ids, rankByArticle
}

func BoostCoarseCandidatesByKeywords(in []CoarseArticleCandidate, queryText string, queryKeywords []string) []CoarseArticleCandidate {
	if len(in) == 0 {
		return nil
	}

	signals := buildQueryKeywordSignals(queryText, queryKeywords)
	out := append([]CoarseArticleCandidate(nil), in...)
	for idx := range out {
		out[idx].KeywordScore = keywordSignalScore(signals, out[idx].Tags)
	}
	if len(signals) == 0 {
		return out
	}

	sort.SliceStable(out, func(i, j int) bool {
		leftScore := out[i].CoarseScore + out[i].KeywordScore
		rightScore := out[j].CoarseScore + out[j].KeywordScore
		if leftScore == rightScore {
			if out[i].KeywordScore == out[j].KeywordScore {
				return out[i].CoarseScore > out[j].CoarseScore
			}
			return out[i].KeywordScore > out[j].KeywordScore
		}
		return leftScore > rightScore
	})
	return out
}

func FilterPassedHits(
	hits []RerankHit,
	coarseRankByArticle map[string]int,
	fineRankByChunk map[string]int,
	supportByArticle map[string]int,
	minRerank float32,
	minPass float32,
	supportBonus float32,
) []RerankHit {
	out := make([]RerankHit, 0, len(hits))
	totalCoarse := len(coarseRankByArticle)
	totalFine := len(fineRankByChunk)

	for _, h := range hits {
		coarseRank, ok1 := coarseRankByArticle[h.ArticleID]
		fineRank, ok2 := fineRankByChunk[h.ChunkID]
		if !ok1 || !ok2 {
			continue
		}

		coarseScore := 0.25 * rankScore(coarseRank, totalCoarse)
		fineScore := 0.35 * rankScore(fineRank, totalFine)
		rerankScore := 0.40 * clamp01(h.RerankScore)
		supportScore := minFloat32(0.10, float32(maxInt(supportByArticle[h.ArticleID]-1, 0))*supportBonus)
		passScore := coarseScore + fineScore + rerankScore + supportScore

		if h.RerankScore < minRerank {
			continue
		}
		if passScore < minPass {
			continue
		}

		h.CoarseRank = coarseRank
		h.FineRank = fineRank
		h.SupportCount = supportByArticle[h.ArticleID]
		h.CoarseScore = coarseScore
		h.FineScore = fineScore
		h.SupportScore = supportScore
		h.MatchScore = passScore
		out = append(out, h)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MatchScore == out[j].MatchScore {
			if out[i].RerankScore == out[j].RerankScore {
				return out[i].SupportCount > out[j].SupportCount
			}
			return out[i].RerankScore > out[j].RerankScore
		}
		return out[i].MatchScore > out[j].MatchScore
	})
	return out
}

func buildVarCharInExpr(field string, ids []string) string {
	vals := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		id = strings.ReplaceAll(id, `\`, `\\`)
		id = strings.ReplaceAll(id, `"`, `\"`)
		vals = append(vals, fmt.Sprintf(`"%s"`, id))
	}
	if len(vals) == 0 {
		return ""
	}
	return fmt.Sprintf("%s in [%s]", field, strings.Join(vals, ","))
}

type storageChunkView struct {
	ArticleID string
	ChunkID   string
	H2        string
	Content   string
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

func buildQueryKeywordSignals(queryText string, queryKeywords []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(queryKeywords)+8)
	add := func(s string) {
		s = normalizeSignal(s)
		if s == "" {
			return
		}
		key := strings.ToLower(s)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}

	add(queryText)
	for _, item := range queryKeywords {
		add(item)
	}
	for _, piece := range splitSignals(queryText) {
		add(piece)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return len([]rune(out[i])) > len([]rune(out[j]))
	})
	return out
}

func keywordSignalScore(querySignals []string, tags string) float32 {
	if len(querySignals) == 0 {
		return 0
	}
	tagSignals := splitSignals(tags)
	if len(tagSignals) == 0 {
		return 0
	}

	queryText := strings.ToLower(strings.TrimSpace(querySignals[0]))
	matched := 0
	seen := map[string]struct{}{}
	for _, tagSignal := range tagSignals {
		tagSignal = normalizeSignal(tagSignal)
		if tagSignal == "" {
			continue
		}
		tagKey := strings.ToLower(tagSignal)
		if _, ok := seen[tagKey]; ok {
			continue
		}
		if signalMatched(tagKey, queryText, querySignals) {
			seen[tagKey] = struct{}{}
			matched++
			if matched >= 5 {
				return 1.0
			}
		}
	}
	return float32(matched) * 0.2
}

func signalMatched(tagSignal string, queryText string, querySignals []string) bool {
	if tagSignal == "" {
		return false
	}
	if queryText != "" && (strings.Contains(queryText, tagSignal) || strings.Contains(tagSignal, queryText)) {
		return true
	}
	for _, signal := range querySignals {
		signal = strings.ToLower(normalizeSignal(signal))
		if signal == "" {
			continue
		}
		if strings.Contains(signal, tagSignal) || strings.Contains(tagSignal, signal) {
			return true
		}
	}
	return false
}

func splitSignals(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ',', '，', '。', '、', '/', '\\', '|', ':', '：', ';', '；', '(', ')', '（', '）', '[', ']', '【', '】', '<', '>', '《', '》', '"', '\'', '“', '”', '‘', '’', '-', '_', '+', '=', '*', '&', '·', '!', '！', '?', '？':
			return true
		default:
			return false
		}
	})
	if len(parts) == 0 {
		return []string{s}
	}

	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		part = normalizeSignal(part)
		if part == "" {
			continue
		}
		key := strings.ToLower(part)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, part)
	}
	return out
}

func normalizeSignal(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "#,.，。!！?？:：;；/\\|()（）[]【】<>《》\"'“”‘’`~")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len([]rune(s)) < 2 {
		return ""
	}
	return s
}

func rankScore(rank, total int) float32 {
	if rank <= 0 || total <= 0 {
		return 0
	}
	if total == 1 {
		return 1
	}
	return 1 - float32(rank-1)/float32(total-1)
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func minFloat32(a, b float32) float32 {
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
