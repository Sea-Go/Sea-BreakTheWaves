package agent

import (
	"strings"
	"testing"

	"agent_v2/graph"
	"agent_v2/tools"
)

func TestNaturalHighlandStructuredSnapshotDerivesAnchorsAndMissingChecks(t *testing.T) {
	snap := TravelRequirementSnapshot{
		DestinationScope:   "香格里拉 稻城亚丁 林芝",
		StartCity:          "丽江",
		TotalDays:          7,
		BudgetTotal:        "2w",
		TransportMode:      "自驾",
		TravelStyle:        []string{"自然风光"},
		Pace:               "均衡",
		AccommodationStyle: "经济舒适",
		FoodPreference:     []string{"当地特色"},
	}
	snap.DestinationAnchors = deriveDestinationAnchors(snap, "")

	for _, want := range []string{"香格里拉", "稻城亚丁", "林芝", "南迦巴瓦峰", "梅里雪山"} {
		if !anchorNamesContain(snap.DestinationAnchors, want) {
			t.Fatalf("expected anchor %q in %#v", want, snap.DestinationAnchors)
		}
	}

	decision := buildPlanningDecision(snap)
	if decision.Ready {
		t.Fatalf("request missing start date/altitude/driving details should ask first: %#v", decision)
	}
	for _, want := range []string{"start_date", "high_altitude_acceptance", "daily_driving_preference"} {
		if !stringSliceContains(decision.MissingP1, want) {
			t.Fatalf("missing P1 should contain %s, got %#v", want, decision.MissingP1)
		}
	}
}

func TestBuildDayPOISearchSpecsPreferExactNaturalAnchor(t *testing.T) {
	day := graph.DayNode{
		ID:            "day-linzhi-1",
		DayIndex:      5,
		Theme:         "林芝自然风光：南迦巴瓦峰",
		PrimaryArea:   "南迦巴瓦峰",
		RouteOverview: "围绕南迦巴瓦峰展开自然风光体验",
		ThinkingNotes: "anchor=南迦巴瓦峰；destination=林芝",
	}
	specs := buildDayPOISearchSpecs(day, dayExpansionContext{PhaseRegion: "林芝", PhaseTheme: "自然风光"})
	if len(specs) == 0 {
		t.Fatal("expected search specs")
	}
	if !strings.Contains(specs[0].Keyword, "南迦巴瓦") || !specs[0].MainScenic {
		t.Fatalf("first spec should target exact natural anchor, got %#v", specs[0])
	}
	if strings.Contains(specs[0].Keyword, "林芝 景点") {
		t.Fatalf("first spec is too generic: %#v", specs[0])
	}
	last := specs[len(specs)-1]
	if last.Kind != "住宿" {
		t.Fatalf("logistics should stay at the end, got %#v", last)
	}
}

func TestPOISearchFiltersGenericCityForNaturalAnchor(t *testing.T) {
	resp := tools.AmapResponse{
		OK: true,
		Raw: map[string]any{
			"pois": []any{
				map[string]any{
					"id":       "generic",
					"name":     "林芝市政府",
					"type":     "政府机构",
					"location": "94.360000,29.650000",
					"address":  "林芝市",
				},
				map[string]any{
					"id":       "scenic",
					"name":     "南迦巴瓦峰观景台",
					"type":     "风景名胜",
					"location": "94.900000,29.620000",
					"address":  "林芝市米林市",
				},
			},
		},
	}
	pois := poiInputsFromAmapSearch(resp, dayPOISearchSpec{
		Keyword:    "林芝 南迦巴瓦 自驾 攻略",
		City:       "林芝",
		Kind:       "景点",
		Reason:     "核心自然锚点",
		AnchorName: "南迦巴瓦峰",
		MainScenic: true,
	})
	if len(pois) != 1 {
		t.Fatalf("expected only exact scenic candidate, got %#v", pois)
	}
	if pois[0].Name != "南迦巴瓦峰观景台" {
		t.Fatalf("poi = %s, want 南迦巴瓦峰观景台", pois[0].Name)
	}
}

func anchorNamesContain(anchors []DestinationAnchorSnapshot, name string) bool {
	for _, anchor := range anchors {
		if anchor.Name == name {
			return true
		}
	}
	return false
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
