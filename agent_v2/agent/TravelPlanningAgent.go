package agent

import (
	"net/http"
	"time"

	"agent_v2/config"
	"agent_v2/graph"
	"agent_v2/tools"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/memory/extractor"
	memoryinmemory "trpc.group/trpc-go/trpc-agent-go/memory/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/planner/builtin"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/server/agui"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/session/summary"
	"trpc.group/trpc-go/trpc-agent-go/skill"
	"trpc.group/trpc-go/trpc-agent-go/team"
)

const graphInstruction = `
你是旅游规划协调者 Agent，使用 Neo4j 图数据库进行层级分解。

## 运行前提
需求准入已由上游 orchestrator 完成。上下文中已包含完整的 TravelRequirementSnapshot。
不要加载 travel-requirement-intake。不要向用户追问。

## MacroPlanning 阶段（仅允许以下操作）
1. 基于需求快照创建 TripPlan（create_trip_plan，绑定 userId/sessionId/requestId）
2. 规划 3-8 个 Phase（region, season, theme, dayCount）
3. 使用 get_weather_context + dili360-agent 做气候驱动拆分

## 禁止
Month/Week/Day 拆分、攻略采集、POI 验证、审查、逐日输出。
（这些属于 graph_splitting / day_expansion / review / final_output 阶段）

## 输出（必须只输出此 JSON，不含 markdown）
{
  "skill_name": "travel-planning-workflow",
  "stage": "macro_planning",
  "status": "completed",
  "trip_plan_id": "生成的TripPlan节点ID",
  "stop_workflow": false,
  "next_stage": "graph_splitting"
}

## 可用能力
天气/地理：get_weather_context, dili360-agent
图写入：create_trip_plan, split_parent_node, write_climate_data
Skills：travel-planning-workflow

## 核心规则
只输出单个 JSON object。TripPlan 绑定 userId/sessionId/requestId。
攻略内容只能是体验灵感层，不能当作地理事实。
`

// rateLimitHTTPClient 在遇到 429 限流时等待 60 秒后重试。
var rateLimitHTTPClient = &http.Client{
	Transport: newRetryAfter429Transport(2),
}

func TravelPlanningAgent() agentcore.Agent {
	// Check if Neo4j graph database is available
	graphClient := graph.GetClient()
	if graphClient != nil && graphClient.IsEnabled() {
		workflowAgent := NewGraphWorkflowAgent()
		if workflowAgent != nil {
			log.Infof("[travel-planning-agent] Neo4j 可用，使用混合图工作流 Agent")
			return workflowAgent
		}
	}

	// Fallback to traditional Team agent for non-graph mode
	// (same code as before)
	return newTravelPlanningTeam()
}

