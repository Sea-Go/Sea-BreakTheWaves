package service

import (
	"context"
	"errors"

	"sea/storage"
)

type ArticleTitleSearchItem struct {
	ArticleID     string   `json:"article_id"`
	Title         string   `json:"title"`
	Brief         string   `json:"brief,omitempty"`
	Cover         string   `json:"cover,omitempty"`
	ManualTypeTag string   `json:"manual_type_tag,omitempty"`
	SecondaryTags []string `json:"secondary_tags,omitempty"`
	AuthorID      string   `json:"author_id,omitempty"`
	AuthorName    string   `json:"author_name,omitempty"`
}

type ArticleTitleSearchResponse struct {
	TraceID         string                   `json:"trace_id"`
	SearchRequestID string                   `json:"search_request_id"`
	Status          string                   `json:"status"`
	Items           []ArticleTitleSearchItem `json:"items"`
}

type articleTitleSearchRepo interface {
	SearchPublishedByTitle(ctx context.Context, query string, limit int) ([]storage.SourceArticleSearchRecord, error)
}

type ArticleTitleSearchService struct {
	repo articleTitleSearchRepo
}

func NewArticleTitleSearchService(repo articleTitleSearchRepo) *ArticleTitleSearchService {
	if isNilSearchDependency(repo) {
		return &ArticleTitleSearchService{}
	}
	return &ArticleTitleSearchService{repo: repo}
}

func (s *ArticleTitleSearchService) Search(ctx context.Context, req StructuredSearchRequest) (ArticleTitleSearchResponse, error) {
	req = normalizeStructuredSearchRequest("title", req)
	resp := ArticleTitleSearchResponse{
		TraceID:         newMetadataSearchTraceID(),
		SearchRequestID: req.SearchRequestID,
		Status:          "ok",
		Items:           []ArticleTitleSearchItem{},
	}

	if req.Query == "" {
		return resp, errors.New("query cannot be empty")
	}
	if s == nil || s.repo == nil {
		return resp, ErrSourceMetadataUnavailable
	}

	records, err := s.repo.SearchPublishedByTitle(ctx, req.Query, req.TopK)
	if err != nil {
		return resp, err
	}

	resp.Items = make([]ArticleTitleSearchItem, 0, len(records))
	for _, record := range records {
		resp.Items = append(resp.Items, ArticleTitleSearchItem{
			ArticleID:     record.ArticleID,
			Title:         record.Title,
			Brief:         record.Brief,
			Cover:         record.Cover,
			ManualTypeTag: record.ManualTypeTag,
			SecondaryTags: append([]string(nil), record.SecondaryTags...),
			AuthorID:      record.AuthorID,
			AuthorName:    record.AuthorName,
		})
	}

	return resp, nil
}
