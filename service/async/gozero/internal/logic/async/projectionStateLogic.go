// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package async

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/gozero/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/gozero/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type ProjectionStateLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewProjectionStateLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ProjectionStateLogic {
	return &ProjectionStateLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *ProjectionStateLogic) ProjectionState() (resp *types.ProcessingStateResp, err error) {
	// todo: add your logic here and delete this line

	return
}
