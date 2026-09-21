package recommend

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
)

type SkillListLogic struct {
	logx.Logger
	svcCtx *svc.ServiceContext
}

func NewSkillListLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SkillListLogic {
	return &SkillListLogic{Logger: logx.WithContext(ctx), svcCtx: svcCtx}
}

func (l *SkillListLogic) SkillList() ([]map[string]any, error) {
	skills := l.svcCtx.Recommend.ListSkills()
	out := make([]map[string]any, 0, len(skills))
	for _, skill := range skills {
		out = append(out, map[string]any{"name": skill.Name, "description": skill.Description})
	}
	return out, nil
}
