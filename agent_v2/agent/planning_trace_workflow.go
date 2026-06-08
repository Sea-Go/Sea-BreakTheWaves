package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"agent_v2/config"
	"agent_v2/graph"
	"agent_v2/tools"
)

func emitRequirementMapEvents(ctx context.Context, emitter *TraceEmitter, req TravelRequirementSnapshot) {
	if emitter == nil {
		return
	}
	startCity := strings.TrimSpace(req.StartCity)
	if startCity == "" {
		return
	}
	point, formattedAddress, err := exactMapPointFromGeocode(ctx, startCity, startCity, "start")
	if err != nil {
		emitter.Emit(ctx, PublicPlanningEvent{
			Type:           EventPlanningStageChanged,
			Stage:          "macro_planning",
			Status:         "running",
			PublicAction:   "起点待定位",
			ThoughtSummary: fmt.Sprintf("未取得 %s 的精确坐标，暂不在地图上绘制起点。", startCity),
			RecordedFacts:  []string{fmt.Sprintf("出发地：%s", startCity)},
		})
		return
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapPointAdded,
		Level:          "overview",
		NodeID:         "start-" + startCity,
		Status:         "active",
		PublicAction:   "确认出发地",
		ThoughtSummary: "出发地已经确认，先把起点固定在地图上，后续路线会围绕它展开。",
		RecordedFacts: []string{
			fmt.Sprintf("出发地：%s", startCity),
			fmt.Sprintf("交通方式：%s", defaultIfEmpty(req.TransportMode, "待确认")),
		},
		Point: point,
		Popup: &PublicMapPopup{
			Title:   "出发地已确认",
			Content: fmt.Sprintf("%s，作为本次旅行规划的起点。", defaultIfEmpty(formattedAddress, startCity)),
		},
	})
}

func emitPhaseOverviewMapEvents(ctx context.Context, emitter *TraceEmitter, overview *graph.TripOverview) {
	if emitter == nil || overview == nil {
		return
	}
	phases := append([]map[string]any(nil), overview.Phases...)
	sort.Slice(phases, func(i, j int) bool {
		return int(getFloat(phases[i], "seq")) < int(getFloat(phases[j], "seq"))
	})

	facts := make([]string, 0, len(phases))
	for i, phase := range phases {
		name := getStr(phase, "name")
		region := getStr(phase, "region")
		if region == "" {
			region = name
		}
		seq := int(getFloat(phase, "seq"))
		if seq == 0 {
			seq = i + 1
		}
		facts = append(facts, fmt.Sprintf("阶段 %d：%s，%s ~ %s，约 %.0f 天", seq, region, getStr(phase, "startDate"), getStr(phase, "endDate"), getFloat(phase, "dayCount")))
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapScopeChanged,
		Level:          "overview",
		PublicAction:   "建立大规划方向",
		ThoughtSummary: "大规划已生成；阶段区域不是精确地点，暂不绘制为地图点。",
		RecordedFacts:  facts,
		Viewport:       &PublicMapViewport{Center: [2]float64{104.2, 35.9}, Zoom: 4},
	})
}

func emitGraphSplittingMapEvents(ctx context.Context, emitter *TraceEmitter, overview *graph.TripOverview) {
	if emitter == nil || overview == nil {
		return
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapScopeChanged,
		Level:          "phase",
		PublicAction:   "进入小规划拆分",
		ThoughtSummary: "大规划已经建立，现在开始把阶段拆成月、周、日，地图保留上层方向并准备展开细节。",
		Viewport:       &PublicMapViewport{Center: [2]float64{104.2, 35.9}, Zoom: 4},
	})
}

