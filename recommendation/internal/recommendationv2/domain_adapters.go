package recommendationv2

import (
	"context"
	"fmt"
	"sort"

	"sea/internal/domain"
)

type DomainRecallProvider struct {
	recaller domain.Recaller
}

func NewDomainRecallProvider(recaller domain.Recaller) *DomainRecallProvider {
	return &DomainRecallProvider{recaller: recaller}
}

func WithDomainRecaller(recaller domain.Recaller) Option {
	return func(rt *RecommendationRuntime) {
		if recaller != nil {
			rt.recall = NewDomainRecallProvider(recaller)
		}
	}
}

func (p *DomainRecallProvider) Recall(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error) {
	if p == nil || p.recaller == nil {
		return nil, rctx.Cost, fmt.Errorf("recommendation v2: domain recaller is not configured")
	}
	req := domain.RecallRequest{
		UserKey:   domainUserKey(rctx.Request),
		Profile:   domainProfile(rctx.Profile),
		Channel:   rctx.Request.Channel,
		TopK:      rctx.Config.Recall.TopK,
		TagFilter: domainTagFilter(rctx.Request),
		Intent:    domainIntent(rctx.Request, rctx.Path),
	}
	result, err := p.recaller.Recall(ctx, req)
	if err != nil {
		return nil, rctx.Cost, err
	}
	items := recommendItemsFromCandidates(result.Candidates, result.Source)
	cost := rctx.Cost
	cost.ToolCalls++
	cost.VectorQueries += countSource(result.Candidates, "content", "vector", "milvus")
	cost.GraphQueries += countSource(result.Candidates, "graph", "neo4j")
	return items, cost, nil
}

type DomainRankProvider struct {
	ranker domain.Ranker
}

func NewDomainRankProvider(ranker domain.Ranker) *DomainRankProvider {
	return &DomainRankProvider{ranker: ranker}
}

func WithDomainRanker(ranker domain.Ranker) Option {
	return func(rt *RecommendationRuntime) {
		if ranker != nil {
			rt.rank = NewDomainRankProvider(ranker)
		}
	}
}

func (p *DomainRankProvider) Rank(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error) {
	if p == nil || p.ranker == nil {
		return nil, rctx.Cost, fmt.Errorf("recommendation v2: domain ranker is not configured")
	}
	result, err := p.ranker.Rank(ctx, domain.RankContext{
		Profile:    domainProfile(rctx.Profile),
		Channel:    rctx.Request.Channel,
		Intent:     domainIntent(rctx.Request, rctx.Path),
		Candidates: candidatesFromRecommendItems(rctx.Items),
	})
	if err != nil {
		return nil, rctx.Cost, err
	}
	items := recommendItemsFromCandidates(result.Candidates, "")
	for i := range items {
		items[i].Rank = i + 1
	}
	cost := rctx.Cost
	cost.ToolCalls++
	return items, cost, nil
}

type DomainRerankProvider struct {
	reranker domain.Reranker
}

func NewDomainRerankProvider(reranker domain.Reranker) *DomainRerankProvider {
	return &DomainRerankProvider{reranker: reranker}
}

func WithDomainReranker(reranker domain.Reranker) Option {
	return func(rt *RecommendationRuntime) {
		if reranker != nil {
			rt.rerank = NewDomainRerankProvider(reranker)
		}
	}
}

func (p *DomainRerankProvider) Rerank(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error) {
	if p == nil || p.reranker == nil {
		return nil, rctx.Cost, fmt.Errorf("recommendation v2: domain reranker is not configured")
	}
	result, err := p.reranker.Rerank(ctx, domain.RerankRequest{
		UserKey:    domainUserKey(rctx.Request),
		Candidates: candidatesFromRecommendItems(rctx.Items),
		Query:      rctx.Request.Query,
		Model:      rctx.Config.Rerank.Model,
		TopK:       rctx.Config.Rerank.TopN,
	})
	if err != nil {
		return nil, rctx.Cost, err
	}
	items := recommendItemsFromCandidates(result.Candidates, "")
	for i := range items {
		items[i].Rank = i + 1
		items[i].Reason = "reranked by " + valueOrFallback(result.ModelUsed, rctx.Config.Rerank.Model)
		items[i].Features = mergeAnyMap(items[i].Features, map[string]any{"rerank_model": valueOrFallback(result.ModelUsed, rctx.Config.Rerank.Model)})
	}
	cost := rctx.Cost
	cost.ToolCalls++
	cost.RerankCost += rctx.Config.Rerank.Budget
	cost.EstimatedAmount += rctx.Config.Rerank.Budget
	return items, cost, nil
}

type DomainQualityProvider struct {
	judger    domain.QualityJudger
	threshold float64
}

func NewDomainQualityProvider(judger domain.QualityJudger, threshold float64) *DomainQualityProvider {
	return &DomainQualityProvider{judger: judger, threshold: threshold}
}

func WithDomainQualityJudger(judger domain.QualityJudger, threshold float64) Option {
	return func(rt *RecommendationRuntime) {
		if judger != nil {
			rt.quality = NewDomainQualityProvider(judger, threshold)
		}
	}
}

