package trace

import (
	"strings"

	domaintravel "agent_v3/internal/domain/travel"
	"agent_v3/internal/graph"
)

func getStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getFloat(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case float64:
			return val
		case int64:
			return float64(val)
		case int:
			return float64(val)
		}
	}
	return 0
}

func deriveAnchorsFromTripOverview(overview *graph.TripOverview) []DestinationAnchorSnapshot {
	if overview == nil {
		return nil
	}
	snap := TravelRequirementSnapshot{
		DestinationScope: joinNonEmpty(append([]string{
			overview.TripPlan.Name,
			overview.TripPlan.RawRequirements,
		}, overview.TripPlan.MustVisit...)...),
		TotalDays:     overview.TripPlan.TotalDays,
		TransportMode: overview.TripPlan.TransportMode,
		TravelStyle:   append([]string{overview.TripPlan.TravelStyle}, overview.TripPlan.Interests...),
		MustVisit:     append([]string(nil), overview.TripPlan.MustVisit...),
	}
	domaintravel.EnrichRequirementWithDeterministicFields(&snap, snap.DestinationScope)
	return snap.DestinationAnchors
}

func joinNonEmpty(values ...string) string {
	return compactGuideTopic(strings.Join(values, " "))
}
