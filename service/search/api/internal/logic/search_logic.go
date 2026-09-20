package logic

import (
	"context"

	"sea/service/search/api/internal/types"
	searchclient "sea/service/search/rpc/searchclient"
)

type SearchLogic struct {
	client *searchclient.Client
}

func NewSearchLogic(client *searchclient.Client) *SearchLogic { return &SearchLogic{client: client} }

func (l *SearchLogic) Search(ctx context.Context, req types.SearchRequest) (types.SearchResponse, error) {
	return l.client.Search(ctx, req)
}

func (l *SearchLogic) SearchTitle(ctx context.Context, req types.StructuredSearchRequest) (types.TitleSearchResponse, error) {
	return l.client.SearchTitle(ctx, req)
}

func (l *SearchLogic) SearchAuthors(ctx context.Context, req types.StructuredSearchRequest) (types.AuthorSearchResponse, error) {
	return l.client.SearchAuthors(ctx, req)
}

func (l *SearchLogic) Onboarding(ctx context.Context, req types.OnboardingQuestionnaireRequest) (types.OnboardingQuestionnaireResponse, error) {
	return l.client.SubmitOnboarding(ctx, req)
}

func (l *SearchLogic) Tools() []string { return l.client.Tools() }
