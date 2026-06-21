package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	coordinator  agentcore.Agent // macro_planning 完整 coordinator（有图/地图/攻略工具）
	intakeAgent  agentcore.Agent // intake/merge 精简 Agent（只有 skill 工具）
	graphClient  *graph.Client
	reviewAgents []reviewAgentSpec        // cached review agents for Phase 3
	orchestrator *TravelSkillOrchestrator // skills 编排中央控制器
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
		trace := traceEmitterFromInvocation(invocation)

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
			if trace != nil {
				trace.EmitStage(ctx, string(result.NextStage), "waiting", "等待补充信息", "当前信息还不足以进入地图规划，先在对话中补齐关键条件。")
			}
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
				if trace != nil {
					trace.EmitStage(ctx, string(StageMacroPlanning), "running", "建立大规划", "需求已经足够，开始建立区域级大方向。")
					emitRequirementMapEvents(ctx, trace, rt.Requirement)
				}
				expectedTripPlanID := uuid.NewString()
				augmentedMsg := buildMacroPrompt(msg, rt, expectedTripPlanID)
				tripPlanID, err := a.runMacroPlanningOnly(ctx, userID, sessionID, rt.RunID, expectedTripPlanID, augmentedMsg, outCh, invocation)
				if err != nil {
					a.emitErrorEvent(outCh, invocation, fmt.Sprintf("宏观规划失败: %v", err))
					return
				}
				a.orchestrator.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
					r.TripPlanID = tripPlanID
					r.CurrentStage = StageGraphSplitting
				})
				result.NextStage = StageGraphSplitting

			case StageGraphSplitting:
				rt2 := a.orchestrator.LoadOrInitRuntime(userID, sessionID)
				if rt2.TripPlanID == "" {
					a.emitErrorEvent(outCh, invocation, "TripPlanID 未找到，无法拆分")
					return
				}
				if trace != nil {
					trace.EmitStage(ctx, string(StageGraphSplitting), "running", "拆分小规划", "开始把大规划拆成月、周、日，地图会保留上层方向并逐步展开。")
				}
				if err := a.runGraphSplitting(ctx, rt2.TripPlanID, rt2.Requirement); err != nil {
					a.emitErrorEvent(outCh, invocation, fmt.Sprintf("图拆分失败: %v", err))
					return
				}
				if trace != nil {
					if overview, err := a.graphClient.GetTripOverview(ctx, rt2.TripPlanID); err == nil {
						emitGraphSplittingMapEvents(ctx, trace, overview)
						emitGuideEvidenceForTrip(ctx, trace, a.graphClient, rt2.TripPlanID, overview)
					}
				}
				a.orchestrator.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
					r.CurrentStage = StageDayExpansion
				})
				a.emitTextEvent(outCh, invocation, "图拆分完成，已创建 Month/Week/Day 层级。开始逐日验证地点和路线...")
				result.NextStage = StageDayExpansion

			case StageDayExpansion:
				rtDay := a.orchestrator.LoadOrInitRuntime(userID, sessionID)
				if rtDay.TripPlanID == "" {
					a.emitErrorEvent(outCh, invocation, "TripPlanID 未找到，无法展开日级规划")
					return
				}
				if trace != nil {
					trace.EmitStage(ctx, string(StageDayExpansion), "running", "展开日级地点和路线", "开始逐日验证 POI、坐标和路线，地图会实时加入真实地点和路线。")
				}
				if err := a.runPhase2(ctx, rtDay.TripPlanID, trace); err != nil {
					a.emitErrorEvent(outCh, invocation, fmt.Sprintf("日级地点和路线展开失败: %v", err))
					return
				}
				a.orchestrator.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
					r.CurrentStage = StageReview
				})
				if trace != nil {
					trace.EmitStage(ctx, string(StageDayExpansion), "completed", "地点和路线已展开", "已完成日级地点与路线验证，准备进入审核。")
				}
				a.emitTextEvent(outCh, invocation, "日级地点和路线已展开，开始审核规划质量...")
				result.NextStage = StageReview

			case StageReview:
				rtReview := a.orchestrator.LoadOrInitRuntime(userID, sessionID)
				if rtReview.TripPlanID == "" {
					a.emitErrorEvent(outCh, invocation, "TripPlanID 未找到，无法审核")
					return
				}
				if trace != nil {
					trace.EmitStage(ctx, string(StageReview), "running", "审核规划质量", "开始检查地点、日程、路线和约束是否合理，审核结果会进入地图证据层。")
				}
				if err := a.runPhase3(ctx, rtReview.TripPlanID, trace, rtReview.Requirement); err != nil {
					a.emitErrorEvent(outCh, invocation, fmt.Sprintf("审核失败: %v", err))
					return
				}
				a.orchestrator.updateRuntime(userID, sessionID, func(r *TravelSkillRuntime) {
					r.CurrentStage = StageFinalOutput
				})
				if trace != nil {
					trace.EmitStage(ctx, string(StageReview), "completed", "审核完成", "审核结果已经写入地图证据层，开始生成最终方案。")
				}
				a.emitTextEvent(outCh, invocation, "审核完成，开始生成最终方案...")
				result.NextStage = StageFinalOutput

			case StageFinalOutput:
				rt3 := a.orchestrator.LoadOrInitRuntime(userID, sessionID)
				if rt3.TripPlanID == "" {
					a.emitErrorEvent(outCh, invocation, "TripPlanID 未找到")
					return
				}
				if trace != nil {
					trace.EmitStage(ctx, string(StageFinalOutput), "running", "生成最终方案", "地图结构已经建立，开始组织最终旅行方案文本。")
				}
				// Use background context for final output to avoid request timeout
				bgCtx := context.Background()
				finalJSON, err := a.runFinalOutput(bgCtx, rt3.TripPlanID, outCh, invocation)
				if err != nil {
					a.emitErrorEvent(outCh, invocation, fmt.Sprintf("最终输出失败: %v", err))
					return
				}
				a.emitFinalEvent(outCh, invocation, finalJSON)
				log.Infof("[workflow-runner] final output complete: %d chars", len(finalJSON))
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
	if trace := traceEmitterFromInvocation(inv); trace != nil {
		trace.EmitError(context.Background(), errMsg)
	}
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
	userID, sessionID, requestID string,
	expectedTripPlanID string,
	augmentedMsg string,
	outCh chan<- *event.Event,
	invocation *agentcore.Invocation,
) (string, error) {
	if trace := traceEmitterFromInvocation(invocation); trace != nil {
		trace.EmitStage(ctx, string(StageMacroPlanning), "running", "生成阶段节点", "正在把长周期需求压缩成可在地图上逐层展开的阶段。")
	}
	cfg := config.Cfg
	appName := cfg.Agent.AppName + "macro-planning"

	// Use lightweight macro planning agent (4 tools + Dili360) instead of heavy coordinator (24 tools + 12 sub-agents)
	macroAgent := newMacroPlanningAgent(expectedTripPlanID, userID, sessionID, requestID)

	rn := runner.NewRunner(appName, macroAgent)
	defer rn.Close()

	eventCh, err := rn.Run(ctx, userID, sessionID,
		model.NewUserMessage(augmentedMsg), agentcore.WithStream(true))
	if err != nil {
		return "", fmt.Errorf("run macro planning agent: %w", err)
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

	// Database truth first: check if TripPlan exists in Neo4j
	tp, err := a.graphClient.FindTripPlanByID(ctx, expectedTripPlanID)
	if err != nil {
		log.Errorf("[workflow-runner] macro_planning: FindTripPlanByID(%s) error: %v", expectedTripPlanID, err)
	}
	if tp != nil {
		// Found in Neo4j — validate ownership
		if tp.UserID != userID || tp.SessionID != sessionID || tp.RequestID != requestID {
			errMsg := fmt.Sprintf("TripPlan %s ownership mismatch: expected user=%s session=%s request=%s, got user=%s session=%s request=%s",
				expectedTripPlanID, userID, sessionID, requestID, tp.UserID, tp.SessionID, tp.RequestID)
			log.Errorf("[workflow-runner] %s", errMsg)
			a.saveWorkflowDebugSnapshot(userID, sessionID, requestID, expectedTripPlanID, "macro_planning", out.String(), errMsg)
			return "", fmt.Errorf("%s", errMsg)
		}
		log.Infof("[workflow-runner] macro_planning: TripPlan %s confirmed in Neo4j (user=%s, session=%s)",
			expectedTripPlanID, tp.UserID, tp.SessionID)
	} else {
		// Not found by expected ID — try text extraction as fallback
		fallbackID := extractTripPlanID(out.String())
		if fallbackID != "" && fallbackID != expectedTripPlanID {
			log.Warnf("[workflow-runner] macro_planning: expected %s but found %s in text, checking Neo4j...",
				expectedTripPlanID, fallbackID)
			tp2, err2 := a.graphClient.FindTripPlanByID(ctx, fallbackID)
			if err2 == nil && tp2 != nil {
				expectedTripPlanID = fallbackID
				tp = tp2
				log.Infof("[workflow-runner] macro_planning: fallback TripPlan %s confirmed in Neo4j", fallbackID)
			}
		}
		if tp == nil {
			errMsg := fmt.Sprintf("TripPlan %s not found in Neo4j after macro planning (output len=%d)", expectedTripPlanID, out.Len())
			log.Errorf("[workflow-runner] %s", errMsg)
			a.saveWorkflowDebugSnapshot(userID, sessionID, requestID, expectedTripPlanID, "macro_planning", out.String(), errMsg)
			return "", fmt.Errorf("%s", errMsg)
		}
	}

	// Phase completeness validation
	rtForValidation := a.orchestrator.LoadOrInitRuntime(userID, sessionID)
	if err := a.checkAfterMacroPlanning(ctx, expectedTripPlanID, rtForValidation.Requirement.TotalDays, rtForValidation.Requirement); err != nil {
		var geoErr *TravelGeoScopeError
		if errors.As(err, &geoErr) && traceEmitterFromInvocation(invocation) != nil {
			emitGeoScopeViolationAnnotations(ctx, traceEmitterFromInvocation(invocation), "overview", expectedTripPlanID, geoErr.Violations)
		}
		log.Errorf("[workflow-runner] macro_planning: phase validation failed: %v", err)
		a.saveWorkflowDebugSnapshot(userID, sessionID, requestID, expectedTripPlanID, "macro_planning_validation", out.String(), err.Error())
		return "", fmt.Errorf("宏观规划校验失败: %w", err)
	}

	// Count phases for user message
	overview, err := a.graphClient.GetTripOverview(ctx, expectedTripPlanID)
	phaseCount := 0
	if err == nil {
		phaseCount = len(overview.Phases)
	}
	if trace := traceEmitterFromInvocation(invocation); trace != nil {
		trace.EmitStage(ctx, string(StageMacroPlanning), "completed", "大规划已建立", fmt.Sprintf("已创建 %d 个阶段节点，地图正在绘制阶段方向。", phaseCount))
		if overview != nil {
			emitPhaseOverviewMapEvents(ctx, trace, overview)
			if rt := a.orchestrator.LoadOrInitRuntime(userID, sessionID); rt.Requirement.RequirementReady {
				emitMacroRouteMapEvents(ctx, trace, rt.Requirement, overview)
			}
			emitGuideEvidenceForTrip(ctx, trace, a.graphClient, expectedTripPlanID, overview)
		}
	}

	a.emitTextEvent(outCh, invocation,
		fmt.Sprintf("宏观规划完成 — 已创建 %d 个 Phase。接下来进入图拆分阶段。", phaseCount))

	log.Infof("[workflow-runner] macro_planning: tripPlanID=%s phases=%d session=%s",
		expectedTripPlanID, phaseCount, sessionID)
	return expectedTripPlanID, nil
}

// buildMacroPrompt embeds the requirement snapshot into the macro planning prompt.
func buildMacroPrompt(originalMsg string, rt TravelSkillRuntime, expectedTripPlanID string) string {
	reqJSON, _ := json.Marshal(rt.Requirement)
	return fmt.Sprintf(`## 已确认的旅行需求

%s

## 用户原始消息
%s

## 系统预分配的 ID（必须原样使用，禁止自行生成）
- expected_trip_plan_id: %s
- user_id: %s
- session_id: %s
- request_id: %s

## 本阶段任务：MacroPlanning
只完成以下操作：
1. 基于需求创建 TripPlan（create_trip_plan，必须使用 expected_trip_plan_id 作为 trip_plan_id 参数）
2. 使用 get_weather_context 获取区域气候数据
3. 规划 1-8 个 Phase（region, season, theme, dayCount, start/end anchor）
4. 使用 split_parent_node 拆分 TripPlan → Phase

## 锚点覆盖约束
- requirement.destination_anchors 中 origin=user_explicit 的目的地是用户明确要去的目的地，必须在 Phase 中覆盖，不能只写成泛化目的地范围。
- requirement.destination_anchors 中 origin=system_inferred 的自然景观锚点是候选核心体验，尤其是雪山、峡谷、湖泊、森林、草原、徒步观景点。
- 如果天数不足以覆盖全部显式目的地或核心自然锚点，必须在 phase theme/region 中体现取舍，不要用城市到达、酒店、餐厅替代自然风光体验。

## 目的地范围硬约束
- Phase 的 region/name/theme 只能围绕 destination_scope、must_visit 和 destination_anchors 展开。
- 1-2 天短行程允许 1 个 Phase；3-5 天行程通常 2-3 个 Phase；更长行程再拆成 3-8 个 Phase。
- 用户限定为某个区域（例如西南、川滇藏、云南、香格里拉等）时，禁止引入明显无关的远方城市或区域。
- 出发地只允许作为路线起点或转移说明，不能把出发地扩展成目的地 Phase；若出发地不在目的地范围内，不要把它填入 Phase.region，可在 Phase.name 中写“从 X 出发/交通衔接”。
- 若不确定某城市是否属于用户目的地范围，宁可保留在公开权衡说明中，也不要创建该城市的 Phase。

禁止：Month/Week/Day 拆分、攻略采集、POI 验证、审查、逐日输出。

完成后只输出：TRIP_PLAN_CREATED:%s`,
		string(reqJSON), originalMsg, expectedTripPlanID, rt.UserID, rt.SessionID, rt.RunID, expectedTripPlanID)
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

func requirementSnapshotFromTripOverview(overview *graph.TripOverview) TravelRequirementSnapshot {
	if overview == nil {
		return TravelRequirementSnapshot{}
	}
	return TravelRequirementSnapshot{
		DestinationScope: strings.Join(append([]string{
			overview.TripPlan.Name,
			overview.TripPlan.RawRequirements,
		}, overview.TripPlan.MustVisit...), " "),
		TotalDays:     overview.TripPlan.TotalDays,
		StartDate:     overview.TripPlan.StartDate,
		EndDate:       overview.TripPlan.EndDate,
		TransportMode: overview.TripPlan.TransportMode,
		TravelStyle:   append([]string{overview.TripPlan.TravelStyle}, overview.TripPlan.Interests...),
		MustVisit:     append([]string(nil), overview.TripPlan.MustVisit...),
		AvoidPlaces:   append([]string(nil), overview.TripPlan.Avoid...),
	}
}

func (a *graphWorkflowAgent) runPhase2(ctx context.Context, tripPlanID string, emitters ...*TraceEmitter) error {
	var trace *TraceEmitter
	if len(emitters) > 0 {
		trace = emitters[0]
	}

	overview, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
	if err != nil {
		return fmt.Errorf("get trip overview: %w", err)
	}

	totalDays := len(overview.Days)
	if totalDays == 0 {
		return fmt.Errorf("no days found for tripPlanID: %s", tripPlanID)
	}
	geoConstraint := buildTravelGeoConstraintFromOverview(overview)
	requirement := requirementSnapshotFromTripOverview(overview)

	type dayRunInfo struct {
		id  string
		ctx dayExpansionContext
	}
	dayInfos := make([]dayRunInfo, 0, totalDays)
	dayContexts := dayExpansionContextsFromOverview(overview)
	for _, d := range overview.Days {
		if id, ok := d["id"].(string); ok && id != "" {
			ctx := dayContexts[id]
			ctx.DayID = id
			if ctx.DayIndex == 0 {
				ctx.DayIndex = int(getFloat(d, "dayIndex"))
			}
			ctx.GeoConstraint = geoConstraint
			ctx.Requirement = requirement
			dayInfos = append(dayInfos, dayRunInfo{id: id, ctx: ctx})
		}
	}
	sort.SliceStable(dayInfos, func(i, j int) bool {
		left := dayInfos[i].ctx.DayIndex
		right := dayInfos[j].ctx.DayIndex
		if left == right {
			return dayInfos[i].id < dayInfos[j].id
		}
		if left <= 0 {
			return false
		}
		if right <= 0 {
			return true
		}
		return left < right
	})

	log.Infof("[workflow-runner] Phase 2: verifying POIs for %d days", len(dayInfos))

	for i, info := range dayInfos {
		dayID := info.id
		dayCtx := info.ctx
		if dayCtx.DayIndex == 0 {
			dayCtx.DayIndex = i + 1
		}
		log.Infof("[workflow-runner] Phase 2: day %d/%d (%s)", i+1, len(dayInfos), dayID)
		if trace != nil {
			trace.Emit(ctx, PublicPlanningEvent{
				Type:           EventMapAnnotationAdded,
				Level:          "day",
				NodeID:         dayID,
				Status:         "active",
				PublicAction:   "验证当天地点",
				ThoughtSummary: fmt.Sprintf("正在验证第 %d/%d 天的 POI 和路线。", i+1, len(dayInfos)),
				Annotation: &PublicMapAnnotation{
					ID:      stablePlanningAnnotationID("day-expansion-progress", dayID, fmt.Sprint(i+1)),
					Kind:    "thought",
					Source:  "planning",
					Title:   fmt.Sprintf("第 %d 天地点验证", i+1),
					Summary: fmt.Sprintf("正在验证第 %d/%d 天的地点、坐标和路线。", i+1, len(dayInfos)),
					Status:  "active",
					Anchor:  PublicMapAnnotationAnchor{Type: "scope", NodeID: dayID, Label: fmt.Sprintf("Day %d", i+1)},
				},
			})
		}
		_, err := a.verifyDayPOIs(ctx, dayID, trace, dayCtx)
		if err != nil {
			log.Errorf("[workflow-runner] Phase 2 day %s failed: %v", dayID, err)
			if trace != nil {
				trace.Emit(ctx, PublicPlanningEvent{
					Type:           EventMapAnnotationAdded,
					Level:          "day",
					NodeID:         dayID,
					Status:         "review",
					PublicAction:   "记录地点验证失败",
					ThoughtSummary: "这一天的地点或路线验证失败，先记录失败原因并继续处理后续日程。",
					Annotation: &PublicMapAnnotation{
						ID:      stablePlanningAnnotationID("day-expansion-error", dayID, err.Error()),
						Kind:    "thought",
						Source:  "planning",
						Title:   "地点验证失败",
						Summary: truncateGuideText(err.Error(), maxGuideAnnotationSummary),
						Status:  "review",
						Anchor:  PublicMapAnnotationAnchor{Type: "scope", NodeID: dayID, Label: dayID},
					},
				})
			}
			// Continue to next day
		}
	}

	return nil
}

func (a *graphWorkflowAgent) verifyDayPOIs(ctx context.Context, dayID string, trace *TraceEmitter, contexts ...dayExpansionContext) ([]graph.POIInput, error) {
	dayContext := dayExpansionContext{}
	if len(contexts) > 0 {
		dayContext = contexts[0]
	}
	subgraph, err := a.graphClient.GetDaySubgraph(ctx, dayID)
	if err != nil {
		return nil, fmt.Errorf("get day subgraph: %w", err)
	}
	if subgraph == nil {
		return nil, fmt.Errorf("day subgraph is nil for %s", dayID)
	}

	day := subgraph.Day
	if dayContext.DayIndex == 0 {
		dayContext.DayIndex = day.DayIndex
	}
	requirementJSON, _ := json.Marshal(dayContext.Requirement)

	// Build a structured prompt for amap-agent
	prompt := fmt.Sprintf(`请验证并完善以下旅行日的 POI 安排：

已确认的旅行需求快照:
%s

日期: %s
天数序号: Day %d
主题: %s
主要区域: %s
起点: %s
路线/锚点说明: %s
规划记录: %s

请执行以下验证：
1. 如果主题或锚点说明包含雪山、峡谷、湖泊、森林、草原、徒步、观景等自然风光词，必须优先搜索自然景点或观景点；不能用市区、酒店、餐厅替代当天主体验
2. 对该区域进行 POI 关键词搜索（核心景点优先，餐饮、住宿仅作为补给），找出 2-3 个最合适的 POI
3. 对每个 POI 调用 amap_geocode 获取精确坐标
4. 结合旅行需求、POI 间距离和城市环境，自主选择最合适的路线工具获取距离和耗时；可用工具包括 amap_route_walking、amap_route_transit、amap_route_driving、amap_route_bicycling
5. 返回结构化的 POI 数据和路线数据；如果路线无法验证，routes 返回空数组，不要编造交通方式

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
      "transportMode": "agent_selected_mode",
      "distanceMeters": 5000,
      "durationMin": 15,
      "estimatedCost": 20,
      "polyline": "[[116.1,39.9],[116.2,39.95]]",
      "notes": "路线选择理由"
    }
  ]
}`, string(requirementJSON), day.Date, day.DayIndex, day.Theme, day.PrimaryArea, day.StartPoint, day.RouteOverview, day.ThinkingNotes)

	// Run amap-agent
	amapResult, err := runAmapAgentStandalone(ctx, prompt)
	if err != nil {
		log.Warnf("[workflow-runner] amap-agent day %s failed, using deterministic POI fallback: %v", dayID, err)
		emitDayExpansionNotice(ctx, trace, dayID, "结构化地点验证未完成", "改用确定性的地图搜索兜底，只接受带真实坐标的地点。", "review")
	}

	// Parse POIs, then re-check coordinates with direct geocoding before exposing them.
	pois, rawRoutes := parseAmapPOIResult(amapResult)
	if len(pois) > 0 {
		pois = exactifyParsedPOIs(ctx, pois, day, dayContext, trace)
		if isNaturalSceneryDay(day, dayContext) && !hasNaturalMainStop(pois) {
			emitDayExpansionNotice(ctx, trace, dayID, "主景点不匹配", "结构化结果没有覆盖自然风光主景点，改用核心锚点关键词重新搜索。", "review")
			pois = nil
		}
	}
	if len(pois) == 0 {
		fallbackPOIs, fallbackErr := discoverDayPOIsDirect(ctx, day, dayContext, trace)
		if fallbackErr != nil {
			emitDayExpansionNotice(ctx, trace, dayID, "地点搜索暂不可用", fallbackErr.Error(), "review")
		}
		pois = fallbackPOIs
	}
	if len(pois) == 0 {
		return nil, fmt.Errorf("no exact POIs found for day %s", dayID)
	}
	pois = filterPOIsByGeoConstraint(ctx, trace, dayID, pois, dayContext.GeoConstraint)
	if len(pois) == 0 {
		return nil, fmt.Errorf("day %s 的候选地点均超出用户目的地范围", dayID)
	}

	// Pre-generate POI IDs before any writes, so routes can reference them correctly.
	for i := range pois {
		if pois[i].ID == "" {
			pois[i].ID = fmt.Sprintf("poi-%s", uuid.NewString())
		}
	}
	writtenPOIs := make([]graph.POIInput, 0, len(pois))
	for _, poi := range pois {
		poiID, err := a.graphClient.UpsertPOIToDay(ctx, dayID, poi)
		if err != nil {
			log.Errorf("[workflow-runner] upsert POI %s to day %s: %v", poi.Name, dayID, err)
			continue
		}
		poi.ID = poiID
		writtenPOIs = append(writtenPOIs, poi)
		log.Infof("[workflow-runner]   POI: %s (%s) id=%s", poi.Name, poi.Type, poiID)
	}
	displayCtx := routeDisplayContext{
		PhaseID:        dayContext.PhaseID,
		PhaseSeq:       dayContext.PhaseSeq,
		PhaseName:      dayContext.PhaseName,
		DayID:          dayID,
		DayIndex:       dayContext.DayIndex,
		ConnectionType: "day_segment",
	}
	emitExactPOIMapBatch(ctx, trace, "day", dayID, writtenPOIs, displayCtx)

	routes := routeInputsFromRawAmapRoutes(rawRoutes, writtenPOIs, dayContext)
	if len(routes) == 0 {
		emitDayExpansionNotice(ctx, trace, dayID, "路线结果待补充", "路线 Agent 暂未返回可验证路线，因此本日只写入已验证地点，不默认生成交通方式。", "review")
	}
	for _, route := range routes {
		if err := a.graphClient.WriteRoute(ctx, route); err != nil {
			log.Errorf("[workflow-runner] write route: %v", err)
		}
	}
	emitRouteSegmentMapEvents(ctx, trace, "day", dayID, writtenPOIs, routes, displayCtx)

	// Mark day as verified
	_ = a.graphClient.UpdateNode(ctx, dayID, map[string]any{"status": "verified"})

	return writtenPOIs, nil
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
	Polyline       any     `json:"polyline"`
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

func routeInputsFromRawAmapRoutes(rawRoutes []rawAmapRoute, pois []graph.POIInput, dayCtx dayExpansionContext) []graph.RouteInput {
	if len(rawRoutes) == 0 || len(pois) == 0 {
		return nil
	}
	routes := make([]graph.RouteInput, 0, len(rawRoutes))
	for i, raw := range rawRoutes {
		mode := strings.TrimSpace(raw.TransportMode)
		if mode == "" {
			continue
		}
		if raw.FromPOI < 0 || raw.FromPOI >= len(pois) || raw.ToPOI < 0 || raw.ToPOI >= len(pois) {
			continue
		}
		from := pois[raw.FromPOI]
		to := pois[raw.ToPOI]
		if from.ID == "" || to.ID == "" {
			continue
		}
		polyline := normalizeRawPolyline(raw.Polyline)
		if polyline == "" && isValidLngLat(from.Lng, from.Lat) && isValidLngLat(to.Lng, to.Lat) {
			polyline = polylineJSON([][2]float64{{from.Lng, from.Lat}, {to.Lng, to.Lat}})
		}
		route := graph.RouteInput{
			FromPOIID:      from.ID,
			ToPOIID:        to.ID,
			TransportMode:  mode,
			Accuracy:       "agent_verified",
			Source:         "amap_agent",
			FromNodeID:     from.ID,
			ToNodeID:       to.ID,
			DistanceMeters: raw.DistanceMeters,
			DurationMin:    raw.DurationMin,
			Polyline:       polyline,
			EstimatedCost:  raw.EstimatedCost,
			Notes:          strings.TrimSpace(raw.Notes),
			ConnectionType: "day_segment",
		}
		enrichRouteDisplayMetadata(&route, from, to, routeDisplayContext{
			PhaseID:        dayCtx.PhaseID,
			PhaseSeq:       dayCtx.PhaseSeq,
			PhaseName:      dayCtx.PhaseName,
			DayID:          dayCtx.DayID,
			DayIndex:       dayCtx.DayIndex,
			SegmentIndex:   i + 1,
			ConnectionType: "day_segment",
		})
		routes = append(routes, route)
	}
	return routes
}

func normalizeRawPolyline(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		b, err := json.Marshal(v)
		if err == nil {
			return string(b)
		}
	}
	return ""
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
| 计算 POI 之间的路线距离和时间 | 根据用户需求、城市环境和 POI 间距离，在 amap_route_walking / amap_route_transit / amap_route_driving / amap_route_bicycling 中选择 |

### 第三步：执行工具调用
1. 先用 amap_poi_keyword_search 按城市+关键词搜索 2-3 个 POI
2. 对每个 POI 调用 amap_geocode 获取精确坐标
3. 对相邻 POI 选择最符合用户需求的路线工具获取路线数据
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
      "transportMode": "agent_selected_mode",
      "distanceMeters": 5000,
      "durationMin": 15,
      "polyline": "[[116.1,39.9],[116.2,39.95]]",
      "notes": "路线选择理由"
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

func (a *graphWorkflowAgent) runPhase3(ctx context.Context, tripPlanID string, trace *TraceEmitter, requirements ...TravelRequirementSnapshot) error {
	overview, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
	if err != nil {
		return fmt.Errorf("get trip overview: %w", err)
	}

	dayIDs := extractDayIDs(overview)
	log.Infof("[workflow-runner] Phase 3: reviewing %d days", len(dayIDs))

	for i, dayID := range dayIDs {
		log.Infof("[workflow-runner] Phase 3: day %d/%d (%s)", i+1, len(dayIDs), dayID)
		if trace != nil {
			trace.Emit(ctx, PublicPlanningEvent{
				Type:           EventMapAnnotationAdded,
				Level:          "day",
				NodeID:         dayID,
				Status:         "active",
				PublicAction:   "启动当天审核",
				ThoughtSummary: fmt.Sprintf("正在审核第 %d/%d 天的地点、路线和内容质量。", i+1, len(dayIDs)),
				Annotation: &PublicMapAnnotation{
					ID:      stablePlanningAnnotationID("review-day-progress", dayID, fmt.Sprint(i+1)),
					Kind:    "review",
					Source:  "review",
					Title:   fmt.Sprintf("第 %d 天审核中", i+1),
					Summary: "审核 Agent 正在检查地点密度、路线合理性和内容质量。",
					Status:  "active",
					Anchor:  PublicMapAnnotationAnchor{Type: "scope", NodeID: dayID, Label: fmt.Sprintf("Day %d", i+1)},
				},
			})
		}

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
				emitReviewAnnotation(ctx, trace, "day", poi.ID, poi.Name, "POI 审核", "point", *poiReview)
			}
		}

		// L4: Day-level content review — run 5 agents in parallel
		dayReviewResults := a.runDayContentReviews(ctx, subgraph)
		for _, r := range dayReviewResults {
			_, _ = a.graphClient.WriteReviewResult(ctx, dayID, r.Review)
			emitReviewAnnotation(ctx, trace, "day", dayID, subgraph.Day.Date, r.AgentName, "scope", r.Review)
		}
	}

	// L3: Week-level review
	weekIDs := extractWeekIDs(overview)
	for _, weekID := range weekIDs {
		weekReview := runWeekReview(ctx, weekID)
		if weekReview != nil {
			_, _ = a.graphClient.WriteReviewResult(ctx, weekID, *weekReview)
			emitReviewAnnotation(ctx, trace, "week", weekID, weekID, "周级审核", "scope", *weekReview)
		}
	}

	if err := a.reviewGeoScope(ctx, tripPlanID, overview, trace, requirements...); err != nil {
		return err
	}

	if err := a.reviewAnchorCoverage(ctx, tripPlanID, overview, trace, requirements...); err != nil {
		return err
	}

	return nil
}

type anchorCoverageFinding struct {
	Anchor   DestinationAnchorSnapshot
	Covered  bool
	Critical bool
	Matches  []string
	Reason   string
}

func (a *graphWorkflowAgent) reviewAnchorCoverage(ctx context.Context, tripPlanID string, overview *graph.TripOverview, trace *TraceEmitter, requirements ...TravelRequirementSnapshot) error {
	anchors := deriveAnchorsForGraphSplitting(overview, requirements...)
	if len(anchors) == 0 {
		return nil
	}
	findings, err := a.evaluateAnchorCoverage(ctx, overview, anchors)
	if err != nil {
		return err
	}

	var criticalMissing []string
	for _, finding := range findings {
		emitAnchorCoverageAnnotation(ctx, trace, tripPlanID, finding)
		if finding.Critical && !finding.Covered {
			criticalMissing = append(criticalMissing, finding.Anchor.Name)
		}
	}
	if len(criticalMissing) == 0 {
		review := graph.ReviewInput{
			Level:       "trip",
			Dimension:   "anchor_coverage",
			Score:       92,
			Passed:      true,
			Summary:     "核心目的地与自然风光锚点覆盖审核通过。",
			Suggestions: []string{"继续在最终方案中解释各锚点的观景窗口、体力和天气风险。"},
		}
		_, _ = a.graphClient.WriteReviewResult(ctx, tripPlanID, review)
		emitReviewAnnotation(ctx, trace, "overview", tripPlanID, "锚点覆盖", "锚点覆盖审核", "scope", review)
		return nil
	}

	review := graph.ReviewInput{
		Level:     "trip",
		Dimension: "anchor_coverage",
		Score:     45,
		Passed:    false,
		Summary:   "核心景点覆盖不足，不能直接生成最终方案。",
		CriticalIssues: []string{
			"缺失核心锚点：" + strings.Join(criticalMissing, "、"),
			"自然风光需求不能用城市到达、酒店或餐饮替代。",
		},
		Suggestions: []string{
			"请延长天数、舍弃部分目的地，或接受更高强度转移日后重新规划。",
			"也可以保留三地但明确哪些核心景点作为备选或放弃。",
		},
	}
	_, _ = a.graphClient.WriteReviewResult(ctx, tripPlanID, review)
	emitReviewAnnotation(ctx, trace, "overview", tripPlanID, "锚点覆盖", "锚点覆盖审核", "scope", review)
	return fmt.Errorf("核心景点覆盖不足：%s。建议延长天数、舍弃部分目的地，或确认可接受更高强度转移日后重新规划", strings.Join(criticalMissing, "、"))
}

func (a *graphWorkflowAgent) evaluateAnchorCoverage(ctx context.Context, overview *graph.TripOverview, anchors []DestinationAnchorSnapshot) ([]anchorCoverageFinding, error) {
	if overview == nil {
		return nil, nil
	}
	coverageTextParts := []string{}
	for _, day := range overview.Days {
		dayID := getStr(day, "id")
		if dayID == "" {
			continue
		}
		subgraph, err := a.graphClient.GetDaySubgraph(ctx, dayID)
		if err != nil || subgraph == nil {
			continue
		}
		for _, poi := range subgraph.POIs {
			if isLogisticsPOIType(poi.Type) || isGenericAdministrativePOI(graph.POIInput{Name: poi.Name, Type: poi.Type, Address: poi.Address}) {
				continue
			}
			coverageTextParts = append(coverageTextParts,
				poi.Name,
				poi.Type,
				poi.Address,
				poi.District,
				poi.City,
				poi.Description,
				poi.Notes,
			)
		}
	}
	coverageText := strings.Join(coverageTextParts, " ")
	coveredHighPriorityByDestination := map[string]bool{}
	topPriorityByDestination := map[string]int{}
	for _, anchor := range anchors {
		if anchor.Kind != "destination" && anchor.Priority > topPriorityByDestination[anchor.Destination] {
			topPriorityByDestination[anchor.Destination] = anchor.Priority
		}
		if anchor.Kind != "destination" && anchor.Priority >= 90 && anchorCoveredByText(anchor, coverageText) {
			coveredHighPriorityByDestination[anchor.Destination] = true
		}
	}

	findings := make([]anchorCoverageFinding, 0, len(anchors))
	for _, anchor := range anchors {
		covered := anchorCoveredByText(anchor, coverageText)
		critical := false
		if anchor.Origin == anchorOriginUserExplicit && anchor.MustCover {
			critical = !covered && !coveredHighPriorityByDestination[anchor.Destination]
		}
		if anchor.Origin == anchorOriginSystemInferred &&
			anchor.Priority >= 90 &&
			anchor.Priority == topPriorityByDestination[anchor.Destination] {
			critical = true
		}
		reason := "已在日程、POI 或路线说明中覆盖。"
		if !covered {
			reason = "未在已验证 POI 或日程说明中找到，需要给出放弃原因或重新规划。"
		}
		findings = append(findings, anchorCoverageFinding{
			Anchor:   anchor,
			Covered:  covered,
			Critical: critical,
			Matches:  anchorMatchLabels(anchor, coverageText),
			Reason:   reason,
		})
	}
	return findings, nil
}

func anchorCoveredByText(anchor DestinationAnchorSnapshot, text string) bool {
	if strings.TrimSpace(anchor.Name) == "" {
		return false
	}
	names := []string{anchor.Name}
	if strings.HasSuffix(anchor.Name, "峰") {
		names = append(names, strings.TrimSuffix(anchor.Name, "峰"))
	}
	if strings.HasSuffix(anchor.Name, "国家公园") {
		names = append(names, strings.TrimSuffix(anchor.Name, "国家公园"))
	}
	for _, name := range names {
		if name != "" && strings.Contains(text, name) {
			return true
		}
	}
	if anchor.Kind == "destination" && anchor.Destination != "" && strings.Contains(text, anchor.Destination) {
		return true
	}
	return false
}

func anchorMatchLabels(anchor DestinationAnchorSnapshot, text string) []string {
	if anchorCoveredByText(anchor, text) {
		return []string{"命中：" + anchor.Name}
	}
	return nil
}

func emitAnchorCoverageAnnotation(ctx context.Context, emitter *TraceEmitter, tripPlanID string, finding anchorCoverageFinding) {
	if emitter == nil {
		return
	}
	status := "selected"
	if !finding.Covered {
		status = "review"
	}
	if finding.Critical && !finding.Covered {
		status = "rejected"
	}
	title := "锚点已覆盖"
	if !finding.Covered {
		title = "锚点待补齐"
	}
	summary := fmt.Sprintf("%s：%s", finding.Anchor.Name, finding.Reason)
	tags := []string{"锚点覆盖", defaultIfEmpty(finding.Anchor.Destination, "目的地")}
	if finding.Anchor.Kind != "" {
		tags = append(tags, finding.Anchor.Kind)
	}
	if finding.Critical {
		tags = append(tags, "关键")
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapAnnotationAdded,
		Level:          "overview",
		NodeID:         tripPlanID,
		Status:         status,
		PublicAction:   "展示锚点覆盖审核",
		ThoughtSummary: "正在核对用户明确目的地和自然风光核心锚点是否被真实日程覆盖。",
		RecordedFacts:  []string{summary},
		Annotation: &PublicMapAnnotation{
			ID:       stablePlanningAnnotationID("anchor-coverage", tripPlanID, finding.Anchor.Destination, finding.Anchor.Name),
			Kind:     "anchor_coverage",
			Source:   "review",
			Title:    title,
			Summary:  truncateGuideText(summary, maxGuideAnnotationSummary),
			Status:   status,
			Tags:     tags,
			Reasons:  []string{defaultIfEmpty(finding.Anchor.Reason, "核心锚点覆盖检查")},
			Evidence: append([]string(nil), finding.Matches...),
			Anchor: PublicMapAnnotationAnchor{
				Type:   "scope",
				NodeID: tripPlanID,
				Label:  defaultIfEmpty(finding.Anchor.Name, "锚点覆盖"),
			},
		},
	})
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

type namedReviewResult struct {
	AgentName string
	Review    graph.ReviewInput
}

func (a *graphWorkflowAgent) runDayContentReviews(ctx context.Context, subgraph *graph.DaySubgraph) []namedReviewResult {
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
	results := make(chan namedReviewResult, len(a.reviewAgents))

	for _, ra := range a.reviewAgents {
		wg.Add(1)
		go func(name string, ag agentcore.Agent) {
			defer wg.Done()
			output, err := runReviewAgentStandalone(ctx, ag,
				fmt.Sprintf("review-%s-%s", name, subgraph.Day.Date), dayPrompt)
			if err == nil && output != "" {
				if r := parseReviewOutput(output); r != nil {
					results <- namedReviewResult{AgentName: publicReviewAgentLabel(name), Review: *r}
				}
			}
		}(ra.name, ra.ag)
	}

	wg.Wait()
	close(results)

	var reviews []namedReviewResult
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

// --- Debug ---

type workflowDebugSnapshot struct {
	RunID              string `json:"run_id"`
	UserID             string `json:"user_id"`
	SessionID          string `json:"session_id"`
	RequestID          string `json:"request_id"`
	ExpectedTripPlanID string `json:"expected_trip_plan_id"`
	Stage              string `json:"stage"`
	OutputLen          int    `json:"output_len"`
	OutputHead         string `json:"output_head"`
	OutputTail         string `json:"output_tail"`
	ExtractedID        string `json:"extracted_trip_plan_id"`
	Error              string `json:"error"`
	CreatedAt          string `json:"created_at"`
}

func (a *graphWorkflowAgent) saveWorkflowDebugSnapshot(
	userID, sessionID, requestID, expectedTripPlanID, stage, output, errMsg string,
) {
	head := output
	if len(head) > 2000 {
		head = head[:2000]
	}
	tail := output
	if len(tail) > 2000 {
		tail = tail[len(tail)-2000:]
	}

	snap := workflowDebugSnapshot{
		RunID:              requestID,
		UserID:             userID,
		SessionID:          sessionID,
		RequestID:          requestID,
		ExpectedTripPlanID: expectedTripPlanID,
		Stage:              stage,
		OutputLen:          len(output),
		OutputHead:         head,
		OutputTail:         tail,
		ExtractedID:        extractTripPlanID(output),
		Error:              errMsg,
		CreatedAt:          time.Now().Format(time.RFC3339),
	}

	dir := "/tmp/sea_workflow_debug"
	_ = os.MkdirAll(dir, 0o755)
	filename := filepath.Join(dir, fmt.Sprintf("%s_%s_%d.json", requestID, stage, time.Now().Unix()))
	b, _ := json.MarshalIndent(snap, "", "  ")
	if err := os.WriteFile(filename, b, 0o644); err != nil {
		log.Errorf("[workflow-runner] save debug snapshot failed: %v", err)
	} else {
		log.Infof("[workflow-runner] debug snapshot saved: %s", filename)
	}
}

// --- Graph Splitting: Phase → Month → Week → Day (mechanical, no LLM) ---

func (a *graphWorkflowAgent) runGraphSplitting(ctx context.Context, tripPlanID string, requirements ...TravelRequirementSnapshot) error {
	overview, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
	if err != nil {
		return fmt.Errorf("get trip overview: %w", err)
	}
	tripAnchors := deriveAnchorsForGraphSplitting(overview, requirements...)

	globalDayIdx := 1
	for _, p := range overview.Phases {
		phaseID := getStr(p, "id")
		phaseName := getStr(p, "name")
		phaseRegion := getStr(p, "region")
		phaseTheme := getStr(p, "theme")
		phaseStart := getStr(p, "startDate")
		phaseEnd := getStr(p, "endDate")
		phaseAnchors := anchorsForPhase(tripAnchors, phaseName, phaseRegion, phaseTheme, len(overview.Phases) <= 1)
		phaseDayOffset := 0

		if phaseStart == "" || phaseEnd == "" {
			log.Warnf("[graph-splitting] Phase %s missing dates, skipping", phaseName)
			continue
		}

		startT, err := time.Parse("2006-01-02", phaseStart)
		if err != nil {
			return fmt.Errorf("parse phase start date %s: %w", phaseStart, err)
		}
		endT, err := time.Parse("2006-01-02", phaseEnd)
		if err != nil {
			return fmt.Errorf("parse phase end date %s: %w", phaseEnd, err)
		}

		log.Infof("[graph-splitting] Phase %s: %s ~ %s (%s)", phaseName, phaseStart, phaseEnd, phaseRegion)

		// Split phase into months by calendar boundary
		months := splitByMonth(startT, endT, phaseRegion)
		for mi, m := range months {
			monthID := fmt.Sprintf("month-%s-%d", phaseID[:8], mi+1)
			// Create Month node
			_, err := a.graphClient.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
				_, err := tx.Run(ctx,
					`MATCH (p:Phase {id: $phaseID})
					 MERGE (m:Month {id: $id})
					 ON CREATE SET m.name = $name, m.seq = $seq, m.startDate = $startDate,
						m.endDate = $endDate, m.region = $region, m.status = 'outlined'
					 MERGE (p)-[:HAS_MONTH]->(m)
					 RETURN m.id`,
					map[string]any{
						"phaseID": phaseID, "id": monthID,
						"name": m.name, "seq": mi + 1,
						"startDate": m.start.Format("2006-01-02"),
						"endDate":   m.end.Format("2006-01-02"),
						"region":    phaseRegion,
					})
				return nil, err
			})
			if err != nil {
				log.Errorf("[graph-splitting] create month %s: %v", monthID, err)
				continue
			}

			// Split month into weeks
			weeks := splitByWeek(m.start, m.end, phaseRegion)
			for wi, w := range weeks {
				weekID := fmt.Sprintf("week-%s-%d-%d", phaseID[:8], mi+1, wi+1)
				_, err := a.graphClient.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
					_, err := tx.Run(ctx,
						`MATCH (m:Month {id: $monthID})
						 CREATE (m)-[:HAS_WEEK]->(w:Week {
							id: $id, name: $name, seq: $seq, startDate: $startDate, endDate: $endDate,
							primaryLocation: $region, status: 'outlined'
						 }) RETURN w.id`,
						map[string]any{
							"monthID": monthID, "id": weekID,
							"name": w.name, "seq": wi + 1,
							"startDate": w.start.Format("2006-01-02"),
							"endDate":   w.end.Format("2006-01-02"),
							"region":    phaseRegion,
						})
					return nil, err
				})
				if err != nil {
					log.Errorf("[graph-splitting] create week %s: %v", weekID, err)
					continue
				}

				// Split week into days
				days := splitByDay(w.start, w.end, globalDayIdx)
				for _, d := range days {
					dayID := fmt.Sprintf("day-%s-%d", phaseID[:8], d.dayIndex)
					globalDayIdx++
					phaseDayOffset++
					dayPlan := anchoredDayPlanForPhase(phaseAnchors, phaseDayOffset, phaseName, phaseRegion)
					_, err := a.graphClient.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
						_, err := tx.Run(ctx,
							`MATCH (w:Week {id: $weekID})
							 CREATE (w)-[:HAS_DAY]->(d:Day {
								id: $id, date: $date, dayIndex: $dayIndex, theme: $theme,
								intensity: $intensity, primaryArea: $primaryArea,
								routeOverview: $routeOverview, thinkingNotes: $thinkingNotes,
								status: 'outlined'
							 }) RETURN d.id`,
							map[string]any{
								"weekID": weekID, "id": dayID,
								"date": d.date, "dayIndex": d.dayIndex,
								"theme": dayPlan.Theme, "intensity": "均衡",
								"primaryArea":   dayPlan.PrimaryArea,
								"routeOverview": dayPlan.RouteOverview,
								"thinkingNotes": dayPlan.ThinkingNotes,
							})
						return nil, err
					})
					if err != nil {
						log.Errorf("[graph-splitting] create day %s: %v", dayID, err)
					}
				}
			}
		}
	}

	// Verify
	overview2, err := a.graphClient.GetTripOverview(ctx, tripPlanID)
	if err == nil {
		log.Infof("[graph-splitting] done: %d phases, %d months, %d weeks, %d days",
			len(overview2.Phases), len(overview2.Months), len(overview2.Weeks), len(overview2.Days))
	}
	return nil
}

