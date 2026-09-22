package logic

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/svc"
	recommendpb "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/pb"
	recommendclient "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendclient"
	"github.com/zeromicro/go-zero/core/logx"
)

type StreamRecommendLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewStreamRecommendLogic(ctx context.Context, svcCtx *svc.ServiceContext) *StreamRecommendLogic {
	return &StreamRecommendLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *StreamRecommendLogic) StreamRecommend(in *recommendpb.RecommendRequest, stream recommendpb.RecommendService_StreamRecommendServer) error {
	events, err := l.svcCtx.Recommend.StreamRecommend(stream.Context(), recommendclient.RecommendRequest{
		TenantID: in.TenantId, RequestID: in.RequestId, Scenario: in.Scenario, Channel: in.Channel,
		User: recommendclient.UserIdentity{UserID: in.UserId}, Query: in.Query,
		TopK: int(in.TopK), PathMode: in.PathMode, Debug: in.Debug,
	})
	if err != nil {
		return err
	}
	for event := range events {
		if event.Response != nil {
			if err := stream.Send(mapRecommendResponse(*event.Response)); err != nil {
				return err
			}
		}
	}
	return nil
}
