package logic

import (
	"context"

	asynclient "sea/service/async/rpc/asyncclient"
	"sea/service/recommend/api/internal/types"
	recommendclient "sea/service/recommend/rpc/recommendclient"
)

type RecommendLogic struct {
	reco   *recommendclient.Client
	events *asynclient.Client
}

func NewRecommendLogic(reco *recommendclient.Client, events *asynclient.Client) *RecommendLogic {
	return &RecommendLogic{reco: reco, events: events}
}

func (l *RecommendLogic) Recommend(ctx context.Context, req types.RecommendRequest) (types.RecommendResponse, error) {
	return l.reco.Recommend(ctx, req)
}

func (l *RecommendLogic) Stream(ctx context.Context, req types.RecommendRequest) (<-chan recommendclient.StreamEvent, error) {
	return l.reco.StreamRecommend(ctx, req)
}

func (l *RecommendLogic) SubmitEvents(ctx context.Context, contentType string, body []byte) (types.EventBatchResponse, error) {
	var out types.EventBatchResponse
	err := l.events.SubmitEvents(ctx, contentType, body, &out)
	return out, err
}

func (l *RecommendLogic) Summary() any { return l.reco.Summary() }

func (l *RecommendLogic) Trace(req types.TraceQueryRequest) any { return l.reco.Trace(req) }

func (l *RecommendLogic) Skills() []any {
	values := l.reco.ListSkills()
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func (l *RecommendLogic) Evaluation(ctx context.Context, surface, window string) (any, error) {
	return l.reco.EvaluationSummary(ctx, surface, window)
}
