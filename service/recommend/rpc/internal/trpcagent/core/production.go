package recommendationv2

import (
	"context"
	"database/sql"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/model"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/model/rank"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/model/recall"
	eventpub "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/trpcagent/event"
)

const (
	productionDefaultTopK           = 50
	productionDefaultRecallPoolSize = 4
	productionDefaultQualityScore   = 0.72
	productionQualityThreshold      = 0.5
)

// WithProductionProviders wires the v2 recommendation runtime to existing Sea
// storage and domain implementations. This is the production-oriented path used
// by main.go; tests may still inject narrower providers directly.
func WithProductionProviders(articleRepo *storage.ArticleRepo, poolRepo *storage.PoolRepo) Option {
	return func(rt *RecommendationRuntime) {
		if rt == nil {
			return
		}

		recallers := make([]domain.Recaller, 0, 2)
		if articleRepo != nil {
			recallers = append(recallers, recall.NewRuleRecaller(newArticleRecallRepository(articleRepo), productionDefaultTopK))
		}
		if poolRepo != nil {
			recallers = append(recallers, recall.NewChannelRecaller(newChannelPoolRepository(poolRepo), productionDefaultTopK))
		}
		switch len(recallers) {
		case 0:
			// Keep the in-memory fallback only when no production dependency is
			// available. This keeps local tests usable without hiding startup wiring.
		case 1:
			rt.recall = NewDomainRecallProvider(recallers[0])
		default:
			hybrid, err := recall.NewHybridRecaller(recallers, productionDefaultRecallPoolSize, productionDefaultTopK)
			if err != nil {
				panic(err)
			}
			rt.recall = NewDomainRecallProvider(hybrid)
		}

		rt.rank = NewDomainRankProvider(rank.NewWeightedRanker(map[string]float64{
			"recall_score": 0.45,
			"score":        0.30,
			"freshness":    0.15,
			"quality":      0.10,
		}))
		rt.rerank = productionRerankProvider{}
		rt.quality = NewDomainQualityProvider(staticQualityJudger{}, productionQualityThreshold)
	}
}

type articleRecallRepository struct {
	articleRepo *storage.ArticleRepo
}

func newArticleRecallRepository(articleRepo *storage.ArticleRepo) *articleRecallRepository {
	return &articleRecallRepository{articleRepo: articleRepo}
}

func (r *articleRecallRepository) ListHotArticles(ctx context.Context, topK int) ([]domain.Candidate, error) {
	return r.listFallback(ctx, topK, "hot")
}

func (r *articleRecallRepository) ListLatestArticles(ctx context.Context, topK int) ([]domain.Candidate, error) {
	if r == nil || r.articleRepo == nil {
		return nil, sql.ErrConnDone
	}
	ids, err := r.articleRepo.ListLatestArticleIDs(ctx, normalizeTopK(topK), nil)
	if err != nil {
		return nil, err
	}
	return r.candidatesFromIDs(ctx, ids, "latest")
}

func (r *articleRecallRepository) ListEditorPicks(ctx context.Context, topK int) ([]domain.Candidate, error) {
	return r.listFallback(ctx, topK, "editor")
}

func (r *articleRecallRepository) listFallback(ctx context.Context, topK int, source string) ([]domain.Candidate, error) {
	if r == nil || r.articleRepo == nil {
		return nil, sql.ErrConnDone
	}
	ids, err := r.articleRepo.ListFallbackArticleIDs(ctx, normalizeTopK(topK), nil)
	if err != nil {
		return nil, err
	}
	return r.candidatesFromIDs(ctx, ids, source)
}

func (r *articleRecallRepository) candidatesFromIDs(ctx context.Context, ids []string, source string) ([]domain.Candidate, error) {
	if len(ids) == 0 {
		return []domain.Candidate{}, nil
	}
	metas, err := r.articleRepo.GetArticlesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]storage.ArticleMeta, len(metas))
	for _, meta := range metas {
		byID[meta.ArticleID] = meta
	}
	candidates := make([]domain.Candidate, 0, len(ids))
	for i, id := range ids {
		meta, ok := byID[id]
		if !ok {
			continue
		}
		score := float64(meta.Score)
		candidates = append(candidates, domain.Candidate{
			ArticleID: meta.ArticleID,
			Score:     score,
			Source:    source,
			Scores: map[string]float64{
				"recall_score": score,
				"score":        score,
				"freshness":    freshnessScore(i),
				"quality":      productionDefaultQualityScore,
			},
			Extra: map[string]any{
				"title":     meta.Title,
				"cover":     meta.Cover,
				"type_tags": splitTags(meta.TypeTags),
				"tags":      splitTags(meta.Tags),
			},
		})
	}
	return candidates, nil
}

