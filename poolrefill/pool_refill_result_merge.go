package poolrefill

import (
	"sort"
	"strings"

	searchsvc "sea/service"
	"sea/storage"
	types "sea/type"
)

func mergeQueryTexts(existing []string, incoming []string, limit int) []string {
	merged := make([]string, 0, len(existing)+len(incoming))
	merged = append(merged, existing...)
	merged = append(merged, incoming...)
	return normalizeLatestQueries(merged, limit)
}

func normalizeLatestQueries(values []string, limit int) []string {
	if limit <= 0 {
		limit = 3
	}

	outRev := make([]string, 0, minInt(limit, len(values)))
	seen := make(map[string]struct{}, len(values))
	for i := len(values) - 1; i >= 0; i-- {
		queryText := strings.TrimSpace(values[i])
		if queryText == "" {
			continue
		}
		queryKey := strings.ToLower(queryText)
		if _, ok := seen[queryKey]; ok {
			continue
		}
		seen[queryKey] = struct{}{}
		outRev = append(outRev, queryText)
		if len(outRev) >= limit {
			break
		}
	}

	out := make([]string, 0, len(outRev))
	for i := len(outRev) - 1; i >= 0; i-- {
		out = append(out, outRev[i])
	}
	return out
}

func sameQueryTexts(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}
	return true
}

func mergeQueryMatchResults(results []searchsvc.QueryMatchResult, maxArticleCandidates int) searchsvc.QueryMatchResult {
	merged := searchsvc.QueryMatchResult{
		VectorScoreByChunk:  map[string]float32{},
		FineRankByChunk:     map[string]int{},
		SupportByArticle:    map[string]int{},
		CoarseRankByArticle: map[string]int{},
	}
	if len(results) == 0 {
		return merged
	}

	coarseBest := make(map[string]searchsvc.CoarseArticleCandidate)
	fineBest := make(map[string]searchsvc.VectorCandidate)
	passedBest := make(map[string]searchsvc.RerankHit)

	for _, result := range results {
		if merged.SkillMeta.RequestID == "" && result.SkillMeta.RequestID != "" {
			merged.SkillMeta = result.SkillMeta
		}

		for _, candidate := range result.CoarseCandidates {
			articleID := strings.TrimSpace(candidate.ArticleID)
			if articleID == "" {
				continue
			}
			prev, ok := coarseBest[articleID]
			if !ok || coarseCandidateScore(candidate) > coarseCandidateScore(prev) {
				coarseBest[articleID] = candidate
			}
		}

		for _, candidate := range result.FineCandidates {
			chunkID := strings.TrimSpace(candidate.ChunkID)
			if chunkID == "" {
				chunkID = strings.TrimSpace(candidate.ID)
			}
			if chunkID == "" {
				continue
			}
			candidate.ChunkID = chunkID
			prev, ok := fineBest[chunkID]
			if !ok || candidate.VectorScore > prev.VectorScore {
				fineBest[chunkID] = candidate
			}
		}

		for _, hit := range result.PassedHits {
			chunkID := strings.TrimSpace(hit.ChunkID)
			if chunkID == "" {
				chunkID = strings.TrimSpace(hit.ID)
			}
			if chunkID == "" {
				continue
			}
			hit.ChunkID = chunkID
			prev, ok := passedBest[chunkID]
			if !ok || hit.MatchScore > prev.MatchScore {
				passedBest[chunkID] = hit
			}
		}
	}

	merged.CoarseCandidates = make([]searchsvc.CoarseArticleCandidate, 0, len(coarseBest))
	for _, candidate := range coarseBest {
		merged.CoarseCandidates = append(merged.CoarseCandidates, candidate)
	}
	sortCoarseCandidates(merged.CoarseCandidates)
	merged.ArticleIDs, merged.CoarseRankByArticle = searchsvc.PickArticleCandidates(merged.CoarseCandidates, maxArticleCandidates)

	merged.FineCandidates = make([]searchsvc.VectorCandidate, 0, len(fineBest))
	for _, candidate := range fineBest {
		merged.FineCandidates = append(merged.FineCandidates, candidate)
	}
	sortFineCandidates(merged.FineCandidates)
	for idx, candidate := range merged.FineCandidates {
		merged.VectorScoreByChunk[candidate.ChunkID] = candidate.VectorScore
		merged.FineRankByChunk[candidate.ChunkID] = idx + 1
		if articleID := strings.TrimSpace(candidate.ArticleID); articleID != "" {
			merged.SupportByArticle[articleID]++
		}
	}

	merged.PassedHits = make([]searchsvc.RerankHit, 0, len(passedBest))
	for _, hit := range passedBest {
		merged.PassedHits = append(merged.PassedHits, hit)
	}
	sortPassedHits(merged.PassedHits)

	return merged
}

