package poolrefill

import (
	"context"
	"errors"
	"strings"
	"time"

	"sea/config"
	embeddingservice "sea/embedding/service"
	"sea/metrics"
	searchsvc "sea/service"
	"sea/storage"
	types "sea/type"
	"sea/zlog"

	"go.uber.org/zap"
)

type PoolRefillExecutionRunner struct {
	poolRepo    *storage.PoolRepo
	articleRepo *storage.ArticleRepo
	reranker    searchsvc.RerankInvoker
}

func NewPoolRefillExecutionRunner(poolRepo *storage.PoolRepo, articleRepo *storage.ArticleRepo, reranker searchsvc.RerankInvoker) *PoolRefillExecutionRunner {
	return &PoolRefillExecutionRunner{
		poolRepo:    poolRepo,
		articleRepo: articleRepo,
		reranker:    reranker,
	}
}

func (r *PoolRefillExecutionRunner) Run(ctx context.Context, job types.PoolRefillJob) (types.PoolRefillRunResult, error) {
	job = normalizePoolRefillJob(job, config.Cfg.Pools.Async.QueryFanoutValue())
	if job.UserID == "" || job.PoolType == "" {
		return types.PoolRefillRunResult{}, errors.New("pool refill job is invalid")
	}
	if len(job.QueryTexts) == 0 {
		return types.PoolRefillRunResult{}, errors.New("pool refill queries are empty")
	}
	if r.poolRepo == nil || r.articleRepo == nil || r.reranker == nil {
		return types.PoolRefillRunResult{}, errors.New("pool refill dependencies are not initialized")
	}

	policy := poolPolicyFor(job.PoolType)
	if policy.RefillSize <= 0 {
		policy.RefillSize = 200
	}

	matchOpt := searchsvc.QueryMatchOptions{
		CoarseRecallK:        maxInt(policy.RefillSize*4, defaultInt(config.Cfg.Search.CoarseRecallK, 80)),
		FineRecallK:          maxInt(policy.RefillSize*2, defaultInt(config.Cfg.Search.FineRecallK, 40)),
		MaxArticleCandidates: maxInt(policy.RefillSize*2, defaultInt(config.Cfg.Search.MaxArticleCandidates, 20)),
		MinRerankScore:       float32(defaultFloat(config.Cfg.Search.MinRerankScore, 0.10)),
		MinPassScore:         float32(defaultFloat(config.Cfg.Search.MinPassScore, 0.55)),
		SupportBonus:         float32(defaultFloat(config.Cfg.Search.SupportBonus, 0.03)),
		RerankTopK:           maxInt(policy.RefillSize*2, 20),
	}

	ranker := searchsvc.NewPrecisionRanker(r.articleRepo, r.reranker)
	successes := make([]searchsvc.QueryMatchResult, 0, len(job.QueryTexts))
	failedQueries := 0
	for _, queryText := range job.QueryTexts {
		result, err := r.runSingleQuery(ctx, ranker, strings.TrimSpace(queryText), matchOpt)
		if err != nil {
			failedQueries++
			zlog.L().Warn("pool refill query failed",
				zap.String("user_id", job.UserID),
				zap.String("pool_type", job.PoolType),
				zap.String("period_bucket", job.PeriodBucket),
				zap.String("query_text", queryText),
				zap.Error(err),
			)
			continue
		}
		successes = append(successes, result)
	}

	if len(successes) == 0 {
		return types.PoolRefillRunResult{
			PoolType:          job.PoolType,
			PeriodBucket:      job.PeriodBucket,
			FailedQueries:     failedQueries,
			SuccessfulQueries: 0,
		}, errors.New("all pool refill queries failed")
	}

	merged := mergeQueryMatchResults(successes, matchOpt.MaxArticleCandidates)
	scoreMap := map[string]float32{}
	if len(merged.ArticleIDs) > 0 {
		var err error
		scoreMap, err = r.articleRepo.GetArticleScores(ctx, merged.ArticleIDs)
		if err != nil {
			return types.PoolRefillRunResult{
				PoolType:          job.PoolType,
				PeriodBucket:      job.PeriodBucket,
				FailedQueries:     failedQueries,
				SuccessfulQueries: len(successes),
			}, err
		}
	}

	stageStart := time.Now()
	items, considered, coverage := buildPoolItems(job, policy.RefillSize, merged, scoreMap)
	if err := r.poolRepo.AddItems(ctx, items); err != nil {
		return types.PoolRefillRunResult{
			PoolType:          job.PoolType,
			PeriodBucket:      job.PeriodBucket,
			FailedQueries:     failedQueries,
			SuccessfulQueries: len(successes),
		}, err
	}
	metrics.PoolRefillStageDurationSeconds.WithLabelValues("pool_add").Observe(time.Since(stageStart).Seconds())

	sizeAfter, err := r.poolRepo.GetPoolSize(ctx, job.UserID, storage.PoolType(job.PoolType), job.PeriodBucket)
	if err != nil {
		return types.PoolRefillRunResult{
			PoolType:          job.PoolType,
			PeriodBucket:      job.PeriodBucket,
			FailedQueries:     failedQueries,
			SuccessfulQueries: len(successes),
		}, err
	}

	metrics.PoolRefillItemsInserted.WithLabelValues(job.PoolType).Add(float64(len(items)))

	_, sp := zlog.StartSpan(ctx, "side_effect.pool_refill")
	sp.End(zlog.StatusOK, nil,
		zap.Any("side_effect", map[string]any{
			"type":            "pool_refill",
			"pool_type":       job.PoolType,
			"period_bucket":   job.PeriodBucket,
			"inserted":        len(items),
			"considered":      considered,
			"pool_size_after": sizeAfter,
			"query_count":     len(job.QueryTexts),
			"failed_queries":  failedQueries,
		}),
		zap.Any("retrieval", map[string]any{
			"returned_doc_count":     len(merged.ArticleIDs),
			"passed_chunk_count":     len(merged.PassedHits),
			"coverage_score":         coverage,
			"empty":                  len(items) == 0,
			"coarse_candidate_count": len(merged.CoarseCandidates),
			"fine_candidate_count":   len(merged.FineCandidates),
			"rerank_request_id":      merged.SkillMeta.RequestID,
			"rerank_model":           merged.SkillMeta.Model,
		}),
	)

	return types.PoolRefillRunResult{
		PoolType:          job.PoolType,
		PeriodBucket:      job.PeriodBucket,
		Inserted:          len(items),
		Considered:        considered,
		PoolSizeAfter:     sizeAfter,
		ReturnedDocCount:  len(merged.ArticleIDs),
		CoverageScore:     coverage,
		Empty:             len(items) == 0,
		FailedQueries:     failedQueries,
		SuccessfulQueries: len(successes),
	}, nil
}

