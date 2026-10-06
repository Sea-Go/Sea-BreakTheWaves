// Code scaffolded by goctl. Safe to edit.
package content

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
	"time"
)

type GetJobLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetJobLogic(ctx context.Context, s *svc.ServiceContext) *GetJobLogic {
	return &GetJobLogic{ctx, s}
}
func (l *GetJobLogic) GetJob(req *types.JobRequest) (*types.Job, error) {
	v, err := l.svcCtx.Content.Job(l.ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return &types.Job{Id: v.ID, RevisionId: v.RevisionID, Kind: v.Kind, Stage: v.Stage, State: v.State, Attempt: v.Attempt, Error: v.Error, ParsedSections: v.ParsedSections, UnderstoodSections: v.UnderstoodSections, NextRun: v.NextRun.Format(time.RFC3339)}, nil
}
