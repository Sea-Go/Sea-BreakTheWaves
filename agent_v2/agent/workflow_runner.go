package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"agent_v2/config"
	"agent_v2/graph"
	"agent_v2/tools"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/planner/builtin"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/tool"

	"github.com/google/uuid"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// graphWorkflowAgent implements agentcore.Agent for the hybrid graph workflow.
// Orchestrator controls stage progression; coordinator handles macro planning.
type graphWorkflowAgent struct {
	name         string
	description  string
	coordinator  agentcore.Agent              // macro_planning 完整 coordinator（有图/地图/攻略工具）
	intakeAgent  agentcore.Agent              // intake/merge 精简 Agent（只有 skill 工具）
	graphClient  *graph.Client
	reviewAgents []reviewAgentSpec            // cached review agents for Phase 3
	orchestrator *TravelSkillOrchestrator     // skills 编排中央控制器
}

type reviewAgentSpec struct {
	name string
	ag   agentcore.Agent
}

func (a *graphWorkflowAgent) Info() agentcore.Info {
	return agentcore.Info{
		Name:        a.name,
		Description: a.description,
	}
}

func (a *graphWorkflowAgent) Tools() []tool.Tool {
	return a.coordinator.Tools()
}

func (a *graphWorkflowAgent) SubAgents() []agentcore.Agent {
	return a.coordinator.SubAgents()
}

func (a *graphWorkflowAgent) FindSubAgent(name string) agentcore.Agent {
	return a.coordinator.FindSubAgent(name)
}

func (a *graphWorkflowAgent) Run(ctx context.Context, invocation *agentcore.Invocation) (<-chan *event.Event, error) {
	outCh := make(chan *event.Event, 64)
	msg := invocation.Message.Content

	go func() {
		defer close(outCh)

		userID := "workflow-user"
		sessionID := fmt.Sprintf("workflow-%d", time.Now().UnixNano())

		// Extract from runtime state if available
		if invocation.Session != nil {
			userID = invocation.Session.UserID
			sessionID = invocation.Session.ID
		}

		// ================================================================
		// 1. Orchestrator 决定本轮执行哪个 stage
		// ================================================================
		result, err := a.orchestrator.Handle(ctx, userID, sessionID, msg, a.intakeAgent)
		if err != nil {
			a.emitErrorEvent(outCh, invocation, fmt.Sprintf("编排错误: %v", err))
			return
		}

		// ================================================================
		// 2. StopWorkflow → 直接返回追问/提示文本给用户
		// ================================================================
		if result.StopWorkflow {
			if result.Output != "" {
				a.emitTextEvent(outCh, invocation, result.Output)
			}
			return
		}

		// ================================================================
		// 3. 连续推进 stage，直到遇到 checkpoint 或未实现阶段
		// ================================================================
		const maxStageSteps = 5
		for step := 0; step < maxStageSteps; step++ {
			switch result.NextStage {
			case StageMacroPlanning:
				rt := a.orchestrator.LoadOrInitRuntime(userID, sessionID)
				if !rt.Requirement.RequirementReady {
					a.emitErrorEvent(outCh, invocation, "需求未完成，不能进入宏观规划")
					return
				}
				augmentedMsg := buildMacroPrompt(msg, rt)
				tripPlanID, err := a.runMacroPlanningOnly(ctx, userID, sessionID, augmentedMsg, outCh, invocation)
				if err != nil {
					a.emitErrorEvent(outCh, invocation, fmt.Sprintf("宏观规划失败: %v", err))
					return
				}
				a.orchestrator.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
					r.TripPlanID = tripPlanID
					r.CurrentStage = StageGraphSplitting
				})
				result.NextStage = StageGraphSplitting

			case StageGraphSplitting, StageDayExpansion, StageReview, StageFinalOutput:
				a.emitTextEvent(outCh, invocation,
					fmt.Sprintf("%s 阶段（待后续 PR 实现）", result.NextStage))
				return

			default:
				a.emitTextEvent(outCh, invocation,
					fmt.Sprintf("规划完成: stage=%s", result.NextStage))
				return
			}
		}

		a.emitErrorEvent(outCh, invocation, "达到最大连续推进步数，停止。")
	}()

	return outCh, nil
}

func (a *graphWorkflowAgent) emitTextEvent(outCh chan<- *event.Event, inv *agentcore.Invocation, text string) {
	msgID := fmt.Sprintf("wf-msg-%d", time.Now().UnixNano())
	outCh <- &event.Event{
		Response: &model.Response{
			ID:     msgID,
			Object: model.ObjectTypeChatCompletionChunk,
			Choices: []model.Choice{{
				Delta: model.Message{Content: text + "\n"},
			}},
		},
	}
}

func (a *graphWorkflowAgent) emitErrorEvent(outCh chan<- *event.Event, inv *agentcore.Invocation, errMsg string) {
	msgID := fmt.Sprintf("wf-err-%d", time.Now().UnixNano())
	outCh <- &event.Event{
		Response: &model.Response{
			ID:     msgID,
			Object: model.ObjectTypeChatCompletion,
			Choices: []model.Choice{{
				Message: model.Message{Role: model.RoleAssistant, Content: fmt.Sprintf("❌ %s", errMsg)},
			}},
		},
	}
}

