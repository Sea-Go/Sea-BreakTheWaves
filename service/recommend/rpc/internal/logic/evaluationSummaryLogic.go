package logic

import (
	"context"
	"encoding/json"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/svc"
	recommendpb "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/pb"
	"github.com/zeromicro/go-zero/core/logx"
)

type EvaluationSummaryLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewEvaluationSummaryLogic(ctx context.Context, svcCtx *svc.ServiceContext) *EvaluationSummaryLogic {
	return &EvaluationSummaryLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *EvaluationSummaryLogic) EvaluationSummary(in *recommendpb.EvaluationSummaryRequest) (*recommendpb.EvaluationSummaryResponse, error) {
	out, err := l.svcCtx.Recommend.EvaluationSummary(l.ctx, in.Surface, in.Window)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &recommendpb.EvaluationSummaryResponse{Json: raw}, nil
}
