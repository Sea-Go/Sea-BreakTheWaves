package search

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/gozero/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/gozero/internal/types"
	searchclient "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/searchclient"

	"github.com/zeromicro/go-zero/core/logx"
)

type SearchLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewSearchLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SearchLogic {
	return &SearchLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *SearchLogic) Search(req *types.SearchReq) (*types.SearchResp, error) {
	out, err := l.svcCtx.Search.Search(l.ctx, searchclient.ContentSearchRequest{
		SearchRequestID: req.SearchRequestId,
		RequestID:       req.SearchRequestId,
		Query:           req.Query,
		TopK:            int(req.Topk),
		Explain:         req.Explain,
	})
	if err != nil {
		return nil, err
	}

	resp := &types.SearchResp{
		TraceId:         out.TraceID,
		SearchRequestId: out.SearchRequestID,
		Status:          out.Status,
		Explanation:     out.Explanation,
		Hits:            make([]types.SearchHit, 0, len(out.Items)),
	}
	for _, item := range out.Items {
		resp.Hits = append(resp.Hits, types.SearchHit{
			ArticleId:     item.ArticleID,
			Title:         item.Title,
			CoverImageUrl: item.Cover,
			ManualTypeTag: item.TypeTags,
			SecondaryTags: []string{item.Tags},
			Snippet:       item.Snippet,
			ChunkId:       item.ChunkID,
			ArticleScore:  float64(item.ArticleScore),
			VectorScore:   float64(item.VectorScore),
			RerankScore:   float64(item.RerankScore),
			MatchScore:    float64(item.MatchScore),
		})
	}
	return resp, nil
}
