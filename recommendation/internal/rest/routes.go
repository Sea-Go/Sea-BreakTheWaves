package rest

import (
	"context"

	v1 "sea/api/recommendation/v1"
	"sea/internal/handler"

	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/restful"
)

type fullBody struct{}

func (fullBody) Body() string                          { return "*" }
func (fullBody) Locate(msg restful.ProtoMessage) interface{} { return msg }

func RegisterRoutes(h *handler.RecommendationHandler) {
	router := restful.NewRouter(
		restful.WithServiceName("recommendation.v1.RecommendationService.rest"),
		restful.WithFilterFunc(func() filter.ServerChain {
			return filter.ServerChain{}
		}),
	)

	router.AddImplBinding(&restful.Binding{
		Name:       "/api/v1/reco/recommend",
		Input:      func() restful.ProtoMessage { return &v1.RecommendRequest{} },
		Output:     func() restful.ProtoMessage { return &v1.RecommendResponse{} },
		HTTPMethod: "POST",
		Pattern:    restful.Enforce("/api/v1/reco/recommend"),
		Body:       fullBody{},
		Filter: func(_ interface{}, ctx context.Context, reqBody interface{}) (interface{}, error) {
			return h.Recommend(ctx, reqBody.(*v1.RecommendRequest))
		},
	}, nil)

	router.AddImplBinding(&restful.Binding{
		Name:       "/api/v1/search",
		Input:      func() restful.ProtoMessage { return &v1.ContentSearchRequest{} },
		Output:     func() restful.ProtoMessage { return &v1.ContentSearchResponse{} },
		HTTPMethod: "POST",
		Pattern:    restful.Enforce("/api/v1/search"),
		Body:       fullBody{},
		Filter: func(_ interface{}, ctx context.Context, reqBody interface{}) (interface{}, error) {
			return h.ContentSearch(ctx, reqBody.(*v1.ContentSearchRequest))
		},
	}, nil)

	router.AddImplBinding(&restful.Binding{
		Name:       "/api/v1/docs/ingest",
		Input:      func() restful.ProtoMessage { return &v1.IngestRequest{} },
		Output:     func() restful.ProtoMessage { return &v1.IngestResponse{} },
		HTTPMethod: "POST",
		Pattern:    restful.Enforce("/api/v1/docs/ingest"),
		Body:       fullBody{},
		Filter: func(_ interface{}, ctx context.Context, reqBody interface{}) (interface{}, error) {
			return h.IngestDocument(ctx, reqBody.(*v1.IngestRequest))
		},
	}, nil)

	router.AddImplBinding(&restful.Binding{
		Name:       "/api/v1/search/title",
		Input:      func() restful.ProtoMessage { return &v1.TitleSearchRequest{} },
		Output:     func() restful.ProtoMessage { return &v1.TitleSearchResponse{} },
		HTTPMethod: "POST",
		Pattern:    restful.Enforce("/api/v1/search/title"),
		Body:       fullBody{},
		Filter: func(_ interface{}, ctx context.Context, reqBody interface{}) (interface{}, error) {
			return h.SearchTitle(ctx, reqBody.(*v1.TitleSearchRequest))
		},
	}, nil)

	router.AddImplBinding(&restful.Binding{
		Name:       "/api/v1/search/authors",
		Input:      func() restful.ProtoMessage { return &v1.AuthorSearchRequest{} },
		Output:     func() restful.ProtoMessage { return &v1.AuthorSearchResponse{} },
		HTTPMethod: "POST",
		Pattern:    restful.Enforce("/api/v1/search/authors"),
		Body:       fullBody{},
		Filter: func(_ interface{}, ctx context.Context, reqBody interface{}) (interface{}, error) {
			return h.SearchAuthor(ctx, reqBody.(*v1.AuthorSearchRequest))
		},
	}, nil)

	router.AddImplBinding(&restful.Binding{
		Name:       "/api/v1/onboarding/questionnaire",
		Input:      func() restful.ProtoMessage { return &v1.OnboardingRequest{} },
		Output:     func() restful.ProtoMessage { return &v1.OnboardingResponse{} },
		HTTPMethod: "POST",
		Pattern:    restful.Enforce("/api/v1/onboarding/questionnaire"),
		Body:       fullBody{},
		Filter: func(_ interface{}, ctx context.Context, reqBody interface{}) (interface{}, error) {
			return h.SubmitOnboarding(ctx, reqBody.(*v1.OnboardingRequest))
		},
	}, nil)

	router.AddImplBinding(&restful.Binding{
		Name:       "/health",
		Input:      func() restful.ProtoMessage { return &v1.HealthRequest{} },
		Output:     func() restful.ProtoMessage { return &v1.HealthResponse{} },
		HTTPMethod: "GET",
		Pattern:    restful.Enforce("/health"),
		Filter: func(_ interface{}, ctx context.Context, reqBody interface{}) (interface{}, error) {
			return h.HealthCheck(ctx, reqBody.(*v1.HealthRequest))
		},
	}, nil)

	restful.RegisterRouter("recommendation.v1.RecommendationService.rest", router)
}
