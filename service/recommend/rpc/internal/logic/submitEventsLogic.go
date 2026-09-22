package logic

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/svc"
	recommendpb "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/pb"
	recommendclient "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendclient"
	"github.com/zeromicro/go-zero/core/logx"
)

type SubmitEventsLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewSubmitEventsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SubmitEventsLogic {
	return &SubmitEventsLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *SubmitEventsLogic) SubmitEvents(in *recommendpb.SubmitEventsRequest) (*recommendpb.SubmitEventsResponse, error) {
	events := make([]recommendclient.BehaviorEvent, 0, len(in.Events))
	for _, e := range in.Events {
		events = append(events, recommendclient.BehaviorEvent{EventID: e.EventId, EventType: e.EventType,
			RequestID: e.RequestId, TraceID: e.TraceId, TenantID: e.TenantId,
			User: recommendclient.UserIdentity{UserID: e.UserId}, ArticleID: e.ArticleId,
			Rank: int(e.Rank), Channel: e.Channel, PathTaken: e.PathTaken})
	}
	out := l.svcCtx.Recommend.RecordEvents(l.ctx, recommendclient.EventBatchRequest{Events: events})
	return &recommendpb.SubmitEventsResponse{Accepted: int32(out.Accepted), Rejected: out.Rejected}, nil
}
