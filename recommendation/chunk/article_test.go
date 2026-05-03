package chunk

import "testing"

func TestKeywordCoverageScore(t *testing.T) {
	t.Run("caps at one", func(t *testing.T) {
		if got := KeywordCoverageScore(8); got != 1.0 {
			t.Fatalf("expected 1.0, got %.2f", got)
		}
	})

	t.Run("increments by point two", func(t *testing.T) {
		if got := KeywordCoverageScore(3); got != 0.6 {
			t.Fatalf("expected 0.6, got %.2f", got)
		}
	})
}

func TestSplitArticleBuildsCoarseProfile(t *testing.T) {
	article := Article{
		ArticleID: "article-1",
		Title:     "夏目友人帐的治愈日常",
		TypeTags:  []string{"动漫", "治愈"},
		Tags:      []string{"陪伴"},
		Sections: []Section{
			{
				H2: "猫咪老师与友情",
				Blocks: []Block{
					{Type: "text", Text: "这是一段正文。"},
				},
			},
		},
	}

	got, err := SplitArticle(article, 320, 40, 10)
	if err != nil {
		t.Fatalf("SplitArticle returned error: %v", err)
	}
	if got.KeywordScore <= 0 {
		t.Fatalf("expected keyword score to be positive, got %.2f", got.KeywordScore)
	}
	if got.CoarseRawText == "" || got.CoarseIntro == "" || got.CoarseText == "" {
		t.Fatalf("expected coarse profile fields to be populated: %+v", got)
	}
	if len(got.Keywords) == 0 {
		t.Fatalf("expected keywords to be extracted")
	}
}