type anchoredDayPlan struct {
	Theme         string
	PrimaryArea   string
	RouteOverview string
	ThinkingNotes string
}

func deriveAnchorsFromTripOverview(overview *graph.TripOverview) []DestinationAnchorSnapshot {
	if overview == nil {
		return nil
	}
	snap := TravelRequirementSnapshot{
		DestinationScope: strings.Join(append([]string{
			overview.TripPlan.Name,
			overview.TripPlan.RawRequirements,
		}, overview.TripPlan.MustVisit...), " "),
		TotalDays:     overview.TripPlan.TotalDays,
		TransportMode: overview.TripPlan.TransportMode,
		TravelStyle:   append([]string{overview.TripPlan.TravelStyle}, overview.TripPlan.Interests...),
		MustVisit:     append([]string(nil), overview.TripPlan.MustVisit...),
	}
	enrichRequirementPlanningAnchors(&snap)
	return snap.DestinationAnchors
}

func deriveAnchorsForGraphSplitting(overview *graph.TripOverview, requirements ...TravelRequirementSnapshot) []DestinationAnchorSnapshot {
	if len(requirements) > 0 {
		req := requirements[0]
		if len(req.DestinationAnchors) == 0 {
			enrichRequirementPlanningAnchors(&req)
		}
		if len(req.DestinationAnchors) > 0 {
			return req.DestinationAnchors
		}
	}
	return deriveAnchorsFromTripOverview(overview)
}

