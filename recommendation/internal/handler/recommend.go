package handler

import (
	"context"
	"encoding/json"

	"sea/agent"
	v1 "sea/api/recommendation/v1"
	searchsvc "sea/service"
	"sea/skillsys"
	"sea/zlog"

	"go.uber.org/zap"
)

// RecommendationHandler implements the tRPC RecommendationServiceService interface.
// It bridges tRPC proto types to the existing agent/service layer.
type RecommendationHandler struct {
	reg                *skillsys.Registry
	recoAgent          *agent.RecoAgent
	contentSearchAgent *agent.ContentSearchAgent
	titleSearchService *searchsvc.ArticleTitleSearchService
	authorSearchService *searchsvc.AuthorNameSearchService
	onboardingService  *searchsvc.OnboardingQuestionnaireService
}

func New(
	reg *skillsys.Registry,
	recoAgent *agent.RecoAgent,
	contentSearchAgent *agent.ContentSearchAgent,
	titleSearchService *searchsvc.ArticleTitleSearchService,
	authorSearchService *searchsvc.AuthorNameSearchService,
	onboardingService *searchsvc.OnboardingQuestionnaireService,
) *RecommendationHandler {
	return &RecommendationHandler{
		reg:                reg,
		recoAgent:          recoAgent,
		contentSearchAgent: contentSearchAgent,
		titleSearchService: titleSearchService,
		authorSearchService: authorSearchService,
		onboardingService:  onboardingService,
	}
}

// Recommend handles recommendation requests.
func (h *RecommendationHandler) Recommend(ctx context.Context, req *v1.RecommendRequest) (*v1.RecommendResponse, error) {
	internalReq := agent.RecommendRequest{
		RecRequestID: req.RecRequestId,
		UserID:       req.UserId,
		SessionID:    req.SessionId,
		Surface:      req.Surface,
		Query:        req.Query,
		PeriodBucket: req.PeriodBucket,
		Explain:      req.Explain,
	}
	resp, err := h.recoAgent.Recommend(ctx, internalReq)
	if err != nil {
		return &v1.RecommendResponse{
			TraceId:      resp.TraceID,
			RecRequestId: req.RecRequestId,
			Status:       "error",
			Explanation:  resp.Explanation,
		}, err
	}
	return &v1.RecommendResponse{
		TraceId:      resp.TraceID,
		RecRequestId: req.RecRequestId,
		Status:       resp.Status,
		Explanation:  resp.Explanation,
	}, nil
}

// ContentSearch handles content search requests.
func (h *RecommendationHandler) ContentSearch(ctx context.Context, req *v1.ContentSearchRequest) (*v1.ContentSearchResponse, error) {
	internalReq := agent.ContentSearchRequest{
		SearchRequestID: req.SearchRequestId,
		RequestID:       req.RequestId,
		Query:           req.Query,
		RecallK:         int(req.RecallK),
		CoarseRecallK:   int(req.CoarseRecallK),
		Explain:         req.Explain,
	}
	if req.Topk > 0 {
		internalReq.TopK = int(req.Topk)
	}
	resp, err := h.contentSearchAgent.Search(ctx, internalReq)
	if err != nil {
		return &v1.ContentSearchResponse{
			TraceId:         resp.TraceID,
			SearchRequestId: req.SearchRequestId,
			Status:          "error",
		}, err
	}
	return &v1.ContentSearchResponse{
		TraceId:         resp.TraceID,
		SearchRequestId: req.SearchRequestId,
		Status:          resp.Status,
	}, nil
}

// ingestArgs mirrors skills/doc_ingest.ingestArgs for JSON marshalling.
type ingestArgs struct {
	ArticleID   string   `json:"article_id"`
	Score       float64  `json:"score"`
	ArticleJSON string   `json:"article_json"`
	Markdown    string   `json:"markdown"`
	Title       string   `json:"title"`
	Cover       string   `json:"cover"`
	TypeTags    []string `json:"type_tags"`
	Tags        []string `json:"tags"`
}

// ingestResult mirrors skills/doc_ingest.ingestResult for JSON unmarshalling.
type ingestResult struct {
	ArticleID            string `json:"article_id"`
	CoarseVectorInserted bool   `json:"coarse_vector_inserted"`
	FineVectorInserted   int    `json:"fine_vector_inserted"`
	FineChunkCount       int    `json:"fine_chunk_count"`
	ImageVectorInserted  int    `json:"image_vector_inserted"`
	ImageChunkCount      int    `json:"image_chunk_count"`
	GraphEnabled         bool   `json:"graph_enabled"`
	GraphWriteOK         bool   `json:"graph_write_ok"`
}

