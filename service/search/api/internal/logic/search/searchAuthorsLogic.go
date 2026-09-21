// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package search

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type SearchAuthorsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewSearchAuthorsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SearchAuthorsLogic {
	return &SearchAuthorsLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *SearchAuthorsLogic) SearchAuthors(req *types.StructuredSearchReq) (resp *types.SearchResp, err error) {
	// todo: add your logic here and delete this line

	return
}
