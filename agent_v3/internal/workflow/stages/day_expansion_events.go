package stages

import (
	"agent_v3/internal/graph"
	"context"
	"strings"
)

func emitPOISearchCandidateAnnotation(ctx context.Context, emitter *TraceEmitter, dayID string, poi graph.POIInput, spec dayPOISearchSpec) {
	if emitter == nil {
		return
	}
	summary := strings.TrimSpace(strings.Join([]string{
		poi.Name,
		firstNonEmptyString(poi.Address, poi.District, poi.City),
		poi.Description,
		spec.Reason,
	}, "，"))
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapAnnotationAdded,
		Level:          "day",
		NodeID:         poi.ID,
		Status:         "selected",
		PublicAction:   "记录地点搜索结果",
		ThoughtSummary: "搜索结果带有真实坐标，因此可以进入地图点位层。",
		Annotation: &PublicMapAnnotation{
			ID:      stablePlanningAnnotationID("poi-search", dayID, poi.ID, poi.Name),
			Kind:    "map_search",
			Source:  "map_search",
			Title:   "地点搜索结果",
			Summary: truncateGuideText(summary, maxGuideAnnotationSummary),
			Status:  "selected",
			Tags:    []string{"地图搜索", defaultIfEmpty(poi.Type, "地点")},
			Reasons: []string{spec.Reason, "坐标精确", "用于当天动线"},
			Anchor: PublicMapAnnotationAnchor{
				Type:   "point",
				NodeID: poi.ID,
				Label:  poi.Name,
				Point: &PublicMapPoint{
					Lng:      poi.Lng,
					Lat:      poi.Lat,
					Label:    poi.Name,
					Kind:     "poi",
					Accuracy: "exact",
					Source:   "map_search",
					Address:  poi.Address,
				},
			},
		},
	})
}

func emitDayExpansionNotice(ctx context.Context, emitter *TraceEmitter, dayID, title, summary, status string) {
	if emitter == nil {
		return
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapAnnotationAdded,
		Level:          "day",
		NodeID:         dayID,
		Status:         defaultIfEmpty(status, "active"),
		PublicAction:   title,
		ThoughtSummary: summary,
		Annotation: &PublicMapAnnotation{
			ID:      stablePlanningAnnotationID("day-expansion-notice", dayID, title, summary),
			Kind:    "thought",
			Source:  "planning",
			Title:   title,
			Summary: truncateGuideText(summary, maxGuideAnnotationSummary),
			Status:  defaultIfEmpty(status, "active"),
			Anchor:  PublicMapAnnotationAnchor{Type: "scope", NodeID: dayID, Label: dayID},
		},
	})
}
