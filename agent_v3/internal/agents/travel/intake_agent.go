package travel

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
1. 从用户消息中抽取旅行需求字段
2. 判断哪些字段已知、哪些缺失
3. 输出 SkillResult JSON

## 禁止
- 创建 TripPlan
- 调用地图工具（amap_*）
- 调用攻略工具（zhihu_*、bilibili_*）
- 调用天气工具（get_weather_context、check_weather_feasibility）
- 调用图写入工具（create_trip_plan、split_parent_node）
- 生成旅行方案

## 输出格式
只输出一个 SkillResult JSON object，不要 markdown，不要解释。

字段说明：
- skill_name: 固定 "travel-requirement-intake"
- stage: 固定 "requirement_intake"
- status: "need_user_input"（需要用户补充）或 "ready"（信息充足）
- requirement_ready: boolean
- missing_fields: 缺失字段名数组
- follow_up_questions: 追问问题数组
- result.requirement: 已抽取的结构化需求字段
- next_stage: "awaiting_user_info" 或 "macro_planning"
- stop_workflow: true（需要用户回复时）或 false（可以进入规划时）
- output: 展示给用户的文本

## 字段名（必须使用这些 key）
- destination_scope: 目的地范围（如"全国""云南""大理"）
- total_days: 总天数（如 365, 7, 30）
- start_city: 出发城市
- start_date: 出发日期
- budget_total: 总预算
- transport_mode: 交通方式（自驾/高铁/飞机/混合）
- travel_style: 旅行风格数组（自然风光/历史文化/美食/摄影/慢旅行/亲子）
- pace: 节奏（轻松/均衡/紧凑）
- high_altitude_acceptance: 高海拔接受度（可接受/不接受/待确认）
- daily_driving_preference: 日均驾驶强度偏好（4小时内/4-6小时/可接受长途）

## 示例

输入："我要一个全国365天的旅游plan"
输出：
{
  "skill_name": "travel-requirement-intake",
  "stage": "requirement_intake",
  "status": "need_user_input",
  "requirement_ready": false,
  "missing_fields": ["start_city", "start_date", "budget", "transport_mode", "travel_style", "pace"],
  "follow_up_questions": [
    "你计划从哪个城市出发？",
    "这365天旅行大概从什么时候开始？",
    "总预算或每月预算大概是多少？",
    "主要交通方式是自驾、高铁火车、飞机，还是混合？",
    "你更偏自然风光、历史文化、美食城市、摄影打卡，还是慢旅行？",
    "每天节奏希望轻松、均衡，还是尽量多打卡？"
  ],
  "result": {
    "requirement": {
      "destination_scope": "全国",
      "total_days": 365
    }
  },
  "next_stage": "awaiting_user_info",
  "stop_workflow": true,
  "output": "在开始规划前，我需要先确认几项关键信息：\n\n1. 你计划从哪个城市出发？\n2. 这365天旅行大概从什么时候开始？\n3. 总预算或每月预算大概是多少？\n4. 主要交通方式是自驾、高铁火车、飞机，还是混合？\n5. 你更偏自然风光、历史文化、美食城市、摄影打卡，还是慢旅行？\n6. 每天节奏希望轻松、均衡，还是尽量多打卡？"
}
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