func emitMacroRouteMapEvents(ctx context.Context, emitter *TraceEmitter, req TravelRequirementSnapshot, overview *graph.TripOverview) {
	if emitter == nil || overview == nil {
		return
	}
	labels := []string{}
	if strings.TrimSpace(req.StartCity) != "" {
		labels = append(labels, strings.TrimSpace(req.StartCity))
	}
	phases := append([]map[string]any(nil), overview.Phases...)
	sort.Slice(phases, func(i, j int) bool {
		return int(getFloat(phases[i], "seq")) < int(getFloat(phases[j], "seq"))
	})
	for _, phase := range phases {
		label := firstNonEmptyString(getStr(phase, "region"), getStr(phase, "name"))
		if label != "" {
			labels = append(labels, label)
		}
	}
	labels = uniqueLimitedStrings(labels, 12)
	if len(labels) < 2 {
		return
	}

	points := make([]PublicPlanningEvent, 0, len(labels))
	anchors := make([]macroRouteAnchor, 0, len(labels))
	for _, label := range labels {
		point, address, err := exactMapPointFromGeocode(ctx, label, label, "city_anchor")
		if err != nil {
			emitter.Emit(ctx, PublicPlanningEvent{
				Type:           EventMapAnnotationAdded,
				Level:          "overview",
				Status:         "review",
				PublicAction:   "宏观路线锚点待定位",
				ThoughtSummary: fmt.Sprintf("未取得 %s 的精确坐标，暂不绘制这个宏观锚点。", label),
				Annotation: &PublicMapAnnotation{
					ID:      stablePlanningAnnotationID("macro-anchor-missing", emitter.RunID(), label),
					Kind:    "thought",
					Source:  "planning",
					Title:   "宏观锚点未定位",
					Summary: fmt.Sprintf("%s 暂无精确坐标，地图不会用粗略区域点替代。", label),
					Status:  "review",
					Anchor:  PublicMapAnnotationAnchor{Type: "scope", Label: label},
				},
			})
			continue
		}
		nodeID := "anchor-" + hashGuideText(label)[:12]
		anchors = append(anchors, macroRouteAnchor{NodeID: nodeID, Label: label, Point: point})
		points = append(points, PublicPlanningEvent{
			Type:         EventMapPointAdded,
			Level:        "overview",
			NodeID:       nodeID,
			Status:       "active",
			PublicAction: "标注宏观路线锚点",
			Point:        point,
			Popup: &PublicMapPopup{
				Title:   label,
				Content: defaultIfEmpty(address, "已取得精确坐标，作为宏观路线锚点。"),
			},
		})
	}
	if len(points) > 0 {
		emitter.Emit(ctx, PublicPlanningEvent{
			Type:           EventMapBatch,
			Level:          "overview",
			PublicAction:   "展示宏观路线锚点",
			ThoughtSummary: "只展示已成功定位的城市/区域锚点，未定位内容保留为文字证据。",
			Events:         points,
		})
	}
	if len(anchors) < 2 {
		return
	}

	routeEvents := make([]PublicPlanningEvent, 0, (len(anchors)-1)*3)
	for i := 0; i < len(anchors)-1; i++ {
		from := anchors[i]
		to := anchors[i+1]
		routeID := "route-macro-" + hashGuideText(from.NodeID + "-" + to.NodeID)[:12]
		route := PublicRouteCandidate{
			ID:             routeID,
			Label:          fmt.Sprintf("%s → %s", from.Label, to.Label),
			Status:         "selected",
			Mode:           defaultIfEmpty(req.TransportMode, "mixed"),
			Accuracy:       "directional",
			Source:         "macro_anchor",
			SegmentIndex:   i + 1,
			FromNodeID:     from.NodeID,
			ToNodeID:       to.NodeID,
			ConnectionType: "phase",
			Polyline:       [][2]float64{{from.Point.Lng, from.Point.Lat}, {to.Point.Lng, to.Point.Lat}},
			Reason:         "宏观阶段方向线，仅由精确地理编码锚点组成。",
		}
		routeEvents = append(routeEvents,
			PublicPlanningEvent{
				Type:         EventRouteCandidateAdded,
				Level:        "overview",
				RouteID:      routeID,
				Status:       "candidate",
				PublicAction: "生成宏观候选路线",
				Route:        &route,
			},
			PublicPlanningEvent{
				Type:           EventRouteSelected,
				Level:          "overview",
				RouteID:        routeID,
				Status:         "selected",
				PublicAction:   "选中宏观路线方向",
				ThoughtSummary: "宏观路线只表达阶段之间的方向，精确行车路线会在日级 POI 验证后展开。",
				Route:          &route,
			},
			PublicPlanningEvent{
				Type:         EventMapAnnotationAdded,
				Level:        "overview",
				RouteID:      routeID,
				Status:       "selected",
				PublicAction: "记录宏观路线取舍",
				Annotation: &PublicMapAnnotation{
					ID:      stablePlanningAnnotationID("macro-route-decision", routeID),
					Kind:    "decision",
					Source:  "planning",
					Title:   "宏观路线方向",
					Summary: route.Reason,
					Status:  "selected",
					Reasons: []string{"锚点坐标精确", "用于展示阶段方向", "不替代日级路线"},
					Anchor:  PublicMapAnnotationAnchor{Type: "route", RouteID: routeID, Label: route.Label},
				},
			},
		)
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapBatch,
		Level:          "overview",
		PublicAction:   "展示宏观路线方向",
		ThoughtSummary: "已用精确锚点连接阶段方向，后续会展开日级路线。",
		Events:         routeEvents,
	})
}

