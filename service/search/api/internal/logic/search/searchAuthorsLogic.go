package search

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/types"
	"github.com/zeromicro/go-zero/core/logx"
)

type SearchAuthorsLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewSearchAuthorsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SearchAuthorsLogic {
	return &SearchAuthorsLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *SearchAuthorsLogic) SearchAuthors(req *types.StructuredSearchReq) (*types.SearchResp, error) {
	return structuredSearch(l.ctx, l.svcCtx, req, "authors")
}
