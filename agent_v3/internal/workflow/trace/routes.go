package trace

import (
	"agent_v3/internal/graph"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

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