func (p *DomainQualityProvider) Score(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, *FallbackReport, error) {
	if p == nil || p.judger == nil {
		return nil, nil, fmt.Errorf("recommendation v2: domain quality judger is not configured")
	}
	threshold := p.threshold
	if threshold <= 0 {
		threshold = 0.5
	}
	items := make([]RecommendItem, 0, len(rctx.Items))
	for _, item := range rctx.Items {
		q, err := p.judger.Judge(ctx, domain.QualityRequest{
			ArticleID: item.ArticleID,
			Content:   stringFromAny(item.Features["content"]),
		})
		if err != nil {
			return nil, nil, err
		}
		if !q.IsPass(threshold) {
			continue
		}
		item.Features = mergeAnyMap(item.Features, map[string]any{
			"quality_score": q.Overall,
			"quality_grade": q.ToGrade(),
		})
		items = append(items, item)
	}
	if len(items) == 0 {
		return items, &FallbackReport{Triggered: true, Reason: "all candidates filtered by quality threshold", Source: "quality"}, nil
	}
	return items, nil, nil
}

func domainUserKey(req RecommendRequest) domain.UserKey {
	userID := req.User.UserID
	if userID == "" {
		userID = req.User.AnonymousID
	}
	return domain.UserKey{UserID: userID, Channel: req.Channel}
}

func domainProfile(snapshot ProfileSnapshot) *domain.UserProfile {
	profile := &domain.UserProfile{
		Key:       domain.UserKey{UserID: valueOrFallback(snapshot.User.UserID, snapshot.User.AnonymousID), Channel: snapshot.User.Channel},
		UpdatedAt: snapshot.Profile.UpdatedAt,
		Static: &domain.StaticProfile{
			Tier:      stringFromAny(snapshot.Profile.Static["tier"]),
			Tags:      append([]string(nil), snapshot.Profile.BusinessTags...),
			Interests: stringSliceFromAny(snapshot.Profile.Static["interests"]),
			Purpose:   stringFromAny(snapshot.Profile.Static["purpose"]),
		},
		Dynamic: &domain.DynamicProfile{
			Topics:   stringSliceFromAny(snapshot.Profile.Dynamic["topics"]),
			Activity: floatFromAny(snapshot.Profile.Dynamic["activity"]),
		},
		Behavior: &domain.BehaviorProfile{
			RecentClicks:      stringSliceFromAny(snapshot.Profile.Behavior["recent_clicks"]),
			RecentLikes:       stringSliceFromAny(snapshot.Profile.Behavior["recent_likes"]),
			RecentFavorites:   stringSliceFromAny(snapshot.Profile.Behavior["recent_favorites"]),
			RecentCompletions: stringSliceFromAny(snapshot.Profile.Behavior["recent_completions"]),
			RecentSearches:    stringSliceFromAny(snapshot.Profile.Behavior["recent_searches"]),
		},
	}
	profile.Static.Tags = append(profile.Static.Tags, snapshot.User.Tags...)
	return profile
}

func domainTagFilter(req RecommendRequest) domain.TagFilter {
	if req.Context == nil {
		return domain.TagFilter{}
	}
	return domain.TagFilter{
		Include: stringSliceFromAny(req.Context["include_tags"]),
		Exclude: stringSliceFromAny(req.Context["exclude_tags"]),
	}
}

func domainIntent(req RecommendRequest, path string) *domain.Intent {
	signals := map[string]string{
		"scenario": req.Scenario,
		"channel":  req.Channel,
		"path":     path,
	}
	if req.Query != "" {
		signals["query"] = req.Query
	}
	complexity := "medium"
	switch path {
	case PathFast:
		complexity = "simple"
	case PathSlow:
		complexity = "complex"
	}
	return &domain.Intent{
		Label:      valueOrFallback(req.Scenario, "recommend"),
		Confidence: 1,
		Signals:    signals,
		Complexity: complexity,
	}
}

func recommendItemsFromCandidates(candidates []domain.Candidate, fallbackSource string) []RecommendItem {
	items := make([]RecommendItem, 0, len(candidates))
	for i, candidate := range candidates {
		source := valueOrFallback(candidate.Source, fallbackSource)
		item := RecommendItem{
			ID:        valueOrFallback(candidate.ArticleID, fmt.Sprintf("candidate_%02d", i+1)),
			ArticleID: candidate.ArticleID,
			Score:     candidate.Score,
			Source:    source,
			Rank:      i + 1,
			Features:  mergeCandidateFeatures(candidate),
		}
		items = append(items, item)
	}
	return items
}

func candidatesFromRecommendItems(items []RecommendItem) []domain.Candidate {
	candidates := make([]domain.Candidate, 0, len(items))
	for _, item := range items {
		candidates = append(candidates, domain.Candidate{
			ArticleID: item.ArticleID,
			Score:     item.Score,
			Source:    item.Source,
			Scores:    floatScoresFromAnyMap(item.Features),
			Extra:     cloneAnyMap(item.Features),
		})
	}
	return candidates
}

func mergeCandidateFeatures(candidate domain.Candidate) map[string]any {
	features := cloneAnyMap(candidate.Extra)
	for k, v := range candidate.Scores {
		features[k] = v
	}
	if candidate.Source != "" {
		features["recall_source"] = candidate.Source
	}
	return features
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func floatScoresFromAnyMap(in map[string]any) map[string]float64 {
	out := make(map[string]float64)
	for k, v := range in {
		if f := floatFromAny(v); f != 0 {
			out[k] = f
		}
	}
	return out
}

func countSource(candidates []domain.Candidate, needles ...string) int {
	count := 0
	for _, candidate := range candidates {
		source := candidate.Source
		for _, needle := range needles {
			if source == needle {
				count++
				break
			}
		}
	}
	return count
}

func stringSliceFromAny(v any) []string {
	switch typed := v.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s := stringFromAny(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	default:
		return nil
	}
}

func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func floatFromAny(v any) float64 {
	switch typed := v.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

func valueOrFallback(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func sortRecommendItems(items []RecommendItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Score > items[j].Score
	})
	for i := range items {
		items[i].Rank = i + 1
	}
}
