// Code scaffolded by goctl. Safe to edit.
package query

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/mapping"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
)

type RecommendLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewRecommendLogic(ctx context.Context, s *svc.ServiceContext) *RecommendLogic {
	return &RecommendLogic{ctx, s}
}
func (l *RecommendLogic) Recommend(req *types.QueryRequest) (*types.Result, error) {
	out, err := l.svcCtx.Recommend.Recommend(l.ctx, mapping.Request(req), nil)
	if err != nil {
		return nil, err
	}
	l.svcCtx.RecordCandidates(l.ctx, mapping.Request(req), &out)
	return mapping.Result(out), nil
}
