// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package search

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
)

type ToolsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewToolsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ToolsLogic {
	return &ToolsLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *ToolsLogic) Tools() error {
	// todo: add your logic here and delete this line

	return nil
}
