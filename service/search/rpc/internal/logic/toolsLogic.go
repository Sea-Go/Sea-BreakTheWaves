package logic

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/svc"
	searchpb "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/pb"
	"github.com/zeromicro/go-zero/core/logx"
)

type ToolsLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewToolsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ToolsLogic {
	return &ToolsLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *ToolsLogic) Tools(*searchpb.ToolsRequest) (*searchpb.ToolsResponse, error) {
	return &searchpb.ToolsResponse{Tools: l.svcCtx.Search.Tools()}, nil
}