func (a *graphWorkflowAgent) emitFinalEvent(outCh chan<- *event.Event, inv *agentcore.Invocation, content string) {
	msgID := fmt.Sprintf("wf-final-%d", time.Now().UnixNano())
	outCh <- &event.Event{
		Response: &model.Response{
			ID:     msgID,
			Object: model.ObjectTypeChatCompletion,
			Choices: []model.Choice{{
				Message: model.Message{Role: model.RoleAssistant, Content: content},
			}},
		},
	}
}

// --- Phase 1: Macro Planning ---

type macroPlanContext struct {
	tripPlanID string
	dayCount   int
	regions    []string
	rawAnswer  string // coordinator's planning output
}

// --- Macro Planning Only ---
// Replaces the old runPhase1 which also did Month/Week/Day splitting.
// Now only does TripPlan + Phase creation.

func (a *graphWorkflowAgent) runMacroPlanningOnly(
	ctx context.Context,
	userID, sessionID string,
	augmentedMsg string,
	outCh chan<- *event.Event,
	invocation *agentcore.Invocation,
) (string, error) {
	cfg := config.Cfg
	appName := cfg.Agent.AppName + "macro-planning"

	rn := runner.NewRunner(appName, a.coordinator)
	defer rn.Close()

	eventCh, err := rn.Run(ctx, userID, sessionID,
		model.NewUserMessage(augmentedMsg), agentcore.WithStream(true))
	if err != nil {
		return "", fmt.Errorf("run coordinator: %w", err)
	}

	var out strings.Builder
	for evt := range eventCh {
		if evt == nil || evt.Response == nil {
			continue
		}
		for _, c := range evt.Response.Choices {
			if c.Delta.Content != "" {
				out.WriteString(c.Delta.Content)
			}
			if c.Message.Content != "" && out.Len() == 0 {
				out.WriteString(c.Message.Content)
			}
		}
	}

	tripPlanID := extractTripPlanID(out.String())
	if tripPlanID == "" {
		log.Errorf("[workflow-runner] macro_planning: no trip_plan_id in output, len=%d", out.Len())
		return "", fmt.Errorf("coordinator 未返回 trip_plan_id，输出长度 %d 字符", out.Len())
	}

	overview, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
	if err != nil {
		return "", fmt.Errorf("get trip overview: %w", err)
	}
	if overview.TripPlan.SessionID != sessionID {
		log.Errorf("[workflow-runner] TripPlan %s session mismatch: expected %s, got %s",
			tripPlanID, sessionID, overview.TripPlan.SessionID)
		return "", fmt.Errorf("TripPlan %s 属于 session %s，不是当前 session %s",
			tripPlanID, overview.TripPlan.SessionID, sessionID)
	}

	// 只给用户阶段摘要，不流式透传 SkillResult JSON
	a.emitTextEvent(outCh, invocation,
		fmt.Sprintf("宏观规划完成 — 已创建 %d 个 Phase。接下来进入图拆分阶段。", len(overview.Phases)))

	log.Infof("[workflow-runner] macro_planning: tripPlanID=%s phases=%d session=%s",
		tripPlanID, len(overview.Phases), sessionID)
	return tripPlanID, nil
}

// buildMacroPrompt embeds the requirement snapshot into the macro planning prompt.
func buildMacroPrompt(originalMsg string, rt TravelSkillRuntime) string {
	reqJSON, _ := json.Marshal(rt.Requirement)
	return fmt.Sprintf(`## 已确认的旅行需求

%s

## 用户原始消息
%s

## 本阶段任务：MacroPlanning
只完成以下操作：
1. 基于需求创建 TripPlan（create_trip_plan，必须绑定 userId=%s, sessionId=%s, requestId=%s）
2. 规划 3-8 个 Phase（region, season, theme, dayCount, start/end anchor）

禁止：Month/Week/Day 拆分、攻略采集、POI 验证、审查、逐日输出。

输出 SkillResult JSON（含 trip_plan_id, next_stage=graph_splitting）。`,
		string(reqJSON), originalMsg, rt.UserID, rt.SessionID, rt.RunID)
}

func extractTripPlanID(output string) string {
	// 优先：解析 SkillResult JSON
	if r := parseSkillResult(output); r != nil && r.TripPlanID != "" {
		return r.TripPlanID
	}
	// 兼容旧格式：tripPlanID / trip_plan_id
	cleaned := extractJSONBlock(output)
	if cleaned != "" {
		var generic map[string]interface{}
		if json.Unmarshal([]byte(cleaned), &generic) == nil {
			if id, ok := generic["tripPlanID"].(string); ok && id != "" {
				return id
			}
			if id, ok := generic["trip_plan_id"].(string); ok && id != "" {
				return id
			}
		}
	}
	return ""
}

