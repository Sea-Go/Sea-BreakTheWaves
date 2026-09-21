package search

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/gozero/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
)

type ToolsLogic struct {
	logx.Logger
	svcCtx *svc.ServiceContext
}

func NewToolsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ToolsLogic {
	return &ToolsLogic{Logger: logx.WithContext(ctx), svcCtx: svcCtx}
}

func (l *ToolsLogic) Tools() ([]string, error) {
	return l.svcCtx.Search.Tools(), nil
}
