// Code scaffolded by goctl. Safe to edit.
package content

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/mapping"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
)

type GetItemLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetItemLogic(ctx context.Context, s *svc.ServiceContext) *GetItemLogic {
	return &GetItemLogic{ctx, s}
}
func (l *GetItemLogic) GetItem(req *types.ItemRequest) (*types.Item, error) {
	v, err := l.svcCtx.Content.Item(l.ctx, req.Id)
	if err != nil {
		return nil, err
	}
	out := mapping.Item(v)
	return &out, nil
}
