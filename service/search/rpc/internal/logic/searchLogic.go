package logic

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/svc"
	searchpb "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/pb"
	"github.com/zeromicro/go-zero/core/logx"
)

type SearchLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewSearchLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SearchLogic {
	return &SearchLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *SearchLogic) Search(in *searchpb.SearchRequest) (*searchpb.SearchResponse, error) {
	out, err := l.svcCtx.Search.Search(l.ctx, mapSearchRequest(in))
	if err != nil {
		return nil, err
	}
	return mapSearchResponse(out), nil
}
