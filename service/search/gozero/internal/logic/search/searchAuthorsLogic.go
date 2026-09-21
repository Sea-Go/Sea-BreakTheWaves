package search

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/gozero/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/gozero/internal/types"
	searchclient "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/searchclient"

	"github.com/zeromicro/go-zero/core/logx"
)

type SearchAuthorsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewSearchAuthorsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SearchAuthorsLogic {
	return &SearchAuthorsLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *SearchAuthorsLogic) SearchAuthors(req *types.StructuredSearchReq) (*types.SearchResp, error) {
	out, err := l.svcCtx.Search.SearchAuthors(l.ctx, searchclient.StructuredSearchRequest{
		SearchRequestID: req.RequestId, Query: req.Query, TopK: int(req.Limit)})
	if err != nil {
		return nil, err
	}
	resp := &types.SearchResp{TraceId: out.TraceID, SearchRequestId: out.SearchRequestID, Status: out.Status}
	for _, item := range out.Authors {
		resp.Hits = append(resp.Hits, types.SearchHit{AuthorId: item.AuthorID, AuthorName: item.AuthorName})
	}
	return resp, nil
}