func anchorsForPhase(anchors []DestinationAnchorSnapshot, phaseName, phaseRegion, phaseTheme string, allowAll bool) []DestinationAnchorSnapshot {
	text := strings.Join([]string{phaseName, phaseRegion, phaseTheme}, " ")
	var matched []DestinationAnchorSnapshot
	for _, anchor := range anchors {
		if anchor.Kind == "destination" {
			continue
		}
		if phaseTextMatchesAnchorDestination(text, anchor.Destination) || strings.Contains(text, anchor.Name) {
			matched = append(matched, anchor)
		}
	}
	if len(matched) == 0 && allowAll {
		for _, anchor := range anchors {
			if anchor.Kind != "destination" {
				matched = append(matched, anchor)
			}
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].Priority > matched[j].Priority
	})
	return dedupeDestinationAnchors(matched)
}

func phaseTextMatchesAnchorDestination(text, destination string) bool {
	if destination != "" && strings.Contains(text, destination) {
		return true
	}
	switch destination {
	case "香格里拉":
		return containsAny(text, []string{"迪庆", "滇西北", "梅里", "德钦"})
	case "稻城亚丁":
		return containsAny(text, []string{"亚丁", "稻城", "川西", "甘孜"})
	case "林芝":
		return containsAny(text, []string{"林芝", "藏东南", "西藏东南", "鲁朗", "巴松措", "南迦巴瓦"})
	default:
		return false
	}
}

