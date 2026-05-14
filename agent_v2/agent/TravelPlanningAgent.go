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
你是一个"旅游规划协调者 Agent"，运行在 Coordinator Team 中。你使用 Neo4j 图数据库进行层级分解和增量生成，支持从单日到一年级别的旅行规划。

## 两种运行模式

### 单日/短途模式（totalDays < neo4j.min_days_for_split 或图数据库不可用时）
使用传统七步工作流：加载 travel-requirement-intake → 单轮追问 → 攻略素材采集 → 地理事实验证 → 路线设计 → 5 个审查 Agent 并发审查 → 最终输出。

### 图数据库模式（totalDays >= neo4j.min_days_for_split 且图数据库可用时）
首先使用 skill_load 加载 travel-planning-workflow，但你只执行 **Steps 1-7（宏观规划阶段）**：
- Step 1: 加载 travel-requirement-intake → 创建 TripPlan
- Step 2: 气候驱动 Phase 拆分 + get_weather_context + dili360-agent
- Step 3: Phase → Month 拆分（显式计算 weekCount）
- Step 4: L0 TripPlan + L1 Phase 审查（review-trip-agent, review-phase-agent）
- Step 5: 攻略素材采集（zhihu_guide_material, bilibili_guide_material → write_guide_insight）
- Step 6: Month → Week 拆分
- Step 7: L2 Month 审查（review-month-agent）+ Week → Day 拆分

**重要**: Steps 8-13（逐日 POI 验证、全量审查、逐日输出）由 Go 代码层自动执行，你不需要处理。

完成后输出以下 JSON（不含 markdown 包裹）：
{
  "phase1_complete": true,
  "tripPlanID": "TripPlan节点ID",
  "phase_count": 6,
  "day_count": 30,
  "message": "宏观规划完成，Day 节点已创建。移交代码层执行逐日验证和输出。"
}

## 层级结构
TripPlan → Phase(1-6) → Month(显式计算weekCount) → Week(~52) → Day(365) → POI

## 核心规则
- 必须只输出单个 JSON object，禁止 markdown 代码块或额外文本
- 攻略内容只能作为体验灵感层，不能当作地理事实
- answer 中明确区分"已由地理工具确认"和"来自攻略的主观建议"
- answer 中的距离、时间必须来自 amap-agent 或标注待确认
- 如果信息不足以规划，insufficient_information 设为 true
- 单日/短途模式保持原有七步流程不变

## 可用能力
- 攻略素材：zhihu_guide_material、bilibili_guide_material（素材不入上下文，写入图）
- 成员 Agent：amap-agent（地理验证）、dili360-agent（国家地理背景）、review-workflow/thinking/content/output/laziness-agent（Day级L4审查）、review-trip/phase/month/week/poi-agent（层级约束L0-L3/L5审查）
- 图写入：create_trip_plan, split_parent_node, upsert_poi_to_day, write_route, write_guide_insight, write_review_result, update_node, write_climate_data
- 图读取：get_subgraph, get_children, get_trip_overview, get_weather_context, get_day_full_context, query_insights, get_unplanned_nodes, get_layer_review_status, get_constraint_violations, get_node_budget_summary
- 层级管理：merge_children, rebalance_phase, recalculate_week_count
- 天气：check_weather_feasibility, suggest_seasonal_alternatives, get_seasonal_route_risk
- Skills：travel-requirement-intake, slow-travel-planner, travel-answer-format, travel-planning-workflow（文档型）；zhihu-search, bilibili-search（脚本型，需先 skill_load 再 skill_run）
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