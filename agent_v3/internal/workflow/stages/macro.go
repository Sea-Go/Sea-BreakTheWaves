package stages

import (
	"agent_v3/internal/config"
	domaingeo "agent_v3/internal/domain/geo"
	"agent_v3/internal/graph"
	workfloworchestrator "agent_v3/internal/workflow/orchestrator"
	workflowruntime "agent_v3/internal/workflow/runtime"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"strings"
	"time"
	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
)

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
	macroAgent := a.newMacroAgent(expectedTripPlanID, userID, sessionID, requestID)

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
		var geoErr *domaingeo.TravelGeoScopeError
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

	a.emitTextEvent(outCh,
		fmt.Sprintf("宏观规划完成 — 已创建 %d 个 Phase。接下来进入图拆分阶段。", phaseCount))

	log.Infof("[workflow-runner] macro_planning: tripPlanID=%s phases=%d session=%s",
		expectedTripPlanID, phaseCount, sessionID)
	return expectedTripPlanID, nil
}

// buildMacroPrompt embeds the requirement snapshot into the macro planning prompt.
func buildMacroPrompt(originalMsg string, rt workflowruntime.TravelSkillRuntime, expectedTripPlanID string) string {
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
	if r := workfloworchestrator.ParseSkillResult(output); r != nil && r.TripPlanID != "" {
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