// IngestDocument handles document ingestion requests via the doc_ingest tool.
func (h *RecommendationHandler) IngestDocument(ctx context.Context, req *v1.IngestRequest) (*v1.IngestResponse, error) {
	zlog.L().Info("tRPC ingest document", zap.String("article_id", req.ArticleId))

	args := ingestArgs{
		ArticleID:   req.ArticleId,
		Score:       req.Score,
		ArticleJSON: req.ArticleJson,
		Markdown:    req.Markdown,
		Title:       req.Title,
		Cover:       req.Cover,
	}
	argsRaw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}

	_, raw, err := h.reg.Invoke(ctx, "doc_ingest", argsRaw)
	if err != nil {
		return nil, err
	}

	var result ingestResult
	if raw != nil {
		if b, ok := raw.([]byte); ok {
			_ = json.Unmarshal(b, &result)
		} else if s, ok := raw.(string); ok {
			_ = json.Unmarshal([]byte(s), &result)
		}
	}

	return &v1.IngestResponse{
		ArticleId:            result.ArticleID,
		CoarseVectorInserted: result.CoarseVectorInserted,
		FineVectorInserted:   int32(result.FineVectorInserted),
		FineChunkCount:       int32(result.FineChunkCount),
		ImageVectorInserted:  int32(result.ImageVectorInserted),
		ImageChunkCount:      int32(result.ImageChunkCount),
		GraphEnabled:         result.GraphEnabled,
		GraphWriteOk:         result.GraphWriteOK,
	}, nil
}

// SearchTitle handles title search requests.
func (h *RecommendationHandler) SearchTitle(ctx context.Context, req *v1.TitleSearchRequest) (*v1.TitleSearchResponse, error) {
	internalReq := searchsvc.StructuredSearchRequest{
		SearchRequestID: req.SearchRequestId,
		Query:           req.Query,
		TopK:            int(req.Topk),
	}
	resp, err := h.titleSearchService.Search(ctx, internalReq)
	if err != nil {
		return &v1.TitleSearchResponse{
			TraceId:         resp.TraceID,
			SearchRequestId: req.SearchRequestId,
			Status:          "error",
		}, err
	}
	return &v1.TitleSearchResponse{
		TraceId:         resp.TraceID,
		SearchRequestId: req.SearchRequestId,
		Status:          resp.Status,
	}, nil
}

// SearchAuthor handles author search requests.
func (h *RecommendationHandler) SearchAuthor(ctx context.Context, req *v1.AuthorSearchRequest) (*v1.AuthorSearchResponse, error) {
	internalReq := searchsvc.StructuredSearchRequest{
		SearchRequestID: req.SearchRequestId,
		Query:           req.Query,
		TopK:            int(req.Topk),
	}
	resp, err := h.authorSearchService.Search(ctx, internalReq)
	if err != nil {
		return &v1.AuthorSearchResponse{
			TraceId:         resp.TraceID,
			SearchRequestId: req.SearchRequestId,
			Status:          "error",
		}, err
	}
	return &v1.AuthorSearchResponse{
		TraceId:         resp.TraceID,
		SearchRequestId: req.SearchRequestId,
		Status:          resp.Status,
	}, nil
}

// SubmitOnboarding handles onboarding questionnaire submissions.
func (h *RecommendationHandler) SubmitOnboarding(ctx context.Context, req *v1.OnboardingRequest) (*v1.OnboardingResponse, error) {
	internalReq := searchsvc.OnboardingQuestionnaireRequest{
		UserID:                          req.UserId,
		Username:                        req.Username,
		Interests:                       req.Interests,
		PrimaryPurpose:                  req.PrimaryPurpose,
		PreferredArticleTypes:           req.PreferredArticleTypes,
		PreferredArticleLength:          req.PreferredArticleLength,
		PreferredStyle:                  req.PreferredStyle,
		Backgrounds:                     req.Backgrounds,
		DifficultyPreference:            req.DifficultyPreference,
		ExcludedContents:                req.ExcludedContents,
		ReadingTimeSlots:                req.ReadingTimeSlots,
		PersonalizedRecommendationTypes: req.PersonalizedRecommendationTypes,
	}
	resp, err := h.onboardingService.Submit(ctx, internalReq)
	if err != nil {
		return &v1.OnboardingResponse{
			TraceId:   resp.TraceID,
			RequestId: req.RequestId,
			Status:    "error",
		}, err
	}
	return &v1.OnboardingResponse{
		TraceId:      resp.TraceID,
		RequestId:    req.RequestId,
		Status:       resp.Status,
		UserId:       resp.UserID,
		PeriodBucket: resp.PeriodBucket,
		MemoryTypes:  resp.MemoryTypes,
		Warnings:     resp.Warnings,
	}, nil
}

// HealthCheck returns service health status.
func (h *RecommendationHandler) HealthCheck(ctx context.Context, req *v1.HealthRequest) (*v1.HealthResponse, error) {
	return &v1.HealthResponse{Status: "ok"}, nil
}

// ListTools returns registered tool names.
func (h *RecommendationHandler) ListTools(ctx context.Context, req *v1.ToolListRequest) (*v1.ToolListResponse, error) {
	return &v1.ToolListResponse{}, nil
}
