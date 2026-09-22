package logic

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/svc"
	recommendpb "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/pb"
	"github.com/zeromicro/go-zero/core/logx"
)

type ListSkillsLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewListSkillsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ListSkillsLogic {
	return &ListSkillsLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *ListSkillsLogic) ListSkills(*recommendpb.SkillListRequest) (*recommendpb.SkillListResponse, error) {
	out := l.svcCtx.Recommend.ListSkills()
	resp := &recommendpb.SkillListResponse{}
	for _, skill := range out {
		resp.Skills = append(resp.Skills, &recommendpb.Skill{Name: skill.Name, Description: skill.Description,
			Category: skill.Category, CostLevel: skill.CostLevel, Online: skill.Online})
	}
	return resp, nil
}
