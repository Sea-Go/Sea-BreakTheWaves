package search

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/searchservice"
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

func (l *SearchLogic) Search(req *types.SearchReq) (*types.SearchResp, error) {
	out, err := l.svcCtx.SearchService.Search(l.ctx, &searchservice.SearchRequest{
		SearchRequestId: req.SearchRequestId, UserId: req.UserId, SessionId: req.SessionId,
		Query: req.Query, TopK: req.Topk, NeedAnswer: req.NeedAnswer, Explain: req.Explain,
	})
	if err != nil {
		return nil, err
	}
	return mapSearchResponse(out), nil
}

func mapSearchResponse(out *searchservice.SearchResponse) *types.SearchResp {
	resp := &types.SearchResp{TraceId: out.TraceId, SearchRequestId: out.SearchRequestId,
		Status: out.Status, Answer: out.Answer, ErrMsg: out.ErrorMessage}
	for _, hit := range out.Hits {
		resp.Hits = append(resp.Hits, types.SearchHit{ArticleId: hit.ArticleId, Title: hit.Title,
			Snippet: hit.Snippet, ArticleScore: hit.Score, VectorScore: hit.Score, MatchScore: hit.Score})
	}
	return resp
}