func (r *PoolRefillExecutionRunner) runSingleQuery(
	ctx context.Context,
	ranker *searchsvc.PrecisionRanker,
	queryText string,
	opt searchsvc.QueryMatchOptions,
) (searchsvc.QueryMatchResult, error) {
	if strings.TrimSpace(queryText) == "" {
		return searchsvc.QueryMatchResult{}, errors.New("query_text cannot be empty")
	}

	stageStart := time.Now()
	vec, err := embeddingservice.TextVector(ctx, queryText)
	metrics.PoolRefillStageDurationSeconds.WithLabelValues("text_vector").Observe(time.Since(stageStart).Seconds())
	if err != nil {
		return searchsvc.QueryMatchResult{}, err
	}

	stageStart = time.Now()
	coarseCandidates, err := ranker.RecallCoarseArticleCandidates(ctx, vec, opt.CoarseRecallK)
	metrics.PoolRefillStageDurationSeconds.WithLabelValues("coarse_recall").Observe(time.Since(stageStart).Seconds())
	if err != nil {
		return searchsvc.QueryMatchResult{}, err
	}

	stageStart = time.Now()
	coarseCandidates = searchsvc.BoostCoarseCandidatesByKeywords(coarseCandidates, queryText, opt.QueryKeywords)
	articleIDs, coarseRankByArticle := searchsvc.PickArticleCandidates(coarseCandidates, opt.MaxArticleCandidates)
	metrics.PoolRefillStageDurationSeconds.WithLabelValues("coarse_rank").Observe(time.Since(stageStart).Seconds())

	result := searchsvc.QueryMatchResult{
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

	stageStart = time.Now()
	fineCandidates, err := ranker.RecallFineCandidatesByArticleIDs(ctx, vec, articleIDs, opt.FineRecallK)
	metrics.PoolRefillStageDurationSeconds.WithLabelValues("fine_recall").Observe(time.Since(stageStart).Seconds())
	if err != nil {
		return result, err
	}
	result.FineCandidates = fineCandidates
	if len(fineCandidates) == 0 {
		return result, nil
	}

	candidateIDs := make([]string, 0, len(fineCandidates))
	for idx, candidate := range fineCandidates {
		chunkID := strings.TrimSpace(candidate.ChunkID)
		if chunkID == "" {
			chunkID = strings.TrimSpace(candidate.ID)
		}
		if chunkID == "" {
			continue
		}
		candidateIDs = append(candidateIDs, chunkID)
		result.VectorScoreByChunk[chunkID] = candidate.VectorScore
		result.FineRankByChunk[chunkID] = idx + 1
		if articleID := strings.TrimSpace(candidate.ArticleID); articleID != "" {
			result.SupportByArticle[articleID]++
		}
	}
	if len(candidateIDs) == 0 {
		return result, nil
	}

	stageStart = time.Now()
	rerankedHits, skillMeta, err := ranker.RerankCandidates(ctx, queryText, candidateIDs, opt.RerankTopK)
	metrics.PoolRefillStageDurationSeconds.WithLabelValues("rerank").Observe(time.Since(stageStart).Seconds())
	if err != nil {
		return result, err
	}

	result.SkillMeta = skillMeta
	result.PassedHits = searchsvc.FilterPassedHits(
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
