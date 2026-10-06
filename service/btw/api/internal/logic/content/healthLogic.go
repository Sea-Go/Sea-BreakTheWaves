// Code scaffolded by goctl. Safe to edit.
package content

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
)

type HealthLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewHealthLogic(ctx context.Context, s *svc.ServiceContext) *HealthLogic {
	return &HealthLogic{ctx, s}
}
func (l *HealthLogic) Health(req *types.EmptyRequest) (*types.Health, error) {
	return &types.Health{Status: "alive"}, nil
}
