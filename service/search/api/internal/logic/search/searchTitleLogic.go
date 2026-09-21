// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package search

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type SearchTitleLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewSearchTitleLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SearchTitleLogic {
	return &SearchTitleLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *SearchTitleLogic) SearchTitle(req *types.StructuredSearchReq) (resp *types.SearchResp, err error) {
	// todo: add your logic here and delete this line

	return
}