func (a *graphWorkflowAgent) ensureAllDaysCreated(ctx context.Context, tripPlanID string, overview *graph.TripOverview) error {
	totalDays := overview.TripPlan.TotalDays
	actualDays := len(overview.Days)
	missing := totalDays - actualDays

	if missing <= 0 {
		return nil
	}

	// Get Week IDs from overview
	weekIDs := extractWeekIDs(overview)
	if len(weekIDs) == 0 {
		return fmt.Errorf("no weeks found to distribute %d missing days", missing)
	}

	// Distribute missing days across weeks proportionally
	daysPerWeek := missing / len(weekIDs)
	remainder := missing % len(weekIDs)

	// Get current max dayIndex
	maxDayIdx := 0
	for _, d := range overview.Days {
		if di, ok := d["dayIndex"].(float64); ok && int(di) > maxDayIdx {
			maxDayIdx = int(di)
		}
	}

	startDate := overview.TripPlan.StartDate

	for i, weekID := range weekIDs {
		toCreate := daysPerWeek
		if i < remainder {
			toCreate++
		}
		if toCreate <= 0 {
			continue
		}

		// Get existing days for this week to check if we need more
		existingCount := 0
		for _, d := range overview.Days {
			if dWeek, ok := d["weekID"].(string); ok && dWeek == weekID {
				existingCount++
			}
		}

		// Create days for this week
		for j := 0; j < toCreate; j++ {
			maxDayIdx++
			dayID := fmt.Sprintf("day-auto-%d", maxDayIdx)
			// Calculate date: startDate + (maxDayIdx - 1) days
			date := calculateDate(startDate, maxDayIdx-1)
			theme := overview.TripPlan.TravelStyle
			if theme == "" {
				theme = "探索与发现"
			}

			dayProps := map[string]any{
				"id":          dayID,
				"date":        date,
				"dayIndex":    maxDayIdx,
				"theme":       theme,
				"intensity":   "medium",
				"status":      "outlined",
				"startPoint":  "",
				"primaryArea": "",
			}

			// Create Day node and link to Week
			_, err := a.graphClient.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
				_, err := tx.Run(ctx,
					`CREATE (d:Day {id: $id})
					 SET d += $props
					 WITH d
					 MATCH (w:Week {id: $weekID})
					 CREATE (w)-[:HAS_DAY]->(d)
					 RETURN d.id`,
					map[string]any{
						"id":     dayID,
						"props":  dayProps,
						"weekID": weekID,
					})
				return nil, err
			})
			if err != nil {
				log.Errorf("[workflow-runner] create auto day %s: %v", dayID, err)
			}
		}

		if toCreate > 0 {
			log.Infof("[workflow-runner]   Week %s: +%d days (had %d)", weekID, toCreate, existingCount)
		}
	}

	// Update TripPlan day count to match actual
	_, _ = a.graphClient.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx,
			`MATCH (tp:TripPlan {id: $tripPlanID})
			 SET tp.totalDays = $totalDays
			 RETURN tp`,
			map[string]any{"tripPlanID": tripPlanID, "totalDays": totalDays})
		return nil, err
	})

	log.Infof("[workflow-runner] ensureAllDaysCreated: filled %d missing days across %d weeks", missing, len(weekIDs))
	return nil
}

func calculateDate(startDate string, offset int) string {
	// Parse YYYY-MM-DD, add offset days, return YYYY-MM-DD
	t, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return startDate
	}
	return t.AddDate(0, 0, offset).Format("2006-01-02")
}

// --- Phase 2: Day-by-Day POI Verification ---

func (a *graphWorkflowAgent) runPhase2(ctx context.Context, tripPlanID string) error {
	overview, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
	if err != nil {
		return fmt.Errorf("get trip overview: %w", err)
	}

	totalDays := len(overview.Days)
	if totalDays == 0 {
		return fmt.Errorf("no days found for tripPlanID: %s", tripPlanID)
	}

	// Collect day IDs ordered by dayIndex
	dayIDs := make([]string, 0, totalDays)
	for _, d := range overview.Days {
		if id, ok := d["id"].(string); ok && id != "" {
			dayIDs = append(dayIDs, id)
		}
	}

	log.Infof("[workflow-runner] Phase 2: verifying POIs for %d days", len(dayIDs))

	for i, dayID := range dayIDs {
		log.Infof("[workflow-runner] Phase 2: day %d/%d (%s)", i+1, len(dayIDs), dayID)
		if err := a.verifyDayPOIs(ctx, dayID); err != nil {
			log.Errorf("[workflow-runner] Phase 2 day %s failed: %v", dayID, err)
			// Continue to next day
		}
	}

	return nil
}

