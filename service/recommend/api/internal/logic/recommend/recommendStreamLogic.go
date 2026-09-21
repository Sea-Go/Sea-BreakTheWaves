// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package recommend

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type RecommendStreamLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewRecommendStreamLogic(ctx context.Context, svcCtx *svc.ServiceContext) *RecommendStreamLogic {
	return &RecommendStreamLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *RecommendStreamLogic) RecommendStream(req *types.RecommendReq) error {
	// todo: add your logic here and delete this line

	return nil
}
