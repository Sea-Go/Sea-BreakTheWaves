package stages

import (
	"agent_v3/internal/config"
	"agent_v3/internal/graph"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/planner/builtin"
	"trpc.group/trpc-go/trpc-agent-go/runner"
)

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
