package async

import (
	"context"
	"encoding/json"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/asyncservice"
	"github.com/zeromicro/go-zero/core/logx"
)

type EventsLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewEventsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *EventsLogic {
	return &EventsLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *EventsLogic) Events(req *types.EventBatchReq) (*types.EventBatchResp, error) {
	events := make([]*asyncservice.EventEnvelope, 0, len(req.Events))
	for _, item := range req.Events {
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		events = append(events, &asyncservice.EventEnvelope{EventId: item["event_id"],
			EventType: item["event_type"], AggregateId: item["aggregate_id"], Payload: raw})
	}
	out, err := l.svcCtx.AsyncService.SubmitEvents(l.ctx, &asyncservice.SubmitEventsRequest{Events: events})
	if err != nil {
		return nil, err
	}
	return &types.EventBatchResp{Accepted: out.Accepted}, nil
}
