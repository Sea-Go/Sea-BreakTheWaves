package stages

import (
	"agent_v3/internal/graph"
	amaptools "agent_v3/internal/tools/amap"
	"github.com/google/uuid"
	"sort"
	"strings"
)

func buildDayPOISearchSpecs(day graph.DayNode, dayCtx dayExpansionContext) []dayPOISearchSpec {
	area := firstNonEmptyString(day.PrimaryArea, day.StartPoint, dayCtx.PhaseRegion, dayCtx.PhaseName)
	theme := firstNonEmptyString(day.Theme, dayCtx.PhaseTheme, area)
	base := firstNonEmptyString(area, theme)
	if base == "" {
		return nil
	}

	searchCity := area
	specs := make([]dayPOISearchSpec, 0, 8)
	dayText := strings.Join([]string{
		day.PrimaryArea,
		day.Theme,
		day.RouteOverview,
		day.ThinkingNotes,
		dayCtx.PhaseRegion,
		dayCtx.PhaseTheme,
	}, " ")
	naturalDay := isNaturalSceneryDay(day, dayCtx)
	for _, anchor := range anchorSearchTermsFromText(dayText) {
		specs = append(specs, dayPOISearchSpec{
			Keyword:    strings.TrimSpace(firstNonEmptyString(anchor.Query, anchor.Name+" 景区", anchor.Name+" 观景点")),
			City:       firstNonEmptyString(anchor.Destination, searchCity),
			Kind:       "景点",
			Reason:     "核心自然锚点，优先验证为当天主体验",
			AnchorName: anchor.Name,
			MainScenic: true,
		})
		specs = append(specs, dayPOISearchSpec{
			Keyword:    strings.TrimSpace(anchor.Name + " 观景台"),
			City:       firstNonEmptyString(anchor.Destination, searchCity),
			Kind:       "景点",
			Reason:     "核心自然锚点的观景位置候选",
			AnchorName: anchor.Name,
			MainScenic: true,
		})
	}
	if naturalDay {
		specs = append(specs, dayPOISearchSpec{
			Keyword:    strings.TrimSpace(base + " 自然风景区"),
			City:       searchCity,
			Kind:       "景点",
			Reason:     "自然风光主体验地点",
			AnchorName: base,
			MainScenic: true,
		})
	} else {
		specs = append(specs, dayPOISearchSpec{
			Keyword: strings.TrimSpace(base + " " + firstNonEmptyString(theme, "景点")),
			City:    searchCity,
			Kind:    "景点",
			Reason:  "当天核心体验地点",
		})
	}
	specs = append(specs,
		dayPOISearchSpec{
			Keyword: strings.TrimSpace(base + " 特色餐厅"),
			City:    searchCity,
			Kind:    "餐饮",
			Reason:  "当天餐饮补充地点，不计入自然主体验覆盖",
		},
		dayPOISearchSpec{
			Keyword: strings.TrimSpace(base + " 酒店"),
			City:    searchCity,
			Kind:    "住宿",
			Reason:  "当天住宿或休整地点，不计入自然主体验覆盖",
		},
	)

	out := make([]dayPOISearchSpec, 0, len(specs))
	seen := map[string]bool{}
	for _, spec := range specs {
		spec.Keyword = strings.Join(strings.Fields(spec.Keyword), " ")
		if spec.Keyword == "" || seen[spec.Keyword] {
			continue
		}
		seen[spec.Keyword] = true
		out = append(out, spec)
	}
	return out
}

func poiInputsFromAmapSearch(resp amaptools.AmapResponse, spec dayPOISearchSpec) []graph.POIInput {
	rawPOIs, _ := resp.Raw["pois"].([]any)
	out := make([]graph.POIInput, 0, len(rawPOIs))
	for _, item := range rawPOIs {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := amapTextField(raw["name"])
		location := amapTextField(raw["location"])
		if name == "" || location == "" {
			continue
		}
		lng, lat, err := parseAmapLngLat(location)
		if err != nil || !isValidLngLat(lng, lat) {
			continue
		}
		poi := graph.POIInput{
			ID:            "poi-" + uuid.NewString(),
			Name:          name,
			AmapPOIID:     amapTextField(raw["id"]),
			Type:          firstNonEmptyString(spec.Kind, amapTextField(raw["type"])),
			Lat:           lat,
			Lng:           lng,
			Address:       amapTextField(raw["address"]),
			District:      amapTextField(raw["adname"]),
			City:          firstNonEmptyString(amapTextField(raw["cityname"]), spec.City),
			Description:   poiDescriptionFromAmapRaw(raw, spec),
			Duration:      defaultPOIDuration(spec.Kind),
			IsMainStop:    spec.MainScenic || !isLogisticsPOIType(spec.Kind),
			Notes:         spec.Reason,
			VerifiedBy:    "map_search",
			EstimatedCost: defaultPOICost(spec.Kind),
		}
		if spec.MainScenic && !isRelevantScenicCandidate(poi, spec) {
			continue
		}
		out = append(out, poi)
	}
	return out
}