func anchoredDayPlanForPhase(anchors []DestinationAnchorSnapshot, phaseDayOffset int, phaseName, phaseRegion string) anchoredDayPlan {
	fallbackArea := firstNonEmptyString(phaseRegion, phaseName)
	if len(anchors) == 0 {
		return anchoredDayPlan{
			Theme:         phaseName,
			PrimaryArea:   fallbackArea,
			RouteOverview: fmt.Sprintf("围绕%s展开，当天地点需要通过地图搜索复核。", fallbackArea),
			ThinkingNotes: "未匹配到内置自然锚点，按阶段区域生成日级搜索。",
		}
	}
	if phaseDayOffset <= 0 {
		phaseDayOffset = 1
	}
	anchor := anchors[(phaseDayOffset-1)%len(anchors)]
	return anchoredDayPlan{
		Theme:         fmt.Sprintf("%s自然风光：%s", defaultIfEmpty(anchor.Destination, fallbackArea), anchor.Name),
		PrimaryArea:   anchor.Name,
		RouteOverview: fmt.Sprintf("围绕%s展开自然风光体验；它是%s的核心候选锚点，后续只用真实 POI 坐标上图。", anchor.Name, defaultIfEmpty(anchor.Destination, fallbackArea)),
		ThinkingNotes: strings.TrimSpace(strings.Join([]string{
			"anchor=" + anchor.Name,
			"destination=" + anchor.Destination,
			"reason=" + anchor.Reason,
		}, "；")),
	}
}