func (a *graphWorkflowAgent) verifyDayPOIs(ctx context.Context, dayID string) error {
	subgraph, err := a.graphClient.GetDaySubgraph(ctx, dayID)
	if err != nil {
		return fmt.Errorf("get day subgraph: %w", err)
	}
	if subgraph == nil {
		return fmt.Errorf("day subgraph is nil for %s", dayID)
	}

	day := subgraph.Day

	// Build a structured prompt for amap-agent
	prompt := fmt.Sprintf(`请验证并完善以下旅行日的 POI 安排：

日期: %s
天数序号: Day %d
主题: %s
主要区域: %s
起点: %s

请执行以下验证：
1. 对该区域进行 POI 关键词搜索（景点、餐饮、住宿），找出 2-3 个最合适的 POI
2. 对每个 POI 调用 amap_geocode 获取精确坐标
3. 对 POI 之间的路线调用 amap_route_driving 获取距离和耗时
4. 返回结构化的 POI 数据和路线数据

输出格式（JSON）：
{
  "pois": [
    {
      "name": "POI名称",
      "type": "景点/餐饮/住宿/交通枢纽",
      "lat": 25.123,
      "lng": 100.456,
      "address": "详细地址",
      "district": "区县",
      "city": "城市",
      "visitOrder": 1,
      "duration": 120,
      "notes": "推荐理由",
      "estimatedCost": 50
    }
  ],
  "routes": [
    {
      "fromPOI": 0,
      "toPOI": 1,
      "transportMode": "driving",
      "distanceMeters": 5000,
      "durationMin": 15,
      "estimatedCost": 20
    }
  ]
}`, day.Date, day.DayIndex, day.Theme, day.PrimaryArea, day.StartPoint)

	// Run amap-agent
	amapResult, err := runAmapAgentStandalone(ctx, prompt)
	if err != nil {
		return fmt.Errorf("amap-agent failed: %w", err)
	}

	// Parse and write POIs
	pois, rawRoutes := parseAmapPOIResult(amapResult)

	// Pre-generate POI IDs before any writes, so routes can reference them correctly.
	for i := range pois {
		if pois[i].ID == "" {
			pois[i].ID = fmt.Sprintf("poi-%s", uuid.NewString())
		}
	}

	// Rebuild routes using the pre-generated POI IDs
	var routes []graph.RouteInput
	for _, rr := range rawRoutes {
		if rr.TransportMode == "" {
			continue
		}
		if rr.FromPOI >= len(pois) || rr.ToPOI >= len(pois) {
			continue
		}
		routes = append(routes, graph.RouteInput{
			FromPOIID:      pois[rr.FromPOI].ID,
			ToPOIID:        pois[rr.ToPOI].ID,
			TransportMode:  rr.TransportMode,
			DistanceMeters: rr.DistanceMeters,
			DurationMin:    rr.DurationMin,
			EstimatedCost:  rr.EstimatedCost,
			Notes:          rr.Notes,
		})
	}

	for _, poi := range pois {
		poiID, err := a.graphClient.UpsertPOIToDay(ctx, dayID, poi)
		if err != nil {
			log.Errorf("[workflow-runner] upsert POI %s to day %s: %v", poi.Name, dayID, err)
			continue
		}
		log.Infof("[workflow-runner]   POI: %s (%s) id=%s", poi.Name, poi.Type, poiID)

		// Write routes for this POI
		for _, route := range routes {
			if route.FromPOIID == poiID || route.ToPOIID == poiID {
				if err := a.graphClient.WriteRoute(ctx, route); err != nil {
					log.Errorf("[workflow-runner] write route: %v", err)
				}
			}
		}
	}

	// Mark day as verified
	_ = a.graphClient.UpdateNode(ctx, dayID, map[string]any{"status": "verified"})

	return nil
}

type parsedAmapResult struct {
	POIs      []graph.POIInput `json:"pois"`
	RawRoutes []rawAmapRoute   `json:"routes"`
}

type rawAmapRoute struct {
	FromPOI        int     `json:"fromPOI"`
	ToPOI          int     `json:"toPOI"`
	TransportMode  string  `json:"transportMode"`
	DistanceMeters float64 `json:"distanceMeters"`
	DurationMin    float64 `json:"durationMin"`
	EstimatedCost  float64 `json:"estimatedCost"`
	Notes          string  `json:"notes"`
}

func parseAmapPOIResult(result string) ([]graph.POIInput, []rawAmapRoute) {
	var parsed parsedAmapResult
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		cleaned := extractJSONBlock(result)
		if cleaned != "" {
			json.Unmarshal([]byte(cleaned), &parsed)
		}
	}

	var pois []graph.POIInput
	for _, poi := range parsed.POIs {
		if poi.Name != "" {
			pois = append(pois, poi)
		}
	}

	return pois, parsed.RawRoutes
}

func extractJSONBlock(text string) string {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		return text[start : end+1]
	}
	return ""
}

// amapPOIVerifyInstruction is a focused version of the AmapAgent instruction
// for Phase 2 POI verification. It uses the same planner-based approach.
const amapPOIVerifyInstruction = `
你是一个"高德地图 POI 验证 Agent"，使用 planner 模式逐步推理，主动调用工具验证 POI 信息后返回结构化 JSON。

## Planner 流程 — 严格执行以下四步

### 第一步：理解问题
- 读取输入中的日期、区域、主题信息
- 识别需要验证的城市/区域名称

### 第二步：信息缺口分析 & 工具选择
思考"要为这一天提供 2-3 个合适的 POI，我需要什么信息？"

| 需求场景 | 调用工具 |
|---------|---------|
| 搜索某区域的景点/餐饮/住宿 POI | amap_poi_keyword_search |
| 获取 POI 精确坐标和地址 | amap_geocode |
| 计算 POI 之间的驾车距离和时间 | amap_route_driving |

### 第三步：执行工具调用
1. 先用 amap_poi_keyword_search 按城市+关键词搜索 2-3 个 POI
2. 对每个 POI 调用 amap_geocode 获取精确坐标
3. 对相邻 POI 调用 amap_route_driving 获取路线数据
4. **关键**：每次只调用一个工具，等待结果后再决定下一步

### 第四步：按格式输出 JSON

基于工具返回的真实数据，输出：
{
  "pois": [
    {
      "name": "POI名称（来自高德API）",
      "type": "景点/餐饮/住宿/交通枢纽",
      "lat": 25.123456,
      "lng": 100.456789,
      "address": "详细地址",
      "district": "区县",
      "city": "城市",
      "visitOrder": 1,
      "duration": 120,
      "notes": "基于高德API数据的特点描述",
      "estimatedCost": 50
    }
  ],
  "routes": [
    {
      "fromPOI": 0,
      "toPOI": 1,
      "transportMode": "driving",
      "distanceMeters": 5000,
      "durationMin": 15
    }
  ]
}

=== 严格规则 ===
- 必须调用工具获取真实数据，禁止凭空编造坐标和地址
- 必须只输出单个 JSON object，禁止 markdown 代码块或额外文本
- 如果工具调用失败（如 QPS 限速），稍等后重试一次
`

