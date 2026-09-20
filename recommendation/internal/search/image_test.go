package search

import (
	"context"
	"errors"
	"testing"
)

// ============================================================================
// 该文件测试 internal/search/image.go 的 ImageSearcher。
// 覆盖：正常/空结果/部分元数据缺失/图谱错误/元数据错误/空 URL/topK 截断/
// topK 默认值/nil metaRepo。
// ============================================================================

// stubImageGraphClient 用于测试的 ImageGraphClient stub。
type stubImageGraphClient struct {
	ids []string
	err error
}

func (s *stubImageGraphClient) ImageSearchArticles(_ context.Context, _ string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.ids, nil
}

// stubMetaRepo 用于测试的 ArticleMetaRepo stub。
type stubMetaRepo struct {
	data map[string]ArticleMeta
	err  error
}

func (s *stubMetaRepo) LoadMany(_ context.Context, ids []string) (map[string]ArticleMeta, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]ArticleMeta, len(ids))
	for _, id := range ids {
		if m, ok := s.data[id]; ok {
			out[id] = m
		}
	}
	return out, nil
}

// TestImageSearcher_Search_Normal 验证正常以图搜文流程与分数递减。
func TestImageSearcher_Search_Normal(t *testing.T) {
	gc := &stubImageGraphClient{ids: []string{"a1", "a2", "a3"}}
	mr := &stubMetaRepo{data: map[string]ArticleMeta{
		"a1": {ArticleID: "a1", Title: "文章1", CoverURL: "u1", AuthorID: "au1"},
		"a2": {ArticleID: "a2", Title: "文章2", CoverURL: "u2", AuthorID: "au2"},
		"a3": {ArticleID: "a3", Title: "文章3", CoverURL: "u3", AuthorID: "au3"},
	}}
	is := NewImageSearcher(gc, mr)
	got, err := is.Search(context.Background(), "http://img/a.png", 10)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("期望 3 条, 实际 %d", len(got))
	}
	// 验证分数递减：1.0, 0.5, 1/3。
	if got[0].Score != 1.0 {
		t.Errorf("首位 Score = %v, 期望 1.0", got[0].Score)
	}
	if got[1].Score != 0.5 {
		t.Errorf("第二位 Score = %v, 期望 0.5", got[1].Score)
	}
	if got[2].Score < 0.333 || got[2].Score > 0.334 {
		t.Errorf("第三位 Score = %v, 期望 ~0.333", got[2].Score)
	}
	// 验证元数据。
	if got[0].Title != "文章1" {
		t.Errorf("首位 Title = %q, 期望 文章1", got[0].Title)
	}
	if got[0].AuthorID != "au1" {
		t.Errorf("首位 AuthorID = %q, 期望 au1", got[0].AuthorID)
	}
	if got[0].CoverURL != "u1" {
		t.Errorf("首位 CoverURL = %q, 期望 u1", got[0].CoverURL)
	}
}

// TestImageSearcher_Search_EmptyResult 验证空结果返回空 slice。
func TestImageSearcher_Search_EmptyResult(t *testing.T) {
	gc := &stubImageGraphClient{ids: nil}
	mr := &stubMetaRepo{}
	is := NewImageSearcher(gc, mr)
	got, err := is.Search(context.Background(), "http://img/a.png", 10)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空结果应返回空, 实际 %d", len(got))
	}
}

