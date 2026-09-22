package recommend

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendservice"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc"
)

type RecommendStreamLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewRecommendStreamLogic(ctx context.Context, svcCtx *svc.ServiceContext) *RecommendStreamLogic {
	return &RecommendStreamLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *RecommendStreamLogic) RecommendStream(req *types.RecommendReq) (recommendservice.RecommendService_StreamRecommendClient, error) {
	return l.svcCtx.RecommendService.StreamRecommend(l.ctx, recommendRequest(req))
}

var _ grpc.ClientStream = (recommendservice.RecommendService_StreamRecommendClient)(nil)
