// Code scaffolded by goctl. Safe to edit.
package query

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/mapping"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
)

type AnswerLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewAnswerLogic(ctx context.Context, s *svc.ServiceContext) *AnswerLogic {
	return &AnswerLogic{ctx, s}
}
func (l *AnswerLogic) Answer(req *types.QueryRequest) (*types.Result, error) {
	out, err := l.svcCtx.Search.Generate(l.ctx, mapping.Request(req), false, nil)
	if err != nil {
		return nil, err
	}
	l.svcCtx.RecordCandidates(l.ctx, mapping.Request(req), &out)
	return mapping.Result(out), nil
}
