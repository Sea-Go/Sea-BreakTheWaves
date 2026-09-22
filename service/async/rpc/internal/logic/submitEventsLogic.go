package logic

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/svc"
	asyncpb "github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/pb"
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

func (l *SubmitEventsLogic) SubmitEvents(in *asyncpb.SubmitEventsRequest) (*asyncpb.SubmitEventsResponse, error) {
	return &asyncpb.SubmitEventsResponse{Accepted: int32(len(in.Events))}, nil
}