// TestImageSearcher_Search_PartialMissingMeta 验证部分元数据缺失时仍返回全部命中。
func TestImageSearcher_Search_PartialMissingMeta(t *testing.T) {
	gc := &stubImageGraphClient{ids: []string{"a1", "a2", "a3"}}
	// 仅 a1 与 a3 有元数据，a2 缺失。
	mr := &stubMetaRepo{data: map[string]ArticleMeta{
		"a1": {ArticleID: "a1", Title: "文章1"},
		"a3": {ArticleID: "a3", Title: "文章3"},
	}}
	is := NewImageSearcher(gc, mr)
	got, err := is.Search(context.Background(), "http://img/a.png", 10)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("期望 3 条（含缺失元数据）, 实际 %d", len(got))
	}
	if got[0].Title != "文章1" {
		t.Errorf("a1 Title = %q, 期望 文章1", got[0].Title)
	}
	if got[1].Title != "" {
		t.Errorf("a2 Title 应为空（缺失元数据）, 实际 %q", got[1].Title)
	}
	if got[1].ArticleID != "a2" {
		t.Errorf("a2 ArticleID = %q, 期望 a2", got[1].ArticleID)
	}
	if got[2].Title != "文章3" {
		t.Errorf("a3 Title = %q, 期望 文章3", got[2].Title)
	}
}

// TestImageSearcher_Search_GraphError 验证图谱查询错误返回 error。
func TestImageSearcher_Search_GraphError(t *testing.T) {
	gc := &stubImageGraphClient{err: errors.New("graph down")}
	mr := &stubMetaRepo{}
	is := NewImageSearcher(gc, mr)
	_, err := is.Search(context.Background(), "http://img/a.png", 10)
	if err == nil {
		t.Fatalf("期望返回错误, 实际 nil")
	}
}

// TestImageSearcher_Search_MetaError 验证元数据加载错误返回 error。
func TestImageSearcher_Search_MetaError(t *testing.T) {
	gc := &stubImageGraphClient{ids: []string{"a1"}}
	mr := &stubMetaRepo{err: errors.New("meta down")}
	is := NewImageSearcher(gc, mr)
	_, err := is.Search(context.Background(), "http://img/a.png", 10)
	if err == nil {
		t.Fatalf("期望返回错误, 实际 nil")
	}
}

// TestImageSearcher_Search_EmptyURL 验证空 URL 返回 error。
func TestImageSearcher_Search_EmptyURL(t *testing.T) {
	gc := &stubImageGraphClient{}
	mr := &stubMetaRepo{}
	is := NewImageSearcher(gc, mr)
	_, err := is.Search(context.Background(), "", 10)
	if err == nil {
		t.Fatalf("空 URL 应返回错误")
	}
}

// TestImageSearcher_Search_TopKCap 验证 topK 截断。
func TestImageSearcher_Search_TopKCap(t *testing.T) {
	gc := &stubImageGraphClient{ids: []string{"a1", "a2", "a3", "a4", "a5"}}
	mr := &stubMetaRepo{}
	is := NewImageSearcher(gc, mr)
	got, err := is.Search(context.Background(), "http://img/a.png", 3)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("期望 topK=3 截断, 实际 %d", len(got))
	}
}

// TestImageSearcher_Search_TopKDefault 验证 topK<=0 时默认 20。
func TestImageSearcher_Search_TopKDefault(t *testing.T) {
	gc := &stubImageGraphClient{ids: []string{"a1"}}
	mr := &stubMetaRepo{}
	is := NewImageSearcher(gc, mr)
	got, err := is.Search(context.Background(), "http://img/a.png", 0)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("期望 1 条, 实际 %d", len(got))
	}
}

// TestImageSearcher_Search_NilMetaRepo 验证 metaRepo 为 nil 时仍返回结果（元数据留空）。
func TestImageSearcher_Search_NilMetaRepo(t *testing.T) {
	gc := &stubImageGraphClient{ids: []string{"a1", "a2"}}
	// metaRepo 传 nil。
	is := NewImageSearcher(gc, nil)
	got, err := is.Search(context.Background(), "http://img/a.png", 10)
	if err != nil {
		t.Fatalf("metaRepo nil 不应返回错误, 实际 %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("期望 2 条, 实际 %d", len(got))
	}
	// 元数据应为空。
	for i, h := range got {
		if h.Title != "" {
			t.Errorf("第 %d 条 Title 应为空, 实际 %q", i, h.Title)
		}
		if h.ArticleID == "" {
			t.Errorf("第 %d 条 ArticleID 不应为空", i)
		}
	}
}
