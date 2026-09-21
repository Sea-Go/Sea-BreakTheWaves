package async

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/types"
	"github.com/zeromicro/go-zero/core/logx"
)

type EventsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewEventsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *EventsLogic {
	return &EventsLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *EventsLogic) Events(req *types.EventBatchReq) (*types.EventBatchResp, error) {
	return &types.EventBatchResp{Accepted: int32(len(req.Events))}, nil
}
