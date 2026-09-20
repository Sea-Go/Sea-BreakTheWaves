package recommendationv2

import "context"

type RecommendationService struct {
	runtime *RecommendationRuntime
}

func NewRecommendationService(runtime *RecommendationRuntime) *RecommendationService {
	if runtime == nil {
		runtime = NewRecommendationRuntime()
	}
	return &RecommendationService{runtime: runtime}
}

func (s *RecommendationService) Recommend(ctx context.Context, req RecommendRequest) (RecommendResponse, error) {
	return s.runtime.Recommend(ctx, req)
}

func (s *RecommendationService) StreamRecommend(ctx context.Context, req RecommendRequest) (<-chan StreamEvent, error) {
	return s.runtime.StreamRecommend(ctx, req)
}

func (s *RecommendationService) RecordEvents(ctx context.Context, req EventBatchRequest) EventBatchResponse {
	return s.runtime.RecordEvents(ctx, req)
}

func (s *RecommendationService) Summary() ObservationSummary {
	return s.runtime.Summary()
}

func (s *RecommendationService) ListSkills() []SkillDefinition {
	return s.runtime.ListSkills()
}

func (s *RecommendationService) Trace(query TraceQueryRequest) TraceQueryResponse {
	return s.runtime.Trace(query)
}
