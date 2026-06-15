package stages

import (
	"agent_v3/internal/config"
	domaingeo "agent_v3/internal/domain/geo"
	"agent_v3/internal/graph"
	amaptools "agent_v3/internal/tools/amap"
	"context"
	"fmt"
	"github.com/google/uuid"
	"strings"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

type dayExpansionContext struct {
	PhaseID       string
	PhaseSeq      int
	PhaseRegion   string
	PhaseName     string
	PhaseTheme    string
	DayIndex      int
	GeoConstraint domaingeo.TravelGeoConstraint
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
		resp, err := amaptools.POIKeywordSearch(ctx, config.Cfg.Amap, amaptools.AmapPOIKeywordSearchInput{
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
		resp, err := amaptools.GeocodeAddress(ctx, config.Cfg.Amap, amaptools.AmapGeocodeInput{
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
