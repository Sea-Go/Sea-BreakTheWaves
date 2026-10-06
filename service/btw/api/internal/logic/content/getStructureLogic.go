// Code scaffolded by goctl. Safe to edit.
package content

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
)

type GetStructureLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetStructureLogic(ctx context.Context, s *svc.ServiceContext) *GetStructureLogic {
	return &GetStructureLogic{ctx, s}
}
func (l *GetStructureLogic) GetStructure(req *types.StructureRequest) (*types.Structure, error) {
	revision, sections, err := l.svcCtx.Content.Structure(l.ctx, req.Id, req.After, req.Limit)
	if err != nil {
		return nil, err
	}
	out := &types.Structure{RevisionId: revision, Sections: []types.Section{}}
	for _, s := range sections {
		out.Sections = append(out.Sections, types.Section{Id: s.ID, ParentId: s.ParentID, Order: s.Order, Title: s.Title, Path: s.Path, Bytes: s.Bytes, Runes: s.Runes})
		out.Next = s.Order
	}
	navigation, err := l.svcCtx.Content.Navigation(l.ctx, revision, req.NavAfter, req.Limit)
	if err != nil {
		return nil, err
	}
	out.Navigation = []types.Navigation{}
	for _, n := range navigation {
		out.Navigation = append(out.Navigation, types.Navigation{Id: n.ID, ParentId: n.ParentID, Title: n.Title, Href: n.Href, SectionId: n.SectionID, Start: n.Start})
	}
	if len(navigation) == req.Limit {
		out.NavigationNext = req.NavAfter + len(navigation)
	}
	return out, nil
}
