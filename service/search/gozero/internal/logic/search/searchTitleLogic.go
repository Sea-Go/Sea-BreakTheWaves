package search

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/gozero/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/gozero/internal/types"
	searchclient "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/searchclient"

	"github.com/zeromicro/go-zero/core/logx"
)

type SearchTitleLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewSearchTitleLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SearchTitleLogic {
	return &SearchTitleLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *SearchTitleLogic) SearchTitle(req *types.StructuredSearchReq) (*types.SearchResp, error) {
	out, err := l.svcCtx.Search.SearchTitle(l.ctx, searchclient.StructuredSearchRequest{
		SearchRequestID: req.RequestId, Query: req.Query, TopK: int(req.Limit)})
	if err != nil {
		return nil, err
	}
	resp := &types.SearchResp{TraceId: out.TraceID, SearchRequestId: out.SearchRequestID, Status: out.Status}
	for _, item := range out.Items {
		resp.Hits = append(resp.Hits, types.SearchHit{ArticleId: item.ArticleID, Title: item.Title,
			Brief: item.Brief, CoverImageUrl: item.Cover, ManualTypeTag: item.ManualTypeTag,
			SecondaryTags: item.SecondaryTags, AuthorId: item.AuthorID, AuthorName: item.AuthorName})
	}
	return resp, nil
}