// --- Month/Week/Day splitting helpers ---

type monthSpan struct {
	name  string
	start time.Time
	end   time.Time
}

func splitByMonth(start, end time.Time, region string) []monthSpan {
	var months []monthSpan
	cur := start
	for cur.Before(end) || cur.Equal(end) {
		// End of current month
		monthEnd := time.Date(cur.Year(), cur.Month()+1, 0, 0, 0, 0, 0, cur.Location())
		if monthEnd.After(end) {
			monthEnd = end
		}
		months = append(months, monthSpan{
			name:  fmt.Sprintf("%s %d年%d月", region, cur.Year(), cur.Month()),
			start: cur,
			end:   monthEnd,
		})
		cur = monthEnd.AddDate(0, 0, 1)
	}
	return months
}

type weekSpan struct {
	name  string
	start time.Time
	end   time.Time
}

func splitByWeek(start, end time.Time, region string) []weekSpan {
	var weeks []weekSpan
	cur := start
	seq := 1
	for cur.Before(end) || cur.Equal(end) {
		weekEnd := cur.AddDate(0, 0, 6)
		if weekEnd.After(end) {
			weekEnd = end
		}
		weeks = append(weeks, weekSpan{
			name:  fmt.Sprintf("第%d周", seq),
			start: cur,
			end:   weekEnd,
		})
		cur = weekEnd.AddDate(0, 0, 1)
		seq++
	}
	return weeks
}

