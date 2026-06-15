package trace

import (
	"agent_v3/internal/graph"
	"context"
	"fmt"
	"strings"
)

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
