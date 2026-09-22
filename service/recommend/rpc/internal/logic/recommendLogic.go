package logic

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/svc"
	recommendpb "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/pb"
	recommendclient "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendclient"
	"github.com/zeromicro/go-zero/core/logx"
)

type RecommendLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewRecommendLogic(ctx context.Context, svcCtx *svc.ServiceContext) *RecommendLogic {
	return &RecommendLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *RecommendLogic) Recommend(in *recommendpb.RecommendRequest) (*recommendpb.RecommendResponse, error) {
	out, err := l.svcCtx.Recommend.Recommend(l.ctx, recommendclient.RecommendRequest{
		TenantID: in.TenantId, RequestID: in.RequestId, Scenario: in.Scenario, Channel: in.Channel,
		User: recommendclient.UserIdentity{UserID: in.UserId}, Query: in.Query,
		TopK: int(in.TopK), PathMode: in.PathMode, Debug: in.Debug,
	})
	if err != nil {
		return nil, err
	}
	return mapRecommendResponse(out), nil
}

func mapRecommendResponse(out recommendclient.RecommendResponse) *recommendpb.RecommendResponse {
	resp := &recommendpb.RecommendResponse{RequestId: out.RequestID, TraceId: out.TraceID, PathTaken: out.PathTaken}
	for _, item := range out.Items {
		resp.Items = append(resp.Items, &recommendpb.RecommendItem{Id: item.ID, ArticleId: item.ArticleID,
			Score: item.Score, Source: item.Source, Rank: int32(item.Rank), Reason: item.Reason})
	}
	return resp
}