func runAmapAgentStandalone(ctx context.Context, prompt string) (string, error) {
	cfg := config.Cfg
	appName := cfg.Agent.AppName + "amap-standalone"

	thinkingEnabled := true
	temperature := 0.0
	topP := 0.3

	alimodel := newModelForLevel("amap-agent-phase2", ModelLevelMedium)
	amapTools := tools.NewDefaultAmapTools()
	amapPlanner := builtin.New(builtin.Options{ThinkingEnabled: &thinkingEnabled})

	amapAgent := llmagent.New("amap-agent-phase2",
		llmagent.WithModel(alimodel),
		llmagent.WithPlanner(amapPlanner),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			Temperature: &temperature,
			TopP:        &topP,
		}),
		llmagent.WithTools(amapTools),
		llmagent.WithInstruction(amapPOIVerifyInstruction),
	)

	rn := runner.NewRunner(appName, amapAgent)
	defer rn.Close()

	eventCh, err := rn.Run(ctx, "amap-phase2", fmt.Sprintf("amap-%d", time.Now().UnixNano()),
		model.NewUserMessage(prompt), agentcore.WithStream(true))
	if err != nil {
		return "", err
	}

	var result strings.Builder
	for evt := range eventCh {
		if evt == nil || evt.Response == nil {
			continue
		}
		for _, choice := range evt.Response.Choices {
			if choice.Delta.Content != "" {
				result.WriteString(choice.Delta.Content)
			}
			if choice.Message.Content != "" && result.Len() == 0 {
				result.WriteString(choice.Message.Content)
			}
		}
	}

	return result.String(), nil
}

// --- Phase 3: Full Review ---

func (a *graphWorkflowAgent) runPhase3(ctx context.Context, tripPlanID string) error {
	overview, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
	if err != nil {
		return fmt.Errorf("get trip overview: %w", err)
	}

	dayIDs := extractDayIDs(overview)
	log.Infof("[workflow-runner] Phase 3: reviewing %d days", len(dayIDs))

	for i, dayID := range dayIDs {
		log.Infof("[workflow-runner] Phase 3: day %d/%d (%s)", i+1, len(dayIDs), dayID)

		subgraph, err := a.graphClient.GetDaySubgraph(ctx, dayID)
		if err != nil || subgraph == nil {
			log.Errorf("[workflow-runner] Phase 3: get day subgraph %s failed: %v", dayID, err)
			continue
		}

		// L5: POI-level review
		for _, poi := range subgraph.POIs {
			poiReview := runPOIReview(ctx, subgraph, poi)
			if poiReview != nil {
				_, _ = a.graphClient.WriteReviewResult(ctx, poi.ID, *poiReview)
			}
		}

		// L4: Day-level content review — run 5 agents in parallel
		dayReviewResults := a.runDayContentReviews(ctx, subgraph)
		for _, r := range dayReviewResults {
			_, _ = a.graphClient.WriteReviewResult(ctx, dayID, r)
		}
	}

	// L3: Week-level review
	weekIDs := extractWeekIDs(overview)
	for _, weekID := range weekIDs {
		weekReview := runWeekReview(ctx, weekID)
		if weekReview != nil {
			_, _ = a.graphClient.WriteReviewResult(ctx, weekID, *weekReview)
		}
	}

	return nil
}

func runPOIReview(ctx context.Context, subgraph *graph.DaySubgraph, poi graph.POINode) *graph.ReviewInput {
	// Use review-poi-agent for constraint-based POI review
	agent := ConstraintReviewAgent("poi")
	prompt := fmt.Sprintf(`请审查以下 POI 的约束合规性：

POI: %s
类型: %s
坐标: (%.6f, %.6f)
城市: %s
费用: %.2f
已验证来源: %s
是否雨天备选: %v

请加载 review-poi skill 并输出审查结果 JSON。`, poi.Name, poi.Type, poi.Lat, poi.Lng, poi.City, poi.EstimatedCost, poi.VerifiedBy, poi.IsRainyDayBackup)

	output, err := runReviewAgentStandalone(ctx, agent, "review-poi-"+poi.ID, prompt)
	if err != nil || output == "" {
		return nil
	}

	return parseReviewOutput(output)
}