func buildPoolItems(job types.PoolRefillJob, limit int, match searchsvc.QueryMatchResult, articleScores map[string]float32) ([]storage.PoolItem, int, float32) {
	if limit <= 0 {
		limit = 200
	}

	type articleEntry struct {
		ArticleID   string
		Similarity  float32
		RemarkScore float32
	}

	bestByArticle := make(map[string]articleEntry, len(match.PassedHits))
	ordered := make([]string, 0, len(match.PassedHits))
	for _, hit := range match.PassedHits {
		articleID := strings.TrimSpace(hit.ArticleID)
		if articleID == "" {
			continue
		}
		entry := articleEntry{
			ArticleID:   articleID,
			Similarity:  match.VectorScoreByChunk[hit.ChunkID],
			RemarkScore: hit.MatchScore,
		}
		prev, ok := bestByArticle[articleID]
		if !ok {
			bestByArticle[articleID] = entry
			ordered = append(ordered, articleID)
			continue
		}
		if entry.RemarkScore > prev.RemarkScore {
			bestByArticle[articleID] = entry
		}
	}

	if len(bestByArticle) == 0 {
		for _, candidate := range match.CoarseCandidates {
			articleID := strings.TrimSpace(candidate.ArticleID)
			if articleID == "" {
				continue
			}
			if _, ok := bestByArticle[articleID]; ok {
				continue
			}
			bestByArticle[articleID] = articleEntry{
				ArticleID:   articleID,
				Similarity:  candidate.CoarseScore,
				RemarkScore: coarseFallbackScore(match.CoarseRankByArticle[articleID], len(match.CoarseRankByArticle)),
			}
			ordered = append(ordered, articleID)
			if len(ordered) >= limit {
				break
			}
		}
	}

	items := make([]storage.PoolItem, 0, minInt(limit, len(ordered)))
	var coverage float32
	for _, articleID := range ordered {
		entry := bestByArticle[articleID]
		items = append(items, storage.PoolItem{
			UserID:       job.UserID,
			PoolType:     storage.PoolType(job.PoolType),
			PeriodBucket: job.PeriodBucket,
			ArticleID:    articleID,
			Score:        articleScores[articleID],
			Similarity:   entry.Similarity,
			RemarkScore:  entry.RemarkScore,
		})
		coverage += entry.RemarkScore
		if len(items) >= limit {
			break
		}
	}
	if len(items) > 0 {
		coverage /= float32(len(items))
	}
	return items, len(bestByArticle), coverage
}

func coarseCandidateScore(candidate searchsvc.CoarseArticleCandidate) float32 {
	return candidate.CoarseScore + candidate.KeywordScore
}

func sortCoarseCandidates(candidates []searchsvc.CoarseArticleCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		leftScore := coarseCandidateScore(candidates[i])
		rightScore := coarseCandidateScore(candidates[j])
		if leftScore == rightScore {
			return candidates[i].ArticleID < candidates[j].ArticleID
		}
		return leftScore > rightScore
	})
}

func sortFineCandidates(candidates []searchsvc.VectorCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].VectorScore == candidates[j].VectorScore {
			return candidates[i].ChunkID < candidates[j].ChunkID
		}
		return candidates[i].VectorScore > candidates[j].VectorScore
	})
}

func sortPassedHits(hits []searchsvc.RerankHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].MatchScore == hits[j].MatchScore {
			if hits[i].RerankScore == hits[j].RerankScore {
				return hits[i].ChunkID < hits[j].ChunkID
			}
			return hits[i].RerankScore > hits[j].RerankScore
		}
		return hits[i].MatchScore > hits[j].MatchScore
	})
}

func coarseFallbackScore(rank, total int) float32 {
	if rank <= 0 || total <= 0 {
		return 0
	}
	if total == 1 {
		return 1
	}
	return 1 - float32(rank-1)/float32(total-1)
}

func defaultInt(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

func defaultFloat(v, fallback float64) float64 {
	if v > 0 {
		return v
	}
	return fallback
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
