package service

import (
	"context"
	"errors"
	"testing"

	"sea/storage"
)

type fakeArticleTitleSearchRepo struct {
	query   string
	limit   int
	records []storage.SourceArticleSearchRecord
	err     error
}

func (f *fakeArticleTitleSearchRepo) SearchPublishedByTitle(_ context.Context, query string, limit int) ([]storage.SourceArticleSearchRecord, error) {
	f.query = query
	f.limit = limit
	if f.err != nil {
		return nil, f.err
	}
	return append([]storage.SourceArticleSearchRecord(nil), f.records...), nil
}

func TestArticleTitleSearchServiceSearch(t *testing.T) {
	repo := &fakeArticleTitleSearchRepo{
		records: []storage.SourceArticleSearchRecord{
			{
				ArticleID:     "a1",
				Title:         "旅游攻略",
				Brief:         "适合周末出发",
				Cover:         "cover.png",
				ManualTypeTag: "travel",
				SecondaryTags: []string{"韩国", "首尔"},
				AuthorID:      "1001",
				AuthorName:    "海风",
			},
		},
	}
	svc := NewArticleTitleSearchService(repo)

	resp, err := svc.Search(context.Background(), StructuredSearchRequest{
		RequestID: "legacy-request-id",
		Query:     " 旅游 ",
		TopK:      99,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if repo.query != "旅游" {
		t.Fatalf("expected trimmed query, got %q", repo.query)
	}
	if repo.limit != 50 {
		t.Fatalf("expected topk to clamp at 50, got %d", repo.limit)
	}
	if resp.SearchRequestID != "legacy-request-id" {
		t.Fatalf("expected legacy request id to be used, got %q", resp.SearchRequestID)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Items))
	}
	if resp.Items[0].AuthorName != "海风" {
		t.Fatalf("expected author name to be mapped, got %q", resp.Items[0].AuthorName)
	}
}

func TestArticleTitleSearchServiceUnavailable(t *testing.T) {
	svc := NewArticleTitleSearchService(nil)
	_, err := svc.Search(context.Background(), StructuredSearchRequest{Query: "旅游"})
	if !errors.Is(err, ErrSourceMetadataUnavailable) {
		t.Fatalf("expected source metadata unavailable, got %v", err)
	}
}