func (a *graphWorkflowAgent) runDayContentReviews(ctx context.Context, subgraph *graph.DaySubgraph) []graph.ReviewInput {
	dayData, _ := json.Marshal(subgraph)
	dayPrompt := fmt.Sprintf(`请审查以下旅行日的规划质量：

日期: %s
主题: %s
区域: %s
POI 数量: %d

完整子图数据:
%s

请加载对应的 review skill 并输出审查结果 JSON。`, subgraph.Day.Date, subgraph.Day.Theme, subgraph.Day.PrimaryArea, len(subgraph.POIs), string(dayData))

	var wg sync.WaitGroup
	results := make(chan graph.ReviewInput, len(a.reviewAgents))

	for _, ra := range a.reviewAgents {
		wg.Add(1)
		go func(name string, ag agentcore.Agent) {
			defer wg.Done()
			output, err := runReviewAgentStandalone(ctx, ag,
				fmt.Sprintf("review-%s-%s", name, subgraph.Day.Date), dayPrompt)
			if err == nil && output != "" {
				if r := parseReviewOutput(output); r != nil {
					results <- *r
				}
			}
		}(ra.name, ra.ag)
	}

	wg.Wait()
	close(results)

	var reviews []graph.ReviewInput
	for r := range results {
		reviews = append(reviews, r)
	}
	return reviews
}

func runWeekReview(ctx context.Context, weekID string) *graph.ReviewInput {
	agent := ConstraintReviewAgent("week")
	prompt := fmt.Sprintf(`请审查 Week 节点 %s 的约束合规性（休息日底线、转移日上限、高强度日上限、POI 密度）。
加载 review-week skill 并输出审查结果 JSON。`, weekID)

	output, err := runReviewAgentStandalone(ctx, agent, "review-week-"+weekID, prompt)
	if err != nil || output == "" {
		return nil
	}
	return parseReviewOutput(output)
}

func runReviewAgentStandalone(ctx context.Context, ag agentcore.Agent, sessionID, prompt string) (string, error) {
	cfg := config.Cfg
	appName := cfg.Agent.AppName + "review-standalone"

	rn := runner.NewRunner(appName, ag)
	defer rn.Close()

	eventCh, err := rn.Run(ctx, "review-system", sessionID,
		model.NewUserMessage(prompt), agentcore.WithStream(true))
	if err != nil {
		return "", err
	}

	var result strings.Builder
	for evt := range eventCh {
		if evt == nil || evt.Response == nil {
			continue
		}
		for _, choice := range evt.Response.Choices {
			if choice.Delta.Content != "" {
				result.WriteString(choice.Delta.Content)
			}
			if choice.Message.Content != "" && result.Len() == 0 {
				result.WriteString(choice.Message.Content)
			}
		}
	}
	return result.String(), nil
}

func parseReviewOutput(output string) *graph.ReviewInput {
	cleaned := extractJSONBlock(output)
	if cleaned == "" {
		return nil
	}
	var r graph.ReviewInput
	if err := json.Unmarshal([]byte(cleaned), &r); err != nil {
		return nil
	}
	if r.Level == "" || r.Dimension == "" {
		return nil
	}
	return &r
}

// --- Phase 4: Global Checks ---

type globalNotes struct {
	weatherNotes   []string
	seasonalEvents []string
	reviewSummary  string
}

func (a *graphWorkflowAgent) runPhase4(ctx context.Context, tripPlanID string, mctx *macroPlanContext) *globalNotes {
	notes := &globalNotes{}

	// Constraint violations
	violations, err := a.graphClient.GetConstraintViolations(ctx, tripPlanID)
	if err != nil {
		log.Errorf("[workflow-runner] Phase 4: get constraint violations: %v", err)
	} else {
		log.Infof("[workflow-runner] Phase 4: found %d constraint violations", len(violations))
		if len(violations) == 0 {
			notes.reviewSummary = "所有约束违规已修复，六级审查通过。"
		} else {
			notes.reviewSummary = fmt.Sprintf("发现 %d 条约束违规待处理。", len(violations))
		}
	}

	// Weather context for each region, using actual month from Phase start date
		overview, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
		if err == nil {
			for _, p := range overview.Phases {
				region := getStr(p, "region")
				startDate := getStr(p, "startDate")
				if region == "" || startDate == "" {
					continue
				}
				month := monthFromDate(startDate)
				wc, wcErr := a.graphClient.GetWeatherContext(ctx, region, month)
				if wcErr != nil || wc == nil {
					continue
				}
				for _, cd := range wc.ClimateData {
					notes.weatherNotes = append(notes.weatherNotes,
						fmt.Sprintf("%s %d月: %.0f-%.0f°C, 极端天气风险=%s",
							region, cd.Month, cd.AvgLowTemp, cd.AvgHighTemp, cd.ExtremeWeatherRisk))
				}
				for _, c := range wc.Constraints {
					notes.weatherNotes = append(notes.weatherNotes,
						fmt.Sprintf("%s: %s(%s) — %s", region, c.ConstraintType, c.Severity, c.Description))
				}
				for _, se := range wc.SeasonalEvents {
					notes.seasonalEvents = append(notes.seasonalEvents,
						fmt.Sprintf("%s (%d-%d月): %s", se.Name, se.StartMonth, se.EndMonth, se.Description))
				}
			}
		}
	return notes
}

// --- Phase 5: Day-by-Day Output ---

