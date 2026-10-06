// Code scaffolded by goctl. Safe to edit.
package content

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/mapping"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"unicode/utf8"
)

type ReadSourceLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewReadSourceLogic(ctx context.Context, s *svc.ServiceContext) *ReadSourceLogic {
	return &ReadSourceLogic{ctx, s}
}
func (l *ReadSourceLogic) ReadSource(req *types.SourceRequest) (*types.SourceContent, error) {
	loc := item.Location{RevisionID: req.Revision, SectionID: req.Section, Start: req.Start, End: req.End}
	text, err := l.svcCtx.Content.Read(l.ctx, loc, l.svcCtx.Config.Limits.ReadBytes)
	if err != nil {
		return nil, err
	}
	loc.End = loc.Start + int64(utf8.RuneCountInString(text))
	return &types.SourceContent{Text: text, Location: mapping.Location(loc)}, nil
}
