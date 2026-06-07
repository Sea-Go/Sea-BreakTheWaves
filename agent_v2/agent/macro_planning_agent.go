package agent

import (
	"fmt"

	"agent_v2/tools"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/planner/builtin"
	"trpc.group/trpc-go/trpc-agent-go/team"
)

const macroPlanningInstruction = `
你是旅行宏观规划 Agent，只负责创建 TripPlan 和 Phase 层级。

## 必须使用的 ID（禁止自行生成）
- trip_plan_id: %s
- user_id: %s
- session_id: %s
- request_id: %s

## 允许的操作（仅以下 4 个工具）
1. create_trip_plan — 创建 TripPlan 根节点，必须使用上述 trip_plan_id
2. split_parent_node — 拆分 TripPlan → Phase（3-8 个 Phase）
3. get_weather_context — 查询区域气候数据
4. write_climate_data — 写入气候数据

## 禁止的操作
- 创建 Month/Week/Day 节点
- 查询 POI/Route
- 调用 amap/zhihu/bilibili 工具
- 生成完整旅行方案文本

## Phase 规划规则
- 规划 3-8 个 Phase，每个 Phase 包含 region, season, theme, dayCount
- Phase dayCount 之和必须等于 total_days
- Phase seq 从 1 开始连续编号
- 使用气候数据驱动 Phase 拆分
- Phase 必须严格围绕用户 destination_scope、must_visit 和 destination_anchors，不得引入明显无关城市
- 出发地只可作为路线起点，不可把无关远方城市扩展为目的地 Phase

## 输出格式
完成所有 Phase 创建后，只输出这一行（不要输出其他任何内容）：
TRIP_PLAN_CREATED:%s
`

// newMacroPlanningAgent creates a lightweight coordinator for macro planning only.
// It has only 4 graph tools + Dili360 sub-agent, replacing the heavy 24-tool coordinator.
func newMacroPlanningAgent(expectedTripPlanID, userID, sessionID, requestID string) agentcore.Agent {
	thinkingEnabled := true
	temperature := 0.1
	topP := 0.6

	alimodel := newModelForLevel("macro-planning-agent", ModelLevelMedium)

	macroPlanner := builtin.New(builtin.Options{
		ThinkingEnabled: &thinkingEnabled,
	})

	graphTools := tools.NewMacroPlanningGraphTools()

	instruction := fmt.Sprintf(macroPlanningInstruction,
		expectedTripPlanID, userID, sessionID, requestID, expectedTripPlanID)

	opts := []llmagent.Option{
		llmagent.WithModel(alimodel),
		llmagent.WithPlanner(macroPlanner),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			Temperature:     &temperature,
			TopP:            &topP,
			ThinkingEnabled: &thinkingEnabled,
		}),
		llmagent.WithTools(graphTools),
		llmagent.WithEnableParallelTools(true),
		llmagent.WithEnableContextCompaction(true),
		llmagent.WithContextCompactionKeepRecentRequests(2),
		llmagent.WithDescription("旅行宏观规划 Agent：只创建 TripPlan + Phase，禁止 Month/Week/Day 拆分。"),
		llmagent.WithInstruction(instruction),
	}

	coordinator := llmagent.New("macro-planning-agent", opts...)

	memberCfg := team.DefaultMemberToolConfig()
	memberCfg.StreamInner = true
	memberCfg.HistoryScope = team.HistoryScopeIsolated

	tm, err := team.New(
		coordinator,
		[]agentcore.Agent{
			Dili360Agent(),
		},
		team.WithDescription("宏观规划 Team：协调中国国家地理 Agent 获取区域背景，配合 4 个图工具完成 TripPlan + Phase 创建。"),
		team.WithMemberToolConfig(memberCfg),
	)
	if err != nil {
		log.Errorf("[macro-planning-agent] 创建 Team 失败: %v", err)
		return coordinator
	}

	return tm
}