type macroRouteAnchor struct {
	NodeID string
	Label  string
	Point  *PublicMapPoint
}

type routeDisplayContext struct {
	PhaseID        string
	PhaseSeq       int
	PhaseName      string
	DayID          string
	DayIndex       int
	SegmentIndex   int
	ConnectionType string
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func exactMapPointFromGeocode(ctx context.Context, label, city, kind string) (*PublicMapPoint, string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return nil, "", fmt.Errorf("empty label")
	}
	resp, err := tools.GeocodeAddress(ctx, config.Cfg.Amap, tools.AmapGeocodeInput{Address: label, City: city})
	if err != nil {
		return nil, "", err
	}
	if !resp.OK {
		return nil, "", fmt.Errorf("geocode failed: %s", resp.Info)
	}
	geocodes, _ := resp.Raw["geocodes"].([]any)
	if len(geocodes) == 0 {
		return nil, "", fmt.Errorf("no geocode result for %s", label)
	}
	geo, _ := geocodes[0].(map[string]any)
	location := stringFromAny(geo["location"])
	lng, lat, err := parseAmapLngLat(location)
	if err != nil {
		return nil, "", err
	}
	if !isValidLngLat(lng, lat) {
		return nil, "", fmt.Errorf("invalid coordinate %.6f,%.6f", lng, lat)
	}
	address := stringFromAny(geo["formatted_address"])
	point := &PublicMapPoint{
		Lng:      lng,
		Lat:      lat,
		Label:    label,
		Kind:     kind,
		Accuracy: "exact",
		Source:   "geocode",
		Address:  address,
	}
	return point, address, nil
}

func emitExactPOIMapBatch(ctx context.Context, emitter *TraceEmitter, level, parentNodeID string, pois []graph.POIInput, contexts ...routeDisplayContext) {
	if emitter == nil || len(pois) == 0 {
		return
	}
	meta := routeDisplayContext{}
	if len(contexts) > 0 {
		meta = contexts[0]
	}
	events := make([]PublicPlanningEvent, 0, len(pois))
	for i, poi := range pois {
		if strings.TrimSpace(poi.Name) == "" || !isValidLngLat(poi.Lng, poi.Lat) {
			continue
		}
		nodeID := poi.ID
		if nodeID == "" {
			nodeID = fmt.Sprintf("%s-poi-%d", parentNodeID, i+1)
		}
		description := publicPOIDescription(poi)
		content := publicPOIPopupContent(poi, description)
		events = append(events, PublicPlanningEvent{
			Type:         EventMapPointAdded,
			Level:        defaultIfEmpty(level, "day"),
			NodeID:       nodeID,
			Status:       "active",
			PublicAction: "标注真实地点",
			Point: &PublicMapPoint{
				Lng:           poi.Lng,
				Lat:           poi.Lat,
				Label:         poi.Name,
				Kind:          "poi",
				Accuracy:      "exact",
				Source:        "stored_poi",
				Address:       poi.Address,
				City:          poi.City,
				District:      poi.District,
				Category:      poi.Type,
				Description:   description,
				Notes:         poi.Notes,
				VisitOrder:    poi.VisitOrder,
				StartTime:     poi.StartTime,
				EndTime:       poi.EndTime,
				DurationMin:   poi.Duration,
				EstimatedCost: poi.EstimatedCost,
				PhaseID:       meta.PhaseID,
				PhaseSeq:      meta.PhaseSeq,
				PhaseName:     meta.PhaseName,
				DayID:         defaultIfEmpty(meta.DayID, parentNodeID),
				DayIndex:      meta.DayIndex,
			},
			Popup: &PublicMapPopup{
				Title:   poi.Name,
				Content: defaultIfEmpty(content, "已确认真实坐标。"),
			},
		})
	}
	if len(events) == 0 {
		return
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapBatch,
		Level:          defaultIfEmpty(level, "day"),
		NodeID:         parentNodeID,
		PublicAction:   "展示真实地点",
		ThoughtSummary: "已将验证过坐标的地点加入地图。",
		Events:         events,
	})
}

