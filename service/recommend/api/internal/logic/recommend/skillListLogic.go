package recommend

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendservice"
	"github.com/zeromicro/go-zero/core/logx"
)

type SkillListLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewSkillListLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SkillListLogic {
	return &SkillListLogic{ctx: ctx, svcCtx: svcCtx, Logger: logx.WithContext(ctx)}
}

func (l *SkillListLogic) SkillList() ([]map[string]any, error) {
	out, err := l.svcCtx.RecommendService.ListSkills(l.ctx, &recommendservice.SkillListRequest{})
	if err != nil {
		return nil, err
	}
	skills := make([]map[string]any, 0, len(out.Skills))
	for _, skill := range out.Skills {
		skills = append(skills, map[string]any{"name": skill.Name, "description": skill.Description,
			"category": skill.Category, "cost_level": skill.CostLevel, "online": skill.Online})
	}
	return skills, nil
}