// newTravelPlanningTeam creates the traditional Team coordinator
// used for both non-graph mode and as the Phase 1 coordinator.
func newTravelPlanningTeam() agentcore.Agent {
	thinkingEnabled := true
	temperature := 0.1
	topP := 0.6

	alimodel := newModelForLevel("travel-planning-agent", ModelLevelMedium)

	travelPlanner := builtin.New(builtin.Options{
		ThinkingEnabled: &thinkingEnabled,
	})

	guideTools := append(tools.NewDefaultZhihuTools(), tools.NewDefaultBilibiliTools()...)
	graphTools := tools.NewDefaultGraphTools()
	if len(graphTools) > 0 {
		guideTools = append(guideTools, graphTools...)
	}

	skillRepo, err := skill.NewFSRepository("skills")
	if err != nil {
		log.Errorf("[travel-planning-agent] 加载 skills 仓库失败: %v", err)
	}

	opts := []llmagent.Option{
		llmagent.WithModel(alimodel),
		llmagent.WithPlanner(travelPlanner),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			Temperature:     &temperature,
			TopP:            &topP,
			ThinkingEnabled: &thinkingEnabled,
		}),
		llmagent.WithTools(guideTools),
		llmagent.WithEnableParallelTools(true),
		llmagent.WithEnableContextCompaction(true),
		llmagent.WithContextCompactionKeepRecentRequests(2),
		llmagent.WithContextCompactionToolResultMaxTokens(1024),
	}
	if skillRepo != nil {
		opts = append(opts,
			llmagent.WithSkills(skillRepo),
			llmagent.WithSkillToolProfile(llmagent.SkillToolProfileFull),
			llmagent.WithSkillLoadMode("turn"),
			llmagent.WithMaxLoadedSkills(5),
		)
	}

	opts = append(opts,
		llmagent.WithDescription(
			"一个旅游规划协调者 Agent，使用知乎/B站攻略素材、高德地图 Agent、中国国家地理 Agent、"+
				"5 个内容审查 Agent 和 5 个层级约束审查 Agent，配合 Neo4j 图数据库进行层级分解和增量生成，"+
				"支持从单日到一年级别的旅行规划。",
		),
		llmagent.WithInstruction(graphInstruction),
	)

	coordinator := llmagent.New("travel-planning-agent", opts...)

	memberCfg := team.DefaultMemberToolConfig()
	memberCfg.StreamInner = true
	memberCfg.HistoryScope = team.HistoryScopeIsolated
	memberCfg.SkipSummarization = false

	tm, err := team.New(
		coordinator,
		[]agentcore.Agent{
			AmapAgent(),
			// Day 级内容审查 Agent (L4)
			ReviewWorkflowAgent(),
			ReviewThinkingAgent(),
			ReviewContentAgent(),
			ReviewOutputAgent(),
			ReviewLazinessAgent(),
			// 层级约束审查 Agent (L0-L3, L5)
			ConstraintReviewAgent("trip"),
			ConstraintReviewAgent("phase"),
			ConstraintReviewAgent("month"),
			ConstraintReviewAgent("week"),
			ConstraintReviewAgent("poi"),
			Dili360Agent(),
		},
		team.WithDescription("旅游规划 Coordinator Team：协调攻略素材、高德地图事实验证、5 个内容审查和 5 个层级约束审查，配合 Neo4j 图数据库支持一年级规划。"),
		team.WithMemberToolConfig(memberCfg),
	)
	if err != nil {
		log.Errorf("[travel-planning-agent] 创建 Coordinator Team 失败: %v", err)
		return coordinator
	}

	return tm
}

func NewTravelPlanningAGUIHandler() (http.Handler, func(), error) {
	appName := config.Cfg.Agent.AppName + "travel-planning"

	// 为 summarizer 和 memory extractor 创建一个轻量模型实例。
	summaryModel := newSummaryModel("summary")

	// 短期记忆：session 服务 + summarizer，自动压缩长对话历史。
	sessSvc := sessioninmemory.NewSessionService(
		sessioninmemory.WithSessionEventLimit(500),
		sessioninmemory.WithSessionTTL(30*time.Minute),
		sessioninmemory.WithSummarizer(summary.NewSummarizer(summaryModel)),
		sessioninmemory.WithAsyncSummaryNum(4),
	)

	// 长期记忆：自动从对话中提取用户旅行偏好、常用出发地、节奏和交通偏好等。
	memSvc := memoryinmemory.NewMemoryService(
		memoryinmemory.WithMemoryLimit(100),
		memoryinmemory.WithExtractor(extractor.NewExtractor(summaryModel)),
		memoryinmemory.WithAsyncMemoryNum(2),
	)

	rn := runner.NewRunner(
		appName,
		TravelPlanningAgent(),
		runner.WithSessionService(sessSvc),
		runner.WithMemoryService(memSvc),
	)

	server, err := agui.New(
		rn,
		agui.WithPath("/agui"),
		agui.WithReasoningContentEnabled(true),
	)
	if err != nil {
		_ = rn.Close()
		return nil, nil, err
	}

	cleanup := func() {
		_ = memSvc.Close()
		_ = sessSvc.Close()
		_ = rn.Close()
	}

	return server.Handler(), cleanup, nil
}