type daySpan struct {
	date     string
	dayIndex int
}

func splitByDay(start, end time.Time, startIdx int) []daySpan {
	var days []daySpan
	cur := start
	idx := startIdx
	for cur.Before(end) || cur.Equal(end) {
		days = append(days, daySpan{
			date:     cur.Format("2006-01-02"),
			dayIndex: idx,
		})
		cur = cur.AddDate(0, 0, 1)
		idx++
	}
	return days
}

// --- Final Output: week-based LLM generation ---

func (a *graphWorkflowAgent) runFinalOutput(ctx context.Context, tripPlanID string, outCh chan<- *event.Event, inv *agentcore.Invocation) (string, error) {
	// Use a long-lived context for the final output generation (56+ LLM calls)
	llmCtx, llmCancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer llmCancel()

	overview, err := a.graphClient.GetTripOverview(llmCtx, tripPlanID)
	if err != nil {
		return "", fmt.Errorf("get trip overview: %w", err)
	}

	var answerBuilder strings.Builder
	var allWeatherNotes []string

	totalDays := len(overview.Days)
	log.Infof("[workflow-runner] final output: %d days, %d phases", totalDays, len(overview.Phases))

	// Group days by Phase
	phaseDayGroups := a.groupDaysByPhase(llmCtx, tripPlanID, overview)

	for _, pg := range phaseDayGroups {
		// Phase header
		answerBuilder.WriteString(fmt.Sprintf("\n## %s\n\n", pg.phaseName))
		answerBuilder.WriteString(fmt.Sprintf("**区域**: %s | **天数**: %d 天 | **时间**: %s ~ %s\n\n",
			pg.region, len(pg.days), pg.startDate, pg.endDate))

		// Get climate data for this phase's region
		climateInfo := a.getClimateSummary(llmCtx, pg.region, pg.startDate)

		// Group days into weeks of 7
		for i := 0; i < len(pg.days); i += 7 {
			end := i + 7
			if end > len(pg.days) {
				end = len(pg.days)
			}
			weekDays := pg.days[i:end]

			log.Infof("[workflow-runner] generating week %d-%d of phase %s (%d days)",
				i+1, end, pg.phaseName, len(weekDays))

			weekText, err := a.runWeekOutputAgent(llmCtx, weekDays, pg.phaseName, pg.region, climateInfo)
			if err != nil {
				log.Errorf("[workflow-runner] week output agent error: %v", err)
				// Fallback: generate basic text
				weekText = generateBasicWeekText(weekDays, pg.phaseName, pg.region)
			}
			answerBuilder.WriteString(weekText)
			answerBuilder.WriteString("\n")
		}

		// Phase summary
		answerBuilder.WriteString(fmt.Sprintf("\n**%s 小结**: %d 天行程已规划，覆盖 %s 区域。\n\n",
			pg.phaseName, len(pg.days), pg.region))

		if climateInfo != "" {
			allWeatherNotes = append(allWeatherNotes, climateInfo)
		}
	}

	finalJSON := assembleFinalJSON(answerBuilder.String(), &globalNotes{weatherNotes: allWeatherNotes}, nil, nil)
	return finalJSON, nil
}

