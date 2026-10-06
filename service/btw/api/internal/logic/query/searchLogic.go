// Code scaffolded by goctl. Safe to edit.
package query

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/mapping"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
)

type SearchLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewSearchLogic(ctx context.Context, s *svc.ServiceContext) *SearchLogic {
	return &SearchLogic{ctx, s}
}
func (l *SearchLogic) Search(req *types.QueryRequest) (*types.Result, error) {
	out, err := l.svcCtx.Search.Search(l.ctx, mapping.Request(req))
	if err != nil {
		return nil, err
	}
	l.svcCtx.RecordCandidates(l.ctx, mapping.Request(req), &out)
	return mapping.Result(out), nil
}