func sortPOICandidatesForSpec(candidates []graph.POIInput, spec dayPOISearchSpec) {
	sort.SliceStable(candidates, func(i, j int) bool {
		return scorePOICandidate(candidates[i], spec) > scorePOICandidate(candidates[j], spec)
	})
}

func scorePOICandidate(poi graph.POIInput, spec dayPOISearchSpec) int {
	text := strings.Join([]string{poi.Name, poi.Address, poi.District, poi.City, poi.Description}, " ")
	score := 0
	if spec.AnchorName != "" && strings.Contains(text, spec.AnchorName) {
		score += 100
	}
	if containsAny(text, []string{"景区", "风景", "公园", "峡谷", "雪山", "湖", "海", "林海", "观景", "山", "草原", "湿地"}) {
		score += 30
	}
	if isGenericAdministrativePOI(poi) {
		score -= 80
	}
	if isLogisticsPOIType(poi.Type) {
		score -= 50
	}
	return score
}

func isRelevantScenicCandidate(poi graph.POIInput, spec dayPOISearchSpec) bool {
	if spec.AnchorName == "" {
		return !isGenericAdministrativePOI(poi)
	}
	text := strings.Join([]string{poi.Name, poi.Address, poi.District, poi.City, poi.Description}, " ")
	return strings.Contains(text, spec.AnchorName) || scorePOICandidate(poi, spec) >= 30
}

func isNaturalSceneryDay(day graph.DayNode, dayCtx dayExpansionContext) bool {
	return containsAny(strings.Join([]string{
		day.Theme,
		day.PrimaryArea,
		day.RouteOverview,
		day.ThinkingNotes,
		dayCtx.PhaseTheme,
		dayCtx.PhaseRegion,
	}, " "), []string{"自然", "风光", "风景", "雪山", "峡谷", "湖", "林海", "森林", "草原", "湿地", "徒步", "观景"})
}

func hasNaturalMainStop(pois []graph.POIInput) bool {
	for _, poi := range pois {
		if poi.IsMainStop && !isLogisticsPOIType(poi.Type) && !isGenericAdministrativePOI(poi) {
			return true
		}
	}
	return false
}

func isLogisticsPOIType(kind string) bool {
	return containsAny(kind, []string{"住宿", "酒店", "餐饮", "餐厅", "美食", "交通", "停车", "服务区"})
}

func isGenericAdministrativePOI(poi graph.POIInput) bool {
	text := strings.Join([]string{poi.Name, poi.Type, poi.Address}, " ")
	return containsAny(text, []string{"市政府", "政府", "政务", "派出所", "公安", "银行", "营业厅", "购物中心", "酒店", "宾馆", "餐厅", "饭店", "广场"}) &&
		!containsAny(text, []string{"景区", "风景", "公园", "峡谷", "雪山", "湖", "海", "林海", "观景", "草原", "湿地"})
}

func poiDescriptionFromAmapRaw(raw map[string]any, spec dayPOISearchSpec) string {
	parts := []string{}
	poiType := amapTextField(raw["type"])
	if poiType != "" {
		parts = append(parts, "类型："+poiType)
	}
	area := firstNonEmptyString(
		amapTextField(raw["business_area"]),
		strings.TrimSpace(strings.Join([]string{
			amapTextField(raw["cityname"]),
			amapTextField(raw["adname"]),
		}, " ")),
	)
	if area != "" {
		parts = append(parts, "区域："+area)
	}
	if rating := amapNestedTextField(raw["biz_ext"], "rating"); rating != "" {
		parts = append(parts, "评分："+rating)
	}
	if cost := amapNestedTextField(raw["biz_ext"], "cost"); cost != "" {
		parts = append(parts, "参考消费："+cost+" 元")
	}
	if spec.Reason != "" {
		parts = append(parts, "入选原因："+spec.Reason)
	}
	return strings.Join(parts, "；")
}

func amapNestedTextField(value any, key string) string {
	if record, ok := value.(map[string]any); ok {
		return amapTextField(record[key])
	}
	return ""
}
