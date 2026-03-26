package retrieval

import "testing"

func TestKeywordSignalScoreCapsAtOne(t *testing.T) {
	signals := buildQueryKeywordSignals("美妆 护肤 妆容 变美 精华 防晒", nil)
	score := keywordSignalScore(signals, "美妆,护肤,妆容,变美,精华,防晒")
	if score != 1.0 {
		t.Fatalf("expected keyword score 1.0, got %.2f", score)
	}
}

func TestBoostCoarseCandidatesByKeywords(t *testing.T) {
	candidates := []CoarseArticleCandidate{
		{ArticleID: "travel-1", CoarseScore: 0.9, Tags: "旅行,攻略,目的地"},
		{ArticleID: "beauty-1", CoarseScore: 0.7, Tags: "美妆,护肤,妆容"},
	}

	boosted := BoostCoarseCandidatesByKeywords(candidates, "想看美妆护肤内容", []string{"美妆", "护肤"})
	if len(boosted) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(boosted))
	}
	if boosted[0].ArticleID != "beauty-1" {
		t.Fatalf("expected beauty article to rank first, got %s", boosted[0].ArticleID)
	}
	if boosted[0].KeywordScore < 0.4 {
		t.Fatalf("expected keyword score >= 0.4, got %.2f", boosted[0].KeywordScore)
	}
}
