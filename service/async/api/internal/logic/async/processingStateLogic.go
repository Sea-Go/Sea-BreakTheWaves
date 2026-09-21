package async

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/types"
	"github.com/zeromicro/go-zero/core/logx"
)

type ProcessingStateLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewProcessingStateLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ProcessingStateLogic {
	return &ProcessingStateLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *ProcessingStateLogic) ProcessingState() (*types.ProcessingStateResp, error) {
	return &types.ProcessingStateResp{State: "healthy"}, nil
}
