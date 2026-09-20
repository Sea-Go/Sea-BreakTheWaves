package chunk

import "testing"

func TestParseMarkdownArticleUsesFallbackTitleWhenMarkdownHasNoH1(t *testing.T) {
	md := "## 小标题\n正文内容\n"

	article, err := ParseMarkdownArticle("a1", md, "发布时输入的标题")
	if err != nil {
		t.Fatalf("ParseMarkdownArticle returned error: %v", err)
	}

	if article.Title != "发布时输入的标题" {
		t.Fatalf("expected fallback title to be used, got %q", article.Title)
	}
	if len(article.Sections) != 1 {
		t.Fatalf("expected one section, got %d", len(article.Sections))
	}
	if article.Sections[0].H2 != "小标题" {
		t.Fatalf("expected h2 to be kept, got %q", article.Sections[0].H2)
	}
}

func TestParseMarkdownArticleKeepsMarkdownTitleWhenPresent(t *testing.T) {
	md := "# Markdown 标题\n## 小标题\n正文内容\n"

	article, err := ParseMarkdownArticle("a2", md, "外部标题")
	if err != nil {
		t.Fatalf("ParseMarkdownArticle returned error: %v", err)
	}

	if article.Title != "Markdown 标题" {
		t.Fatalf("expected markdown title to win, got %q", article.Title)
	}
}
