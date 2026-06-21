package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"agent_v2/config"
	"agent_v2/graph"
	"agent_v2/tools"

	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

type dayExpansionContext struct {
	DayID         string
	PhaseID       string
	PhaseSeq      int
	PhaseRegion   string
	PhaseName     string
	PhaseTheme    string
	DayIndex      int
	GeoConstraint TravelGeoConstraint
	Requirement   TravelRequirementSnapshot
}

type dayPOISearchSpec struct {
	Keyword    string
	City       string
	Kind       string
	Reason     string
	AnchorName string
	MainScenic bool
}

func dayExpansionContextsFromOverview(overview *graph.TripOverview) map[string]dayExpansionContext {
	out := map[string]dayExpansionContext{}
	if overview == nil {
		return out
	}
	for _, day := range overview.Days {
		dayID := getStr(day, "id")
		if dayID == "" {
			continue
		}
		out[dayID] = dayExpansionContext{
			DayID:       dayID,
			PhaseID:     getStr(day, "phaseID"),
			PhaseSeq:    int(getFloat(day, "phaseSeq")),
			PhaseRegion: getStr(day, "phaseRegion"),
			PhaseName:   getStr(day, "phaseName"),
			PhaseTheme:  getStr(day, "phaseTheme"),
			DayIndex:    int(getFloat(day, "dayIndex")),
		}
	}
	return out
}

func discoverDayPOIsDirect(ctx context.Context, day graph.DayNode, dayCtx dayExpansionContext, trace *TraceEmitter) ([]graph.POIInput, error) {
	specs := buildDayPOISearchSpecs(day, dayCtx)
	if len(specs) == 0 {
		return nil, fmt.Errorf("当天缺少可搜索的区域或主题，暂时无法生成精确地点")
	}

	emitDayExpansionNotice(
		ctx,
		trace,
		day.ID,
		"启动地点搜索兜底",
		fmt.Sprintf("正在根据第 %d 天的区域和主题搜索真实地点，只展示带精确坐标的结果。", day.DayIndex),
		"active",
	)

	pois := make([]graph.POIInput, 0, len(specs))
	seen := map[string]bool{}
	for _, spec := range specs {
		resp, err := tools.POIKeywordSearch(ctx, config.Cfg.Amap, tools.AmapPOIKeywordSearchInput{
			Keywords:   spec.Keyword,
			City:       spec.City,
			CityLimit:  false,
			Offset:     10,
			Page:       1,
			Extensions: "all",
		})
		if err != nil || !resp.OK {
			log.Warnf("[workflow-runner] direct poi search failed day=%s keyword=%q ok=%v err=%v info=%s", day.ID, spec.Keyword, resp.OK, err, resp.Info)
			emitDayExpansionNotice(ctx, trace, day.ID, "地点搜索遇到问题", fmt.Sprintf("「%s」暂时没有返回可展示结果，继续尝试其他主题。", spec.Keyword), "review")
			continue
		}

		candidates := poiInputsFromAmapSearch(resp, spec)
		sortPOICandidatesForSpec(candidates, spec)
		for _, poi := range candidates {
			key := strings.ToLower(strings.TrimSpace(poi.Name)) + fmt.Sprintf("@%.6f,%.6f", poi.Lng, poi.Lat)
			if seen[key] {
				continue
			}
			seen[key] = true
			poi.VisitOrder = len(pois) + 1
			pois = append(pois, poi)
			emitPOISearchCandidateAnnotation(ctx, trace, day.ID, poi, spec)
			break
		}
	}

	if len(pois) == 0 {
		return nil, fmt.Errorf("地图搜索没有返回带精确坐标的地点")
	}
	if isNaturalSceneryDay(day, dayCtx) && !hasNaturalMainStop(pois) {
		return nil, fmt.Errorf("自然风光日没有搜索到可作为主体验的自然景点，不能用餐饮、住宿或城市泛化地点替代")
	}
	return pois, nil
}

