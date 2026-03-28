package poolrefill

import (
	"testing"

	searchsvc "sea/service"
)

func TestMergeQueryMatchResultsUsesBestScoresAndDedupes(t *testing.T) {
	first := searchsvc.QueryMatchResult{
		CoarseCandidates: []searchsvc.CoarseArticleCandidate{
			{ArticleID: "a", CoarseScore: 0.60},
			{ArticleID: "b", CoarseScore: 0.50},
		},
		FineCandidates: []searchsvc.VectorCandidate{
			{ArticleID: "a", ChunkID: "a#1", VectorScore: 0.70},
		},
		PassedHits: []searchsvc.RerankHit{
			{ArticleID: "a", ChunkID: "a#1", MatchScore: 0.65, RerankScore: 0.61},
		},
		SkillMeta: searchsvc.DashscopeTextRerankOutput{
			RequestID: "req-1",
			Model:     "rerank-v1",
		},
	}
	second := searchsvc.QueryMatchResult{
		CoarseCandidates: []searchsvc.CoarseArticleCandidate{
			{ArticleID: "a", CoarseScore: 0.90},
			{ArticleID: "c", CoarseScore: 0.55},
		},
		FineCandidates: []searchsvc.VectorCandidate{
			{ArticleID: "a", ChunkID: "a#1", VectorScore: 0.92},
			{ArticleID: "c", ChunkID: "c#1", VectorScore: 0.80},
		},
		PassedHits: []searchsvc.RerankHit{
			{ArticleID: "c", ChunkID: "c#1", MatchScore: 0.91, RerankScore: 0.88},
		},
	}

	merged := mergeQueryMatchResults([]searchsvc.QueryMatchResult{first, second}, 5)
	if len(merged.CoarseCandidates) != 3 {
		t.Fatalf("expected 3 merged coarse candidates, got %d", len(merged.CoarseCandidates))
	}
	if merged.CoarseCandidates[0].ArticleID != "a" {
		t.Fatalf("expected article a to lead coarse candidates, got %s", merged.CoarseCandidates[0].ArticleID)
	}
	if len(merged.PassedHits) != 2 {
		t.Fatalf("expected 2 merged passed hits, got %d", len(merged.PassedHits))
	}
	if merged.PassedHits[0].ArticleID != "c" {
		t.Fatalf("expected highest match score article c first, got %s", merged.PassedHits[0].ArticleID)
	}
	if got := merged.VectorScoreByChunk["a#1"]; got != 0.92 {
		t.Fatalf("expected max vector score to win for a#1, got %.2f", got)
	}
	if merged.SkillMeta.RequestID != "req-1" {
		t.Fatalf("expected first non-empty rerank request id to be preserved, got %s", merged.SkillMeta.RequestID)
	}
}

func TestMergeQueryTextsKeepsLatestUniqueQueriesWithinLimit(t *testing.T) {
	got := mergeQueryTexts([]string{"美妆", "旅行", "日语"}, []string{"旅行", "考研", "穿搭"}, 3)
	want := []string{"旅行", "考研", "穿搭"}
	if len(got) != len(want) {
		t.Fatalf("expected %d queries, got %d: %v", len(want), len(got), got)
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("expected merged queries %v, got %v", want, got)
		}
	}
}
