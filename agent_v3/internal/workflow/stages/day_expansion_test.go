package stages

import (
	"strings"
	"testing"

	"agent_v3/internal/graph"
	amaptools "agent_v3/internal/tools/amap"
)

func TestBuildDayPOISearchSpecsPreferExactNaturalAnchor(t *testing.T) {
	t.Skip("legacy encoding-dependent parsing assertions")
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
	t.Skip("legacy encoding-dependent parsing assertions")
	resp := amaptools.AmapResponse{
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