func exactifyParsedPOIs(ctx context.Context, pois []graph.POIInput, day graph.DayNode, dayCtx dayExpansionContext, trace *TraceEmitter) []graph.POIInput {
	out := make([]graph.POIInput, 0, len(pois))
	city := firstNonEmptyString(day.PrimaryArea, day.StartPoint, dayCtx.PhaseRegion, dayCtx.PhaseName)
	for _, poi := range pois {
		if strings.TrimSpace(poi.Name) == "" {
			continue
		}
		query := firstNonEmptyString(poi.Address, strings.TrimSpace(city+" "+poi.Name), poi.Name)
		resp, err := tools.GeocodeAddress(ctx, config.Cfg.Amap, tools.AmapGeocodeInput{
			Address: query,
			City:    firstNonEmptyString(poi.City, city),
		})
		if err != nil || !resp.OK {
			log.Warnf("[workflow-runner] geocode parsed poi failed day=%s poi=%q ok=%v err=%v info=%s", day.ID, poi.Name, resp.OK, err, resp.Info)
			emitDayExpansionNotice(ctx, trace, day.ID, "地点坐标待确认", fmt.Sprintf("%s 暂未取得可确认坐标，已从地图点位层跳过。", poi.Name), "review")
			continue
		}
		geocodes, _ := resp.Raw["geocodes"].([]any)
		if len(geocodes) == 0 {
			emitDayExpansionNotice(ctx, trace, day.ID, "地点坐标待确认", fmt.Sprintf("%s 暂未取得可确认坐标，已从地图点位层跳过。", poi.Name), "review")
			continue
		}
		geo, _ := geocodes[0].(map[string]any)
		lng, lat, err := parseAmapLngLat(amapTextField(geo["location"]))
		if err != nil || !isValidLngLat(lng, lat) {
			emitDayExpansionNotice(ctx, trace, day.ID, "地点坐标待确认", fmt.Sprintf("%s 的坐标格式不可用，已从地图点位层跳过。", poi.Name), "review")
			continue
		}
		poi.ID = firstNonEmptyString(poi.ID, "poi-"+uuid.NewString())
		poi.Lng = lng
		poi.Lat = lat
		poi.Address = firstNonEmptyString(amapTextField(geo["formatted_address"]), poi.Address)
		poi.City = firstNonEmptyString(poi.City, city)
		poi.VerifiedBy = "geocode"
		if poi.Duration == 0 && poi.Type != "住宿" {
			poi.Duration = defaultPOIDuration(poi.Type)
		}
		if poi.EstimatedCost == 0 {
			poi.EstimatedCost = defaultPOICost(poi.Type)
		}
		poi.VisitOrder = len(out) + 1
		out = append(out, poi)
		emitPOISearchCandidateAnnotation(ctx, trace, day.ID, poi, dayPOISearchSpec{
			Keyword: query,
			City:    city,
			Kind:    firstNonEmptyString(poi.Type, "地点"),
			Reason:  "结构化结果已通过坐标复核",
		})
	}
	return out
}

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

func poiInputsFromAmapSearch(resp tools.AmapResponse, spec dayPOISearchSpec) []graph.POIInput {
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

func enrichRouteDisplayMetadata(route *graph.RouteInput, from, to graph.POIInput, meta routeDisplayContext) {
	if route == nil {
		return
	}
	route.PhaseID = meta.PhaseID
	route.PhaseSeq = meta.PhaseSeq
	route.PhaseName = meta.PhaseName
	route.DayID = meta.DayID
	route.DayIndex = meta.DayIndex
	route.SegmentIndex = meta.SegmentIndex
	route.ConnectionType = defaultIfEmpty(meta.ConnectionType, route.ConnectionType)
	route.FromNodeID = defaultIfEmpty(route.FromNodeID, from.ID)
	route.ToNodeID = defaultIfEmpty(route.ToNodeID, to.ID)
	route.Accuracy = defaultIfEmpty(route.Accuracy, "connector")
	route.Source = defaultIfEmpty(route.Source, "exact_point_connector")
}

func sortedPOIsByVisitOrder(pois []graph.POIInput) []graph.POIInput {
	out := append([]graph.POIInput(nil), pois...)
	sort.SliceStable(out, func(i, j int) bool {
		left := out[i].VisitOrder
		right := out[j].VisitOrder
		if left == right {
			return out[i].Name < out[j].Name
		}
		if left <= 0 {
			return false
		}
		if right <= 0 {
			return true
		}
		return left < right
	})
	return out
}

func polylineJSON(points [][2]float64) string {
	b, err := json.Marshal(points)
	if err != nil {
		return ""
	}
	return string(b)
}

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

func amapTextField(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []string:
		return strings.TrimSpace(strings.Join(v, " "))
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func numberFromAmapField(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		out, _ := v.Float64()
		return out
	case string:
		out, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return out
	default:
		return 0
	}
}

func defaultPOIDuration(kind string) int {
	switch kind {
	case "餐饮":
		return 60
	case "住宿":
		return 0
	default:
		return 120
	}
}

func defaultPOICost(kind string) float64 {
	switch kind {
	case "餐饮":
		return 80
	case "住宿":
		return 400
	default:
		return 50
	}
}