type phaseDayGroup struct {
	phaseName string
	region    string
	startDate string
	endDate   string
	days      []map[string]any
}

func (a *graphWorkflowAgent) groupDaysByPhase(ctx context.Context, tripPlanID string, overview *graph.TripOverview) []phaseDayGroup {
	var groups []phaseDayGroup
	for _, p := range overview.Phases {
		phaseID := getStr(p, "id")
		pg := phaseDayGroup{
			phaseName: getStr(p, "name"),
			region:    getStr(p, "region"),
			startDate: getStr(p, "startDate"),
			endDate:   getStr(p, "endDate"),
		}
		// Find days under this phase via Month→Week→Day
		for _, d := range overview.Days {
			pg.days = append(pg.days, d)
		}
		// Filter: only keep days within this phase's date range
		var filtered []map[string]any
		for _, d := range pg.days {
			date := getStr(d, "date")
			if date >= pg.startDate && date <= pg.endDate {
				filtered = append(filtered, d)
			}
		}
		pg.days = filtered
		if len(pg.days) > 0 {
			groups = append(groups, pg)
		}
		_ = phaseID
	}
	return groups
}

func (a *graphWorkflowAgent) getClimateSummary(ctx context.Context, region, startDate string) string {
	t, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return ""
	}
	month := int(t.Month())
	wc, err := a.graphClient.GetWeatherContext(ctx, region, month)
	if err != nil || wc == nil {
		return ""
	}
	var parts []string
	for _, cd := range wc.ClimateData {
		parts = append(parts, fmt.Sprintf("%d月: %.0f-%.0f°C, 降水%.0fmm, 极端天气风险=%s",
			cd.Month, cd.AvgLowTemp, cd.AvgHighTemp, cd.Precipitation, cd.ExtremeWeatherRisk))
	}
	return strings.Join(parts, "; ")
}

