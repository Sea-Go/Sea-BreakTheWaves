package orchestrator

import (
	"fmt"
	"strings"

	workflowruntime "agent_v3/internal/workflow/runtime"
)

func mergeSnapshotFromMap(snap *workflowruntime.TravelRequirementSnapshot, m map[string]any) {
	setString := func(key string, setter func(string)) {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			setter(strings.TrimSpace(v))
		}
	}

	setString("destination_scope", func(v string) { snap.DestinationScope = v })
	setString("start_date", func(v string) { snap.StartDate = v })
	setString("end_date", func(v string) { snap.EndDate = v })
	setString("start_city", func(v string) { snap.StartCity = v })
	setString("end_city", func(v string) { snap.EndCity = v })
	setString("budget_total", func(v string) { snap.BudgetTotal = v })
	setString("budget_monthly", func(v string) { snap.BudgetMonthly = v })
	setString("transport_mode", func(v string) { snap.TransportMode = v })
	setString("pace", func(v string) { snap.Pace = v })
	setString("high_altitude_acceptance", func(v string) { snap.HighAltitudeAcceptance = v })
	setString("daily_driving_preference", func(v string) { snap.DailyDrivingPreference = v })
	setString("accommodation_style", func(v string) { snap.AccommodationStyle = v })

	if v, ok := m["total_days"].(float64); ok && v > 0 {
		snap.TotalDays = int(v)
	}
	if v, ok := m["total_days"].(int); ok && v > 0 {
		snap.TotalDays = v
	}

	if arr, ok := m["travel_style"].([]any); ok {
		snap.TravelStyle = anySliceToStringSlice(arr)
	}
	if arr, ok := m["food_preference"].([]any); ok {
		snap.FoodPreference = anySliceToStringSlice(arr)
	}
	if arr, ok := m["must_visit"].([]any); ok {
		snap.MustVisit = anySliceToStringSlice(arr)
	}
	if arr, ok := m["avoid_places"].([]any); ok {
		snap.AvoidPlaces = anySliceToStringSlice(arr)
	}
	if arr, ok := m["special_constraints"].([]any); ok {
		snap.SpecialConstraints = anySliceToStringSlice(arr)
	}
	if arr, ok := m["destination_anchors"].([]any); ok {
		snap.DestinationAnchors = anySliceToDestinationAnchors(arr)
	}
}

func anySliceToStringSlice(arr []any) []string {
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s := strings.TrimSpace(fmt.Sprint(item))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func anySliceToDestinationAnchors(arr []any) []workflowruntime.DestinationAnchorSnapshot {
	out := make([]workflowruntime.DestinationAnchorSnapshot, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mapString := func(key string) string {
			if v, ok := m[key]; ok && v != nil {
				return strings.TrimSpace(fmt.Sprint(v))
			}
			return ""
		}
		anchor := workflowruntime.DestinationAnchorSnapshot{
			Destination: mapString("destination"),
			Name:        mapString("name"),
			Kind:        mapString("kind"),
			Origin:      mapString("origin"),
			Query:       mapString("query"),
			Reason:      mapString("reason"),
		}
		if anchor.Name == "" {
			continue
		}
		if v, ok := m["priority"].(float64); ok {
			anchor.Priority = int(v)
		}
		if v, ok := m["must_cover"].(bool); ok {
			anchor.MustCover = v
		}
		if arr, ok := m["themes"].([]any); ok {
			anchor.Themes = anySliceToStringSlice(arr)
		}
		out = append(out, anchor)
	}
	return out
}

// ═══════════════════════════════════════════════════════════════
// 问题生成函数
// ═══════════════════════════════════════════════════════════════
