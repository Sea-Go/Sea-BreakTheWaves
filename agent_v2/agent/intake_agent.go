package agent

import (
	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/planner/builtin"
	"trpc.group/trpc-go/trpc-agent-go/skill"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const intakeAgentInstruction = `
你是旅行需求准入分析 Agent。

## 职责
1. 按当前任务加载 travel-requirement-intake 或 travel-requirement-merge skill
2. 根据 skill 规则输出结构化 SkillResult JSON
3. 只处理需求抽取、需求合并、追问生成、默认补齐和轻量意图分类

## 禁止
- 创建 TripPlan
- 调用地图工具（amap_*）
- 调用攻略工具（zhihu_*、bilibili_*）
- 调用天气工具（get_weather_context、check_weather_feasibility）
- 调用图写入工具（create_trip_plan、split_parent_node）
- 生成旅行方案

## 输出格式
只输出一个 SkillResult JSON object，不要 markdown，不要解释。

## 硬性约束
- 只能输出 TravelRequirementSnapshot 的合法字段。
- 已有非空字段不得重复追问。
- 不要用空值覆盖已有非空字段。
- P0 字段不能默认。
- 是否默认必须写入 result.default_intent。
`

// newIntakeOnlyAgent 创建一个轻量级需求准入 Agent。
// 不含任何业务工具（图/地图/攻略/天气），只加载 intake/merge skills。
// 由 TravelSkillOrchestrator 调用，负责字段抽取和 SkillResult 输出。
func newIntakeOnlyAgent() agentcore.Agent {
	thinkingEnabled := true
	temperature := 0.1
	topP := 0.6

	alimodel := newModelForLevel("intake-agent", ModelLevelMedium)

	intakePlanner := builtin.New(builtin.Options{
		ThinkingEnabled: &thinkingEnabled,
	})

	skillRepo, err := skill.NewFSRepository("skills")
	if err != nil {
		log.Errorf("[intake-agent] 加载 skills 仓库失败: %v", err)
	}

	// intake agent 不需要任何业务工具
	// 只通过 skill 加载 travel-requirement-intake 和 travel-requirement-merge
	var emptyTools []tool.Tool

	opts := []llmagent.Option{
		llmagent.WithModel(alimodel),
		llmagent.WithPlanner(intakePlanner),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			Temperature:     &temperature,
			TopP:            &topP,
			ThinkingEnabled: &thinkingEnabled,
		}),
		llmagent.WithTools(emptyTools),
		llmagent.WithEnableContextCompaction(true),
		llmagent.WithContextCompactionKeepRecentRequests(2),
		llmagent.WithDescription("旅行需求准入 Agent，只做需求分析和字段抽取，不创建图节点，不调用地图/攻略/天气工具。"),
		llmagent.WithInstruction(intakeAgentInstruction),
	}

	if skillRepo != nil {
		opts = append(opts,
			llmagent.WithSkills(skillRepo),
			llmagent.WithSkillToolProfile(llmagent.SkillToolProfileFull),
			llmagent.WithSkillLoadMode("turn"),
			llmagent.WithMaxLoadedSkills(2),
		)
	}

	return llmagent.New("intake-agent", opts...)
}
