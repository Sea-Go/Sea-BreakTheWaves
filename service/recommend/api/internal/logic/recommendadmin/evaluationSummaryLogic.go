// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package recommendadmin

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
)

type EvaluationSummaryLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewEvaluationSummaryLogic(ctx context.Context, svcCtx *svc.ServiceContext) *EvaluationSummaryLogic {
	return &EvaluationSummaryLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *EvaluationSummaryLogic) EvaluationSummary() error {
	// todo: add your logic here and delete this line

	return nil
}