func publicPOIDescription(poi graph.POIInput) string {
	description := strings.TrimSpace(poi.Description)
	if description != "" {
		return description
	}
	if strings.TrimSpace(poi.Notes) != "" && strings.TrimSpace(poi.Notes) != "已确认真实坐标。" {
		return strings.TrimSpace(poi.Notes)
	}
	parts := []string{}
	if strings.TrimSpace(poi.Type) != "" {
		parts = append(parts, fmt.Sprintf("类型：%s", strings.TrimSpace(poi.Type)))
	}
	locationParts := []string{}
	if strings.TrimSpace(poi.City) != "" {
		locationParts = append(locationParts, strings.TrimSpace(poi.City))
	}
	if strings.TrimSpace(poi.District) != "" && strings.TrimSpace(poi.District) != strings.TrimSpace(poi.City) {
		locationParts = append(locationParts, strings.TrimSpace(poi.District))
	}
	if len(locationParts) > 0 {
		parts = append(parts, "位置："+strings.Join(locationParts, " "))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "；")
}

func publicPOIPopupContent(poi graph.POIInput, description string) string {
	lines := []string{}
	if strings.TrimSpace(description) != "" {
		lines = append(lines, strings.TrimSpace(description))
	}
	if strings.TrimSpace(poi.Address) != "" {
		lines = append(lines, "地址："+strings.TrimSpace(poi.Address))
	}
	schedule := []string{}
	if poi.VisitOrder > 0 {
		schedule = append(schedule, fmt.Sprintf("第 %d 站", poi.VisitOrder))
	}
	if strings.TrimSpace(poi.StartTime) != "" || strings.TrimSpace(poi.EndTime) != "" {
		schedule = append(schedule, fmt.Sprintf("%s-%s", strings.TrimSpace(poi.StartTime), strings.TrimSpace(poi.EndTime)))
	}
	if poi.Duration > 0 {
		schedule = append(schedule, fmt.Sprintf("停留约 %d 分钟", poi.Duration))
	}
	if len(schedule) > 0 {
		lines = append(lines, "安排："+strings.Join(schedule, " · "))
	}
	if poi.EstimatedCost > 0 {
		lines = append(lines, fmt.Sprintf("预估费用：%.0f 元", poi.EstimatedCost))
	}
	return strings.Join(lines, "\n")
}

