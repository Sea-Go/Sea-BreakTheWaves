// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package async

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/gozero/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/gozero/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type ProcessingStateLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewProcessingStateLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ProcessingStateLogic {
	return &ProcessingStateLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *ProcessingStateLogic) ProcessingState() (resp *types.ProcessingStateResp, err error) {
	// todo: add your logic here and delete this line

	return
}
