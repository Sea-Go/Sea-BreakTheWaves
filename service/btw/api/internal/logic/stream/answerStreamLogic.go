// Code scaffolded by goctl. Safe to edit.
package stream

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/mapping"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
)

type AnswerStreamLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewAnswerStreamLogic(ctx context.Context, s *svc.ServiceContext) *AnswerStreamLogic {
	return &AnswerStreamLogic{ctx, s}
}
func (l *AnswerStreamLogic) AnswerStream(req *types.QueryRequest, client chan<- *types.StreamEvent) error {
	input := mapping.Request(req)
	emit := func(e item.StreamEvent) error {
		select {
		case <-l.ctx.Done():
			return l.ctx.Err()
		case client <- mapping.Event(e):
			return nil
		}
	}
	out, err := l.svcCtx.Search.Generate(l.ctx, input, false, emit)
	if err != nil {
		_ = emit(item.StreamEvent{Type: "error", RequestID: input.RequestID, Error: err.Error()})
		return err
	}
	l.svcCtx.RecordCandidates(l.ctx, input, &out)
	return emit(item.StreamEvent{Type: "final", RequestID: input.RequestID, Result: &out})
}