func emitRouteSegmentMapEvents(ctx context.Context, emitter *TraceEmitter, level, parentNodeID string, pois []graph.POIInput, routes []graph.RouteInput, contexts ...routeDisplayContext) {
	if emitter == nil || len(routes) == 0 {
		return
	}
	meta := routeDisplayContext{}
	if len(contexts) > 0 {
		meta = contexts[0]
	}
	poiByID := map[string]graph.POIInput{}
	for _, poi := range pois {
		if strings.TrimSpace(poi.ID) != "" && isValidLngLat(poi.Lng, poi.Lat) {
			poiByID[poi.ID] = poi
		}
	}
	events := make([]PublicPlanningEvent, 0, len(routes)*3)
	for _, route := range routes {
		from, okFrom := poiByID[route.FromPOIID]
		to, okTo := poiByID[route.ToPOIID]
		if !okFrom || !okTo {
			continue
		}
		routeID := "route-" + hashGuideText(parentNodeID + route.FromPOIID + route.ToPOIID)[:16]
		label := fmt.Sprintf("%s → %s", from.Name, to.Name)
		polyline := publicPolylineFromRoute(route, from, to)
		accuracy := defaultIfEmpty(route.Accuracy, "connector")
		source := defaultIfEmpty(route.Source, "exact_point_connector")
		connectionType := defaultIfEmpty(route.ConnectionType, defaultIfEmpty(meta.ConnectionType, "day_segment"))
		publicRoute := PublicRouteCandidate{
			ID:             routeID,
			Label:          label,
			Status:         "selected",
			Mode:           defaultIfEmpty(route.TransportMode, "mixed"),
			Accuracy:       accuracy,
			Source:         source,
			PhaseID:        defaultIfEmpty(route.PhaseID, meta.PhaseID),
			PhaseSeq:       firstPositiveInt(route.PhaseSeq, meta.PhaseSeq),
			PhaseName:      defaultIfEmpty(route.PhaseName, meta.PhaseName),
			DayID:          defaultIfEmpty(route.DayID, defaultIfEmpty(meta.DayID, parentNodeID)),
			DayIndex:       firstPositiveInt(route.DayIndex, meta.DayIndex),
			SegmentIndex:   firstPositiveInt(route.SegmentIndex, meta.SegmentIndex),
			FromNodeID:     defaultIfEmpty(route.FromNodeID, from.ID),
			ToNodeID:       defaultIfEmpty(route.ToNodeID, to.ID),
			ConnectionType: connectionType,
			DistanceMeters: route.DistanceMeters,
			DurationMin:    route.DurationMin,
			EstimatedCost:  route.EstimatedCost,
			Polyline:       polyline,
			Reason:         defaultIfEmpty(route.Notes, "基于已验证 POI 坐标和路线数据连接。"),
		}
		selectedAction := "选中日级路线"
		selectedSummary := "该路线连接已验证的真实 POI，作为当天动线的一部分。"
		annotationTitle := "路线权衡"
		if accuracy == "connector" {
			selectedAction = "连接待复核路线"
			selectedSummary = "这段路线暂未取得完整轨迹，先用真实地点坐标保持线路连续，并标记为待复核。"
			annotationTitle = "路线待复核"
		}
		events = append(events,
			PublicPlanningEvent{
				Type:         EventRouteCandidateAdded,
				Level:        defaultIfEmpty(level, "day"),
				NodeID:       parentNodeID,
				RouteID:      routeID,
				Status:       "candidate",
				PublicAction: "生成路线连接",
				Route:        &publicRoute,
			},
			PublicPlanningEvent{
				Type:           EventRouteSelected,
				Level:          defaultIfEmpty(level, "day"),
				NodeID:         parentNodeID,
				RouteID:        routeID,
				Status:         "selected",
				PublicAction:   selectedAction,
				ThoughtSummary: selectedSummary,
				Route:          &publicRoute,
			},
			PublicPlanningEvent{
				Type:         EventMapAnnotationAdded,
				Level:        defaultIfEmpty(level, "day"),
				NodeID:       parentNodeID,
				RouteID:      routeID,
				Status:       "selected",
				PublicAction: "记录路线权衡",
				Annotation: &PublicMapAnnotation{
					ID:      stablePlanningAnnotationID("route-decision", routeID),
					Kind:    "decision",
					Source:  "planning",
					Title:   annotationTitle,
					Summary: routeDecisionSummary(route, label),
					Status:  "selected",
					Reasons: []string{"连接真实 POI", "用于当天动线", "保留距离和耗时信号"},
					Anchor:  PublicMapAnnotationAnchor{Type: "route", RouteID: routeID, Label: label},
				},
			},
		)
	}
	if len(events) == 0 {
		return
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapBatch,
		Level:          defaultIfEmpty(level, "day"),
		NodeID:         parentNodeID,
		PublicAction:   "展示日级路线",
		ThoughtSummary: fmt.Sprintf("已将 %d 条日级路线加入地图。", len(events)/3),
		Events:         events,
	})
}

