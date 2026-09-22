package logic

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/svc"
	searchpb "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/pb"
	searchclient "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/searchclient"
	"github.com/zeromicro/go-zero/core/logx"
)

type SearchStructuredLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewSearchStructuredLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SearchStructuredLogic {
	return &SearchStructuredLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *SearchStructuredLogic) SearchStructured(in *searchpb.StructuredSearchRequest) (*searchpb.SearchResponse, error) {
	if in.Mode == "authors" {
		out, err := l.svcCtx.Search.SearchAuthors(l.ctx, searchclient.StructuredSearchRequest{
			SearchRequestID: in.RequestId, Query: in.Query, TopK: int(in.Limit)})
		if err != nil {
			return nil, err
		}
		resp := &searchpb.SearchResponse{TraceId: out.TraceID, SearchRequestId: out.SearchRequestID, Status: out.Status}
		for _, item := range out.Authors {
			resp.Hits = append(resp.Hits, &searchpb.SearchHit{ArticleId: item.AuthorID, Title: item.AuthorName})
		}
		return resp, nil
	}
	out, err := l.svcCtx.Search.SearchTitle(l.ctx, searchclient.StructuredSearchRequest{
		SearchRequestID: in.RequestId, Query: in.Query, TopK: int(in.Limit)})
	if err != nil {
		return nil, err
	}
	resp := &searchpb.SearchResponse{TraceId: out.TraceID, SearchRequestId: out.SearchRequestID, Status: out.Status}
	for _, item := range out.Items {
		resp.Hits = append(resp.Hits, &searchpb.SearchHit{ArticleId: item.ArticleID, Title: item.Title, Snippet: item.Brief})
	}
	return resp, nil
}
