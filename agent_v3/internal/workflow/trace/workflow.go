package trace

import (
	"agent_v3/internal/config"
	"agent_v3/internal/graph"
	amaptools "agent_v3/internal/tools/amap"
	"context"
	"fmt"
	"sort"
	"strings"
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

type RouteDisplayContext struct {
	PhaseID        string
	PhaseSeq       int
	PhaseName      string
	DayID          string
	DayIndex       int
	SegmentIndex   int
	ConnectionType string
}

type routeDisplayContext = RouteDisplayContext

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
	resp, err := amaptools.GeocodeAddress(ctx, config.Cfg.Amap, amaptools.AmapGeocodeInput{Address: label, City: city})
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
