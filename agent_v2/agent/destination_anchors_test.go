package agent

import (
	"strings"
	"testing"

	"agent_v2/graph"
	"agent_v2/tools"
)

func TestNaturalHighlandRequestEnrichmentAndQuestioning(t *testing.T) {
	msg := "帮我规划一下7天6晚的旅行，从丽江出发，2w，准备去香格里拉稻城亚丁，林芝，预算2w，自驾，自然风光，均衡"
	var snap TravelRequirementSnapshot
	enrichRequirementWithDeterministicFields(&snap, msg)

	if snap.StartCity != "丽江" {
		t.Fatalf("start city = %q, want 丽江", snap.StartCity)
	}
	if snap.TotalDays != 7 {
		t.Fatalf("total days = %d, want 7", snap.TotalDays)
	}
	if snap.TransportMode != "自驾" {
		t.Fatalf("transport = %q, want 自驾", snap.TransportMode)
	}
	for _, want := range []string{"香格里拉", "稻城亚丁", "林芝", "南迦巴瓦峰", "梅里雪山"} {
		if !anchorNamesContain(snap.DestinationAnchors, want) {
			t.Fatalf("expected anchor %q in %#v", want, snap.DestinationAnchors)
		}
	}

	decision := buildPlanningDecision(snap, 0, 2, msg)
	if decision.Ready {
		t.Fatalf("request missing start date/altitude/driving details should ask first: %#v", decision)
	}
	for _, want := range []string{"start_date", "high_altitude_acceptance", "daily_driving_preference"} {
		if !stringSliceContains(decision.MissingP1, want) {
			t.Fatalf("missing P1 should contain %s, got %#v", want, decision.MissingP1)
		}
	}
}

func TestExplicitDefaultStillAllowsNaturalHighlandPlanning(t *testing.T) {
	msg := "帮我规划一下7天6晚的旅行，从丽江出发，2w，准备去香格里拉稻城亚丁，林芝，自驾，自然风光，均衡，按默认直接规划"
	var snap TravelRequirementSnapshot
	enrichRequirementWithDeterministicFields(&snap, msg)

	decision := buildPlanningDecision(snap, 0, 2, msg)
	if !decision.Ready {
		t.Fatalf("explicit default should allow planning after P0 is filled: %#v", decision)
	}
}

func TestLatestTurnPreventsOldDefaultIntentFromSkippingNewQuestion(t *testing.T) {
	msg := "用户第1轮：按默认直接规划\n用户第2轮：帮我规划一下7天6晚的旅行，从丽江出发，2w，准备去香格里拉稻城亚丁，林芝，自驾，自然风光，均衡"
	var snap TravelRequirementSnapshot
	enrichRequirementWithDeterministicFields(&snap, latestUserTurnText(msg))

	if !isLikelyNewPlanningRequest(msg) {
		t.Fatal("expected latest full itinerary request to be detected as new planning intent")
	}
	decision := buildPlanningDecision(snap, 0, 2, msg)
	if decision.Ready {
		t.Fatalf("old default intent from earlier turn must not skip new follow-up: %#v", decision)
	}
}

func TestMergeAnswerParsesAltitudeAndLongDriveTogether(t *testing.T) {
	msg := "按默认日期，能接受高海拔/长途"
	if got := parseHighAltitudeAcceptance(msg); got == "" {
		t.Fatal("expected high altitude acceptance")
	}
	if got := parseDailyDrivingPreference(msg); got == "" {
		t.Fatal("expected long driving acceptance")
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