type channelPoolRepository struct {
	poolRepo *storage.PoolRepo
}

func newChannelPoolRepository(poolRepo *storage.PoolRepo) *channelPoolRepository {
	return &channelPoolRepository{poolRepo: poolRepo}
}

func (r *channelPoolRepository) GetChannelPool(ctx context.Context, channel string, topK int) ([]domain.Candidate, error) {
	if r == nil || r.poolRepo == nil {
		return nil, sql.ErrConnDone
	}
	userID := strings.TrimSpace(channel)
	if userID == "" {
		userID = "default"
	}
	items, err := r.poolRepo.PopTopK(ctx, userID, storage.PoolPeriodic, "default", normalizeTopK(topK), false)
	if err != nil {
		return nil, err
	}
	candidates := make([]domain.Candidate, 0, len(items))
	for i, item := range items {
		score := float64(item.RemarkScore)
		if score == 0 {
			score = float64(item.Score)
		}
		candidates = append(candidates, domain.Candidate{
			ArticleID: item.ArticleID,
			Score:     score,
			Source:    "channel",
			Scores: map[string]float64{
				"recall_score": score,
				"score":        float64(item.Score),
				"similarity":   float64(item.Similarity),
				"freshness":    freshnessScore(i),
				"quality":      productionDefaultQualityScore,
			},
			Extra: map[string]any{
				"pool_type":     string(item.PoolType),
				"period_bucket": item.PeriodBucket,
				"remark_score":  float64(item.RemarkScore),
			},
		})
	}
	return candidates, nil
}

type staticQualityJudger struct{}

func (staticQualityJudger) Judge(_ context.Context, req domain.QualityRequest) (domain.ArticleQuality, error) {
	return domain.ArticleQuality{
		ArticleID:    req.ArticleID,
		Authority:    productionDefaultQualityScore,
		Depth:        productionDefaultQualityScore,
		Freshness:    productionDefaultQualityScore,
		Completeness: productionDefaultQualityScore,
		Readability:  productionDefaultQualityScore,
		Citation:     productionDefaultQualityScore,
		Overall:      productionDefaultQualityScore,
		Grade:        "B",
	}, nil
}

type productionRerankProvider struct{}

func (productionRerankProvider) Rerank(_ context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error) {
	items := cloneItems(rctx.Items)
	sortRecommendItems(items)
	topN := rctx.Config.Rerank.TopN
	if topN > 0 && len(items) > topN {
		items = items[:topN]
	}
	for i := range items {
		items[i].Rank = i + 1
		items[i].Reason = valueOrFallback(items[i].Reason, "reranked by production baseline")
		items[i].Features = mergeAnyMap(items[i].Features, map[string]any{
			"rerank_model": valueOrFallback(rctx.Config.Rerank.Model, "baseline"),
		})
	}
	cost := rctx.Cost
	cost.ToolCalls++
	return items, cost, nil
}

type kafkaEventHook struct{}

func (kafkaEventHook) Name() string { return "event.kafka" }

func (kafkaEventHook) OnEvent(ctx context.Context, event BehaviorEvent) (HookResult, error) {
	return HookResult{Decision: HookContinue}, eventpub.PublishRecommendationEvent(ctx, eventpub.Message{
		EventID: event.EventID, EventType: event.EventType, RequestID: event.RequestID, TraceID: event.TraceID,
		TenantID: event.TenantID, UserID: valueOrFallback(event.User.UserID, event.User.AnonymousID),
		ArticleID: event.ArticleID, Rank: event.Rank, Channel: event.Channel, PathTaken: event.PathTaken,
		Timestamp: event.Timestamp, Payload: event.Metadata,
	})
}

func normalizeTopK(topK int) int {
	if topK <= 0 {
		return productionDefaultTopK
	}
	return topK
}

func freshnessScore(index int) float64 {
	score := 1 - float64(index)*0.01
	if score < 0 {
		return 0
	}
	return score
}

func splitTags(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