func publicPolylineFromRoute(route graph.RouteInput, from, to graph.POIInput) [][2]float64 {
	if strings.TrimSpace(route.Polyline) != "" {
		var points [][2]float64
		if err := json.Unmarshal([]byte(route.Polyline), &points); err == nil && len(points) >= 2 {
			return points
		}
	}
	return [][2]float64{{from.Lng, from.Lat}, {to.Lng, to.Lat}}
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func routeDecisionSummary(route graph.RouteInput, label string) string {
	parts := []string{label}
	if route.DistanceMeters > 0 {
		parts = append(parts, fmt.Sprintf("距离约 %.1f 公里", route.DistanceMeters/1000))
	}
	if route.DurationMin > 0 {
		parts = append(parts, fmt.Sprintf("耗时约 %.0f 分钟", route.DurationMin))
	}
	if route.EstimatedCost > 0 {
		parts = append(parts, fmt.Sprintf("费用约 %.0f", route.EstimatedCost))
	}
	if route.Notes != "" {
		parts = append(parts, route.Notes)
	}
	return strings.Join(parts, "，")
}

func emitReviewAnnotation(ctx context.Context, emitter *TraceEmitter, level, nodeID, label, agentName, anchorType string, review graph.ReviewInput) {
	if emitter == nil {
		return
	}
	status := "review"
	if review.Passed {
		status = "selected"
	}
	title := defaultIfEmpty(agentName, "审核结果")
	summary := review.Summary
	if summary == "" {
		summary = fmt.Sprintf("%s：%s，得分 %d。", defaultIfEmpty(label, nodeID), passLabel(review.Passed), review.Score)
	}
	evidence := reviewEvidence(review)
	reasons := []string{defaultIfEmpty(review.Level, defaultIfEmpty(level, "day")), defaultIfEmpty(review.Dimension, "综合审核")}
	anchor := PublicMapAnnotationAnchor{Type: defaultIfEmpty(anchorType, "scope"), NodeID: nodeID, Label: defaultIfEmpty(label, nodeID)}
	if anchor.Type == "point" {
		anchor.NodeID = nodeID
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapAnnotationAdded,
		Level:          defaultIfEmpty(level, "day"),
		NodeID:         nodeID,
		Status:         status,
		PublicAction:   "展示审核结果",
		ThoughtSummary: "审核结果以公开摘要展示，用于说明规划质量和需要注意的问题。",
		Annotation: &PublicMapAnnotation{
			ID:       stablePlanningAnnotationID("review", nodeID, review.Level, review.Dimension, fmt.Sprint(review.Score), summary),
			Kind:     "review",
			Source:   "review",
			Title:    title,
			Summary:  truncateGuideText(summary, maxGuideAnnotationSummary),
			Score:    float64(review.Score),
			Status:   status,
			Tags:     []string{"审核", passLabel(review.Passed), defaultIfEmpty(review.Dimension, "综合")},
			Reasons:  reasons,
			Evidence: evidence,
			Anchor:   anchor,
		},
	})
}

func reviewEvidence(review graph.ReviewInput) []string {
	values := []string{}
	values = append(values, review.CriticalIssues...)
	values = append(values, review.Issues...)
	values = append(values, review.Suggestions...)
	for _, violation := range review.ConstraintViolations {
		text := strings.TrimSpace(strings.Join([]string{
			violation.Dimension,
			violation.Rule,
			violation.Actual,
			violation.Threshold,
			violation.Severity,
		}, " "))
		if text != "" {
			values = append(values, text)
		}
	}
	return limitStrings(values, maxGuideAnnotationEvidence)
}

func passLabel(passed bool) string {
	if passed {
		return "通过"
	}
	return "待调整"
}

func publicReviewAgentLabel(name string) string {
	switch name {
	case "workflow":
		return "流程审核"
	case "thinking":
		return "思路审核"
	case "content":
		return "内容审核"
	case "output":
		return "输出审核"
	case "laziness":
		return "完整性审核"
	default:
		return defaultIfEmpty(name, "审核结果")
	}
}

func parseAmapLngLat(location string) (float64, float64, error) {
	parts := strings.Split(strings.TrimSpace(location), ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid location %q", location)
	}
	lng, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, err
	}
	lat, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, err
	}
	return lng, lat, nil
}

func isValidLngLat(lng, lat float64) bool {
	if math.IsNaN(lng) || math.IsNaN(lat) || math.IsInf(lng, 0) || math.IsInf(lat, 0) {
		return false
	}
	if lng == 0 && lat == 0 {
		return false
	}
	return lng >= -180 && lng <= 180 && lat >= -90 && lat <= 90
}

func stringFromAny(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
