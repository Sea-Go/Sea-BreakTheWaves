package recommend

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/types"
	recommendclient "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendclient"

	"github.com/zeromicro/go-zero/core/logx"
)

type RecommendLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewRecommendLogic(ctx context.Context, svcCtx *svc.ServiceContext) *RecommendLogic {
	return &RecommendLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *RecommendLogic) Recommend(req *types.RecommendReq) (*types.RecommendResp, error) {
	out, err := l.svcCtx.Recommend.Recommend(l.ctx, recommendclient.RecommendRequest{
		TenantID: req.TenantId, RequestID: req.RequestId, Scenario: req.Scenario,
		Channel: req.Channel, Query: req.Query,
		TopK: int(req.TopK), PathMode: req.PathMode, Debug: req.Debug,
	})
	if err != nil {
		return nil, err
	}
	resp := &types.RecommendResp{RequestId: out.RequestID, TraceId: out.TraceID,
		PathTaken: out.PathTaken, Items: make([]types.RecommendItem, 0, len(out.Items)), ErrMsg: "",
	}
	for _, item := range out.Items {
		resp.Items = append(resp.Items, types.RecommendItem{Id: item.ID, ArticleId: item.ArticleID,
			Score: item.Score, Source: item.Source, Rank: item.Rank, Reason: item.Reason})
	}
	return resp, nil
}
