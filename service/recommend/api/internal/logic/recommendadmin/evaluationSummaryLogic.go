package recommendadmin

import (
	"context"
	"encoding/json"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendservice"
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

func (l *EvaluationSummaryLogic) EvaluationSummary(surface, window string) (json.RawMessage, error) {
	if surface == "" {
		surface = "dashboard_recommend"
	}
	if window == "" {
		window = "24h"
	}
	out, err := l.svcCtx.RecommendService.EvaluationSummary(l.ctx, &recommendservice.EvaluationSummaryRequest{Surface: surface, Window: window})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out.Json), nil
}