func (a *graphWorkflowAgent) runWeekOutputAgent(ctx context.Context, weekDays []map[string]any, phaseName, region, climateInfo string) (string, error) {
	// Build day summaries
	var daySummaries []string
	for _, d := range weekDays {
		date := getStr(d, "date")
		theme := getStr(d, "theme")
		dayIndex := int(getFloat(d, "dayIndex"))
		daySummaries = append(daySummaries, fmt.Sprintf("Day %d (%s): %s — %s", dayIndex, date, region, theme))
	}

	prompt := fmt.Sprintf(`## 任务：为以下 %d 天生成每日旅行行程

### 阶段: %s
### 区域: %s
### 气候: %s

### 天数列表:
%s

### 输出要求

为每一天生成详细的旅行行程，包含：

1. **日期标题**: ### Day N: YYYY-MM-DD — 主题
2. **天气概况**: 温度范围、注意事项
3. **当日行程表**:
| 时间 | 活动 | 地点 | 说明 |
|------|------|------|------|
4. **推荐景点**: 2-3 个具体景点名称、地址、推荐理由
5. **餐饮推荐**: 1-2 个当地特色美食
6. **住宿建议**: 推荐住宿区域和类型
7. **交通方式**: 按已验证路线说明交通方式、距离和耗时
8. **本日小结**: 行程节奏和亮点

**详细度底线**: 每天至少 200 字。必须推荐具体的地名和景点，不要泛泛而谈。
**输出格式**: 直接输出 Markdown，不要包裹在 JSON 中。`, len(weekDays), phaseName, region, climateInfo, strings.Join(daySummaries, "\n"))

	thinkingEnabled := true
	temperature := 0.3
	topP := 0.7

	alimodel := newModelForLevel("day-output-agent", ModelLevelMedium)
	dayPlanner := builtin.New(builtin.Options{ThinkingEnabled: &thinkingEnabled})

	dayAgent := llmagent.New("week-output",
		llmagent.WithModel(alimodel),
		llmagent.WithPlanner(dayPlanner),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			Temperature:     &temperature,
			TopP:            &topP,
			ThinkingEnabled: &thinkingEnabled,
		}),
		llmagent.WithInstruction(fmt.Sprintf(`你是一个旅行行程生成 Agent。
根据阶段名称、区域、气候信息和天数列表，为每一天生成详细的 Markdown 行程。
必须推荐具体的地名和景点（如"张家界国家森林公园"而不是"当地景点"）。
每天至少 200 字。直接输出 Markdown，不要输出 JSON。`)),
	)

	rn := runner.NewRunner("week-output", dayAgent)
	defer rn.Close()

	eventCh, err := rn.Run(ctx, "week-output-system",
		fmt.Sprintf("week-%d", time.Now().UnixNano()),
		model.NewUserMessage(prompt), agentcore.WithStream(true))
	if err != nil {
		return "", fmt.Errorf("run week output agent: %w", err)
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

func generateBasicWeekText(weekDays []map[string]any, phaseName, region string) string {
	var b strings.Builder
	for _, d := range weekDays {
		date := getStr(d, "date")
		dayIndex := int(getFloat(d, "dayIndex"))
		b.WriteString(fmt.Sprintf("### Day %d: %s — %s\n\n", dayIndex, date, phaseName))
		b.WriteString(fmt.Sprintf("**区域**: %s\n\n", region))
		b.WriteString("今日行程待详细规划。\n\n")
	}
	return b.String()
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
