package service

import "testing"

func TestBuildMilvusArticleFilterExprIncludeAndExclude(t *testing.T) {
	filter := buildMilvusArticleFilterExpr(
		[]string{"article-1", "article-2"},
		[]string{"article-9", "article-10"},
	)

	expected := `(article_id in ["article-1","article-2"]) and (article_id not in ["article-9","article-10"])`
	if filter != expected {
		t.Fatalf("expected %q, got %q", expected, filter)
	}
}

func TestBuildMilvusArticleFilterExprExcludeOnly(t *testing.T) {
	filter := buildMilvusArticleFilterExpr(nil, []string{"article-9"})

	expected := `(article_id not in ["article-9"])`
	if filter != expected {
		t.Fatalf("expected %q, got %q", expected, filter)
	}
}
