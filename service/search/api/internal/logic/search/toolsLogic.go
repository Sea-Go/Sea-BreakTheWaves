package search

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/searchservice"
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

func (l *ToolsLogic) Tools() ([]string, error) {
	out, err := l.svcCtx.SearchService.Tools(l.ctx, &searchservice.ToolsRequest{})
	if err != nil {
		return nil, err
	}
	return out.Tools, nil
}
