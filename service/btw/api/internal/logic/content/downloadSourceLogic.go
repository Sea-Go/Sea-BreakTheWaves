// Code scaffolded by goctl. Safe to edit.
package content

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"io"
)

type DownloadSourceLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewDownloadSourceLogic(ctx context.Context, s *svc.ServiceContext) *DownloadSourceLogic {
	return &DownloadSourceLogic{ctx, s}
}
func (l *DownloadSourceLogic) DownloadSource(revision string, start, length int64) (io.ReadCloser, item.Revision, error) {
	return l.svcCtx.Content.File(l.ctx, revision, start, length)
}
