// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package async

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/gozero/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/gozero/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type EventsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewEventsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *EventsLogic {
	return &EventsLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *EventsLogic) Events(req *types.EventBatchReq) (resp *types.EventBatchResp, err error) {
	// todo: add your logic here and delete this line

	return
}
