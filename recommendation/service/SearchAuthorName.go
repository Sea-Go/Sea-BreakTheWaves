package service

import (
	"context"
	"errors"
	"time"

	"sea/storage"
)

type AuthorNameSearchItem struct {
	AuthorID           string    `json:"author_id"`
	AuthorName         string    `json:"author_name"`
	ArticleCount       int       `json:"article_count"`
	LatestArticleID    string    `json:"latest_article_id,omitempty"`
	LatestArticleTitle string    `json:"latest_article_title,omitempty"`
	LatestArticleTime  time.Time `json:"latest_article_time,omitempty"`
}

type AuthorNameSearchResponse struct {
	TraceID         string                 `json:"trace_id"`
	SearchRequestID string                 `json:"search_request_id"`
	Status          string                 `json:"status"`
	Authors         []AuthorNameSearchItem `json:"authors"`
}

type authorNameSearchRepo interface {
	SearchAuthorsByName(ctx context.Context, query string, limit int) ([]storage.SourceAuthorSearchRecord, error)
}

type AuthorNameSearchService struct {
	repo authorNameSearchRepo
}

func NewAuthorNameSearchService(repo authorNameSearchRepo) *AuthorNameSearchService {
	if isNilSearchDependency(repo) {
		return &AuthorNameSearchService{}
	}
	return &AuthorNameSearchService{repo: repo}
}

func (s *AuthorNameSearchService) Search(ctx context.Context, req StructuredSearchRequest) (AuthorNameSearchResponse, error) {
	req = normalizeStructuredSearchRequest("author", req)
	resp := AuthorNameSearchResponse{
		TraceID:         newMetadataSearchTraceID(),
		SearchRequestID: req.SearchRequestID,
		Status:          "ok",
		Authors:         []AuthorNameSearchItem{},
	}

	if req.Query == "" {
		return resp, errors.New("query cannot be empty")
	}
	if s == nil || s.repo == nil {
		return resp, ErrSourceMetadataUnavailable
	}

	records, err := s.repo.SearchAuthorsByName(ctx, req.Query, req.TopK)
	if err != nil {
		return resp, err
	}

	resp.Authors = make([]AuthorNameSearchItem, 0, len(records))
	for _, record := range records {
		resp.Authors = append(resp.Authors, AuthorNameSearchItem{
			AuthorID:           record.AuthorID,
			AuthorName:         record.AuthorName,
			ArticleCount:       record.ArticleCount,
			LatestArticleID:    record.LatestArticleID,
			LatestArticleTitle: record.LatestArticleTitle,
			LatestArticleTime:  record.LatestArticleTime,
		})
	}

	return resp, nil
}