func (a *graphWorkflowAgent) runPhase5(ctx context.Context, tripPlanID string, notes *globalNotes, mctx *macroPlanContext) (string, error) {
	overview, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
	if err != nil {
		return "", fmt.Errorf("get trip overview: %w", err)
	}

	// Build Phase→Day hierarchy
	type phaseInfo struct {
		name string
		seq  int
		days []string // day IDs
	}

	// Map day IDs to phases via Month→Week→Day traversal
	phaseDays := make(map[int]*phaseInfo)
	for _, p := range overview.Phases {
		seq := int(getFloat(p, "seq"))
		name := getStr(p, "name")
		phaseDays[seq] = &phaseInfo{name: name, seq: seq}
	}

	// Get all days with their context
	dayIDs := extractDayIDs(overview)
	totalDays := len(dayIDs)

	// Assign days to phases (simplified: use day index)
	// Re-query to get proper phase grouping
	type dayWithPhase struct {
		id    string
		phase int
		date  string
	}

	var days []dayWithPhase
	dayIndex := 1
	for _, pid := range []int{1, 2, 3, 4, 5, 6} {
		if pi, ok := phaseDays[pid]; ok {
			pi.days = nil
			// Count days in this phase based on overview
			for _, d := range overview.Days {
				dID := getStr(d, "id")
				dIdx := int(getFloat(d, "dayIndex"))
				dDate := getStr(d, "date")
				// Simple distribution: allocate days proportionally
				_ = dIdx
				days = append(days, dayWithPhase{id: dID, phase: pid, date: dDate})
			}
		}
	}
	_ = dayIndex

	// Actually, let's query Neo4j properly for the hierarchy
	log.Infof("[workflow-runner] Phase 5: generating output for %d days", totalDays)

	var answerBuilder strings.Builder
	var allWeatherNotes []string
	var allSeasonalEvents []string

	processedDays := 0
	for _, day := range overview.Days {
		dayID := getStr(day, "id")
		if dayID == "" {
			continue
		}

		processedDays++
		log.Infof("[workflow-runner] Phase 5: day %d/%d (%s)", processedDays, totalDays, dayID)

		// 1. Load full day context from Neo4j
		subgraph, err := a.graphClient.GetDaySubgraph(ctx, dayID)
		if err != nil || subgraph == nil {
			log.Errorf("[workflow-runner] Phase 5: get day subgraph %s failed: %v", dayID, err)
			// Generate placeholder
			answerBuilder.WriteString(fmt.Sprintf("### Day %d: %s\n\n数据加载失败，请重试。\n\n",
				int(getFloat(day, "dayIndex")), getStr(day, "date")))
			continue
		}

		// 2. Format as structured prompt for DayOutputAgent
		dayData := formatDayDataForOutput(subgraph)

		// 3. Run DayOutputAgent
		dayText, err := a.runDayOutputAgent(ctx, dayData, processedDays)
		if err != nil {
			log.Errorf("[workflow-runner] Phase 5: day output agent for %s failed: %v", dayID, err)
			answerBuilder.WriteString(fmt.Sprintf("### Day %d: %s\n\n生成失败: %v\n\n",
				subgraph.Day.DayIndex, subgraph.Day.Date, err))
			continue
		}

		// 4. Accumulate
		answerBuilder.WriteString(dayText)
		answerBuilder.WriteString("\n")

		// Collect weather and seasonal events from subgraph
		for _, c := range subgraph.Climate {
			allWeatherNotes = append(allWeatherNotes,
				fmt.Sprintf("%s %d月: %.0f-%.0f°C, 极端天气风险=%s",
					subgraph.Day.PrimaryArea, 3, c.AvgLowTemp, c.AvgHighTemp, c.ExtremeWeatherRisk))
		}
	}

	// Assemble final JSON
	finalJSON := assembleFinalJSON(answerBuilder.String(), notes, allWeatherNotes, allSeasonalEvents)
	log.Infof("[workflow-runner] Phase 5 complete: answer=%d chars", answerBuilder.Len())

	return finalJSON, nil
}

func formatDayDataForOutput(subgraph *graph.DaySubgraph) string {
	type dayOutputData struct {
		Date        string                   `json:"date"`
		DayIndex    int                      `json:"dayIndex"`
		Theme       string                   `json:"theme"`
		Intensity   string                   `json:"intensity"`
		StartPoint  string                   `json:"startPoint"`
		PrimaryArea string                   `json:"primaryArea"`
		POIs        []graph.POINode          `json:"pois"`
		Routes      []map[string]any         `json:"routes"`
		Insights    []graph.GuideInsightNode `json:"insights"`
		Climate     []graph.ClimateDataNode  `json:"climate"`
		Reviews     []graph.ReviewResultNode `json:"reviews"`
	}

	data := dayOutputData{
		Date:        subgraph.Day.Date,
		DayIndex:    subgraph.Day.DayIndex,
		Theme:       subgraph.Day.Theme,
		Intensity:   subgraph.Day.Intensity,
		StartPoint:  subgraph.Day.StartPoint,
		PrimaryArea: subgraph.Day.PrimaryArea,
		POIs:        subgraph.POIs,
		Routes:      subgraph.Routes,
		Insights:    subgraph.Insights,
		Climate:     subgraph.Climate,
		Reviews:     subgraph.Reviews,
	}

	b, _ := json.Marshal(data)
	return string(b)
}

