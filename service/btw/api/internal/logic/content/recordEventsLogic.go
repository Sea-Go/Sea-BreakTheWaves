// Code scaffolded by goctl. Safe to edit.
package content

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"time"
)

type RecordEventsLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewRecordEventsLogic(ctx context.Context, s *svc.ServiceContext) *RecordEventsLogic {
	return &RecordEventsLogic{ctx, s}
}
func (l *RecordEventsLogic) RecordEvents(req *types.EventsRequest) (*types.EventsResponse, error) {
	events := make([]item.Behavior, 0, len(req.Events))
	for _, v := range req.Events {
		if v.Type == "candidate" {
			return nil, item.ErrInvalid
		}
		at, err := time.Parse(time.RFC3339Nano, v.OccurredAt)
		if err != nil {
			return nil, item.ErrInvalid
		}
		events = append(events, item.Behavior{EventID: v.EventId, RequestID: v.RequestId, UserID: v.UserId, ItemID: v.ItemId, Scenario: v.Scenario, Type: v.Type, Position: v.Position, OccurredAt: at, LabelSource: "behavior"})
	}
	n, err := l.svcCtx.Content.RecordEvents(l.ctx, events)
	return &types.EventsResponse{Accepted: n}, err
}
