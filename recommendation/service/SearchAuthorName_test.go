package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"sea/storage"
)

type fakeAuthorNameSearchRepo struct {
	query   string
	limit   int
	records []storage.SourceAuthorSearchRecord
	err     error
}

func (f *fakeAuthorNameSearchRepo) SearchAuthorsByName(_ context.Context, query string, limit int) ([]storage.SourceAuthorSearchRecord, error) {
	f.query = query
	f.limit = limit
	if f.err != nil {
		return nil, f.err
	}
	return append([]storage.SourceAuthorSearchRecord(nil), f.records...), nil
}

func TestAuthorNameSearchServiceSearch(t *testing.T) {
	now := time.Now().UTC()
	repo := &fakeAuthorNameSearchRepo{
		records: []storage.SourceAuthorSearchRecord{
			{
				AuthorID:           "2002",
				AuthorName:         "旅行的人",
				ArticleCount:       8,
				LatestArticleID:    "art-1",
				LatestArticleTitle: "韩国周末路线",
				LatestArticleTime:  now,
			},
		},
	}
	svc := NewAuthorNameSearchService(repo)

	resp, err := svc.Search(context.Background(), StructuredSearchRequest{
		SearchRequestID: "author-req",
		Query:           " 旅行 ",
		TopK:            3,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if repo.query != "旅行" {
		t.Fatalf("expected trimmed query, got %q", repo.query)
	}
	if repo.limit != 3 {
		t.Fatalf("expected topk to pass through, got %d", repo.limit)
	}
	if len(resp.Authors) != 1 {
		t.Fatalf("expected 1 author, got %d", len(resp.Authors))
	}
	if resp.Authors[0].LatestArticleTime != now {
		t.Fatalf("expected latest article time to be preserved")
	}
}

func TestAuthorNameSearchServiceUnavailable(t *testing.T) {
	svc := NewAuthorNameSearchService(nil)
	_, err := svc.Search(context.Background(), StructuredSearchRequest{Query: "作者"})
	if !errors.Is(err, ErrSourceMetadataUnavailable) {
		t.Fatalf("expected source metadata unavailable, got %v", err)
	}
}
