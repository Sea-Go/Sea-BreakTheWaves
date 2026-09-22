package recommend

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendservice"
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

func (l *RecommendLogic) Recommend(req *types.RecommendReq) (*types.RecommendResp, error) {
	out, err := l.svcCtx.RecommendService.Recommend(l.ctx, recommendRequest(req))
	if err != nil {
		return nil, err
	}
	resp := &types.RecommendResp{RequestId: out.RequestId, TraceId: out.TraceId, PathTaken: out.PathTaken, ErrMsg: out.ErrorMessage}
	for _, item := range out.Items {
		resp.Items = append(resp.Items, types.RecommendItem{Id: item.Id, ArticleId: item.ArticleId,
			Score: item.Score, Source: item.Source, Rank: int(item.Rank), Reason: item.Reason})
	}
	return resp, nil
}

func recommendRequest(req *types.RecommendReq) *recommendservice.RecommendRequest {
	return &recommendservice.RecommendRequest{TenantId: req.TenantId, RequestId: req.RequestId,
		Scenario: req.Scenario, Channel: req.Channel, UserId: req.UserId, Query: req.Query,
		TopK: req.TopK, PathMode: req.PathMode, Debug: req.Debug}
}
