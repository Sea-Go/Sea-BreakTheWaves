package recommendadmin

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
)

type EvaluationSummaryLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewEvaluationSummaryLogic(ctx context.Context, svcCtx *svc.ServiceContext) *EvaluationSummaryLogic {
	return &EvaluationSummaryLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *EvaluationSummaryLogic) EvaluationSummary() (any, error) {
	return l.svcCtx.Recommend.EvaluationSummary(l.ctx, "dashboard_recommend", "24h")
}
