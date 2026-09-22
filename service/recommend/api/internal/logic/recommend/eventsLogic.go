package recommend

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendservice"
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
	events := make([]*recommendservice.BehaviorEvent, 0, len(req.Events))
	for _, item := range req.Events {
		events = append(events, &recommendservice.BehaviorEvent{EventId: item["event_id"],
			EventType: item["event_type"], RequestId: item["request_id"], TraceId: item["trace_id"],
			TenantId: item["tenant_id"], UserId: item["user_id"], ArticleId: item["article_id"],
			Channel: item["channel"], PathTaken: item["path_taken"]})
	}
	out, err := l.svcCtx.RecommendService.SubmitEvents(l.ctx, &recommendservice.SubmitEventsRequest{Events: events})
	if err != nil {
		return nil, err
	}
	return &types.EventBatchResp{Accepted: out.Accepted}, nil
}
