package stages

import (
	"agent_v3/internal/config"
	"agent_v3/internal/graph"
	amaptools "agent_v3/internal/tools/amap"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"sort"
	"strings"
	"time"
	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/planner/builtin"
	"trpc.group/trpc-go/trpc-agent-go/runner"
)

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

	type dayRunInfo struct {
		id  string
		ctx dayExpansionContext
	}
	dayInfos := make([]dayRunInfo, 0, totalDays)
	dayContexts := dayExpansionContextsFromOverview(overview)
	for _, d := range overview.Days {
		if id, ok := d["id"].(string); ok && id != "" {
			ctx := dayContexts[id]
			if ctx.DayIndex == 0 {
				ctx.DayIndex = int(getFloat(d, "dayIndex"))
			}
			ctx.GeoConstraint = geoConstraint
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

	var previousDayID string
	var previousDayCtx dayExpansionContext
	var previousPOIs []graph.POIInput
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
		writtenPOIs, err := a.verifyDayPOIs(ctx, dayID, trace, dayCtx)
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
		if len(previousPOIs) > 0 && len(writtenPOIs) > 0 {
			previousSorted := sortedPOIsByVisitOrder(previousPOIs)
			currentSorted := sortedPOIsByVisitOrder(writtenPOIs)
			from := previousSorted[len(previousSorted)-1]
			to := currentSorted[0]
			parentNodeID := previousDayID + "-to-" + dayID
			route := buildRouteBetweenPOIsDirect(ctx, from, to, trace, parentNodeID)
			enrichRouteDisplayMetadata(&route, from, to, routeDisplayContext{
				PhaseID:        previousDayCtx.PhaseID,
				PhaseSeq:       previousDayCtx.PhaseSeq,
				PhaseName:      previousDayCtx.PhaseName,
				DayID:          previousDayID,
				DayIndex:       previousDayCtx.DayIndex,
				SegmentIndex:   1,
				ConnectionType: "cross_day",
			})
			if err := a.graphClient.WriteRoute(ctx, route); err != nil {
				log.Errorf("[workflow-runner] write cross-day route: %v", err)
			}
			emitRouteSegmentMapEvents(ctx, trace, "day", parentNodeID, []graph.POIInput{from, to}, []graph.RouteInput{route}, routeDisplayContext{
				PhaseID:        previousDayCtx.PhaseID,
				PhaseSeq:       previousDayCtx.PhaseSeq,
				PhaseName:      previousDayCtx.PhaseName,
				DayID:          previousDayID,
				DayIndex:       previousDayCtx.DayIndex,
				SegmentIndex:   1,
				ConnectionType: "cross_day",
			})
		}
		if len(writtenPOIs) > 0 {
			previousDayID = dayID
			previousDayCtx = dayCtx
			previousPOIs = writtenPOIs
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

	// Build a structured prompt for amap-agent
	prompt := fmt.Sprintf(`请验证并完善以下旅行日的 POI 安排：

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
4. 对 POI 之间的路线调用 amap_route_driving 获取距离和耗时
5. 返回结构化的 POI 数据和路线数据

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
      "estimatedCost": 20,
      "polyline": "[[116.1,39.9],[116.2,39.95]]",
      "notes": "路线选择理由"
    }
  ]
}`, day.Date, day.DayIndex, day.Theme, day.PrimaryArea, day.StartPoint, day.RouteOverview, day.ThinkingNotes)

	// Run amap-agent
	amapResult, err := runAmapAgentStandalone(ctx, prompt)
	if err != nil {
		log.Warnf("[workflow-runner] amap-agent day %s failed, using deterministic POI fallback: %v", dayID, err)
		emitDayExpansionNotice(ctx, trace, dayID, "结构化地点验证未完成", "改用确定性的地图搜索兜底，只接受带真实坐标的地点。", "review")
	}

	// Parse POIs, then re-check coordinates with direct geocoding before exposing them.
	pois, _ := parseAmapPOIResult(amapResult)
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
	routes := buildDayRoutesDirect(ctx, pois, trace, dayID, dayContext)

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
	amapTools := amaptools.NewDefaultAmapTools()
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
