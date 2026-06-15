package stages

import (
	"agent_v3/internal/graph"
	"context"
	"fmt"
	"strings"
	"time"
	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/planner/builtin"
	"trpc.group/trpc-go/trpc-agent-go/runner"
)

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
7. **交通方式**: 自驾路线和距离
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
