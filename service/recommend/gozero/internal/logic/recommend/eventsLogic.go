package recommend

import (
	"context"
	"encoding/json"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/types"
	recommendclient "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendclient"

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
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var out recommendclient.EventBatchResponse
	if err := l.svcCtx.Events.SubmitEvents(l.ctx, "application/json", body, &out); err != nil {
		return nil, err
	}
	return &types.EventBatchResp{Accepted: int32(out.Accepted)}, nil
}
