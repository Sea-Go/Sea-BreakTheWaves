package search

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/searchservice"
	"github.com/zeromicro/go-zero/core/logx"
)

type SearchTitleLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewSearchTitleLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SearchTitleLogic {
	return &SearchTitleLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *SearchTitleLogic) SearchTitle(req *types.StructuredSearchReq) (*types.SearchResp, error) {
	return structuredSearch(l.ctx, l.svcCtx, req, "title")
}

func structuredSearch(ctx context.Context, svcCtx *svc.ServiceContext, req *types.StructuredSearchReq, mode string) (*types.SearchResp, error) {
	out, err := svcCtx.SearchService.SearchStructured(ctx, &searchservice.StructuredSearchRequest{
		RequestId: req.RequestId, Query: req.Query, Limit: req.Limit, Mode: mode,
	})
	if err != nil {
		return nil, err
	}
	resp := &types.SearchResp{TraceId: out.TraceId, SearchRequestId: out.SearchRequestId, Status: out.Status, ErrMsg: out.ErrorMessage}
	for _, hit := range out.Hits {
		resp.Hits = append(resp.Hits, types.SearchHit{ArticleId: hit.ArticleId, Title: hit.Title, Snippet: hit.Snippet})
	}
	return resp, nil
}
