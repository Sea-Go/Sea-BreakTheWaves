package logic

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/svc"
	asyncpb "github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/pb"
	"github.com/zeromicro/go-zero/core/logx"
)

type GetProjectionLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewGetProjectionLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetProjectionLogic {
	return &GetProjectionLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *GetProjectionLogic) GetProjection(in *asyncpb.ProjectionQuery) (*asyncpb.ProjectionSnapshot, error) {
	return &asyncpb.ProjectionSnapshot{SubjectId: in.SubjectId, Projection: in.Projection}, nil
}