func (a *graphWorkflowAgent) runDayOutputAgent(ctx context.Context, dayDataJSON string, dayNum int) (string, error) {
	cfg := config.Cfg
	appName := cfg.Agent.AppName + "day-output"

	thinkingEnabled := true
	temperature := 0.3
	topP := 0.7

	alimodel := newModelForLevel("day-output-agent", ModelLevelMedium)
	dayOutputPlanner := builtin.New(builtin.Options{ThinkingEnabled: &thinkingEnabled})

	prompt := fmt.Sprintf(`## 任务：为以下旅行日生成详细逐日文本

### 输入数据（JSON）：%s

### 输出要求

基于输入数据，生成该天的详细文字描述。**必须包含**：

1. **日期 + 主题 + 强度**：如"Day 5: 3月5日 — 大理苍山徒步（强度：中等）"

2. **天气概况**：从 climate 中提取温度、降水、极端天气风险

3. **当日行程表格**：
| 顺序 | 时间 | POI | 类型 | 停留 | 说明 |
|------|------|-----|------|------|------|

4. **每个 POI 的详细描述**（六要素）：
   - POI名称（类型）
   - 地址、坐标 — 高德已确认
   - 推荐理由（从 insights 中提取，标注"攻略/主观信号"）
   - 预计游览时间
   - 费用预估
   - 攻略信号（从 insights 中提取）
   - 交通备注（从 routes 中提取距离和方式）

5. **路线衔接**：POI 之间的交通方式、距离、耗时

6. **天气注意事项**

7. **本日小结**：当日行程节奏、亮点

**详细度底线**：每天至少 200 字。

**输出格式**：直接输出 Markdown，不要包裹在 JSON 中。`, dayDataJSON)

	dayAgent := llmagent.New("day-output",
		llmagent.WithModel(alimodel),
		llmagent.WithPlanner(dayOutputPlanner),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			Temperature: &temperature,
			TopP:        &topP,
		}),
		llmagent.WithInstruction(`你是一个旅行日文本生成 Agent。
根据输入的 Day 子图数据（POI、路线、攻略洞察、天气、审查结果），
生成该天的详细 Markdown 文本。必须包含 POI 六要素、天气信息和攻略信号。
直接输出 Markdown，不要输出 JSON 包装。`),
	)

	rn := runner.NewRunner(appName, dayAgent)
	defer rn.Close()

	eventCh, err := rn.Run(ctx, "day-output-system",
		fmt.Sprintf("day-%d-%d", dayNum, time.Now().UnixNano()),
		model.NewUserMessage(prompt),
		agentcore.WithStream(true))
	if err != nil {
		return "", fmt.Errorf("run day output agent: %w", err)
	}

	var result strings.Builder
	for evt := range eventCh {
		if evt == nil || evt.Response == nil {
			continue
		}
		for _, choice := range evt.Response.Choices {
			if choice.Delta.Content != "" {
				result.WriteString(choice.Delta.Content)
			}
			if choice.Message.Content != "" && result.Len() == 0 {
				result.WriteString(choice.Message.Content)
			}
		}
	}

	return result.String(), nil
}

func assembleFinalJSON(answer string, notes *globalNotes, weatherNotes, seasonalEvents []string) string {
	allWeather := append(notes.weatherNotes, weatherNotes...)
	allSeasonal := append(notes.seasonalEvents, seasonalEvents...)

	final := map[string]any{
		"answer":                    answer,
		"weather_notes":             deduplicate(allWeather),
		"seasonal_events":           deduplicate(allSeasonal),
		"constraint_review_summary": notes.reviewSummary,
		"insufficient_information":  false,
	}

	b, _ := json.Marshal(final)
	return string(b)
}

// --- Helpers ---

func extractDayIDs(overview *graph.TripOverview) []string {
	var ids []string
	for _, d := range overview.Days {
		if id, ok := d["id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func extractWeekIDs(overview *graph.TripOverview) []string {
	var ids []string
	for _, w := range overview.Weeks {
		if id, ok := w["id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func getStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getFloat(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case float64:
			return val
		case int64:
			return float64(val)
		case int:
			return float64(val)
		}
	}
	return 0
}

func monthFromDate(dateStr string) int {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return 1
	}
	return int(t.Month())
}

func deduplicate(items []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, item := range items {
		if item != "" && !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

// NewGraphWorkflowAgent creates a hybrid workflow agent that uses the LLM coordinator
// for macro planning (Steps 1-7) and Go-level loops for micro execution (Steps 8-13).
func NewGraphWorkflowAgent() agentcore.Agent {
	graphClient := graph.GetClient()
	if graphClient == nil || !graphClient.IsEnabled() {
		return nil
	}

	// Create the coordinator Team (macro planning only)
	coordinator := newTravelPlanningTeam()

	// Create intake-only agent (requirement analysis, no business tools)
	intakeAgent := newIntakeOnlyAgent()

	// Create skill orchestrator
	orchestrator := NewTravelSkillOrchestrator()

	// Pre-create review agents for Phase 3 reuse
	reviewAgents := []reviewAgentSpec{
		{"workflow", ReviewWorkflowAgent()},
		{"thinking", ReviewThinkingAgent()},
		{"content", ReviewContentAgent()},
		{"output", ReviewOutputAgent()},
		{"laziness", ReviewLazinessAgent()},
	}

	return &graphWorkflowAgent{
		name:         "graph-workflow-agent",
		description:  "混合图工作流 Agent：Orchestrator 编排 skills + LLM 宏观规划 + Go 层逐日执行",
		coordinator:  coordinator,
		intakeAgent:  intakeAgent,
		graphClient:  graphClient,
		reviewAgents: reviewAgents,
		orchestrator: orchestrator,
	}
}
