package async

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/types"
	"github.com/zeromicro/go-zero/core/logx"
)

type ProjectionStateLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewProjectionStateLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ProjectionStateLogic {
	return &ProjectionStateLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *ProjectionStateLogic) ProjectionState() (*types.ProcessingStateResp, error) {
	return &types.ProcessingStateResp{State: "healthy"}, nil
}
