package stages

import (
	"agent_v3/internal/config"
	"agent_v3/internal/graph"
	amaptools "agent_v3/internal/tools/amap"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

func buildDayRoutesDirect(ctx context.Context, pois []graph.POIInput, trace *TraceEmitter, dayID string, dayCtx dayExpansionContext) []graph.RouteInput {
	pois = sortedPOIsByVisitOrder(pois)
	if len(pois) < 2 {
		return nil
	}
	routes := make([]graph.RouteInput, 0, len(pois)-1)
	for i := 0; i < len(pois)-1; i++ {
		from := pois[i]
		to := pois[i+1]
		if !isValidLngLat(from.Lng, from.Lat) || !isValidLngLat(to.Lng, to.Lat) {
			continue
		}
		route := buildRouteBetweenPOIsDirect(ctx, from, to, trace, dayID)
		enrichRouteDisplayMetadata(&route, from, to, routeDisplayContext{
			PhaseID:        dayCtx.PhaseID,
			PhaseSeq:       dayCtx.PhaseSeq,
			PhaseName:      dayCtx.PhaseName,
			DayID:          dayID,
			DayIndex:       dayCtx.DayIndex,
			SegmentIndex:   i + 1,
			ConnectionType: "day_segment",
		})
		routes = append(routes, route)
	}
	return routes
}

func buildRouteBetweenPOIsDirect(ctx context.Context, from, to graph.POIInput, trace *TraceEmitter, noticeNodeID string) graph.RouteInput {
	route := graph.RouteInput{
		FromPOIID:      from.ID,
		ToPOIID:        to.ID,
		TransportMode:  "driving",
		Accuracy:       "connector",
		Source:         "exact_point_connector",
		FromNodeID:     from.ID,
		ToNodeID:       to.ID,
		Polyline:       polylineJSON([][2]float64{{from.Lng, from.Lat}, {to.Lng, to.Lat}}),
		Notes:          "路线服务暂未返回完整轨迹，先用真实地点坐标连接，等待复核。",
		ConnectionType: "day_segment",
	}

	resp, err := amaptools.DrivingRoute(ctx, config.Cfg.Amap, amaptools.AmapDrivingRouteInput{
		Origin:        fmt.Sprintf("%.6f,%.6f", from.Lng, from.Lat),
		Destination:   fmt.Sprintf("%.6f,%.6f", to.Lng, to.Lat),
		OriginID:      from.AmapPOIID,
		DestinationID: to.AmapPOIID,
		Extensions:    "all",
	})
	if err != nil || !resp.OK {
		log.Warnf("[workflow-runner] direct route failed node=%s from=%s to=%s ok=%v err=%v info=%s", noticeNodeID, from.Name, to.Name, resp.OK, err, resp.Info)
		emitDayExpansionNotice(ctx, trace, noticeNodeID, "路线轨迹待复核", fmt.Sprintf("%s 到 %s 暂未取得完整路线轨迹，地图先用真实地点连线。", from.Name, to.Name), "review")
		return route
	}
	if parsed := routeInputFromDrivingResponse(resp, from, to); parsed != nil {
		parsed.Accuracy = "exact"
		parsed.Source = "amap_driving"
		parsed.FromNodeID = from.ID
		parsed.ToNodeID = to.ID
		parsed.ConnectionType = "day_segment"
		return *parsed
	}
	emitDayExpansionNotice(ctx, trace, noticeNodeID, "路线轨迹待复核", fmt.Sprintf("%s 到 %s 暂未取得完整路线轨迹，地图先用真实地点连线。", from.Name, to.Name), "review")
	return route
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

func routeInputFromDrivingResponse(resp amaptools.AmapResponse, from, to graph.POIInput) *graph.RouteInput {
	routeRaw, _ := resp.Raw["route"].(map[string]any)
	paths, _ := routeRaw["paths"].([]any)
	if len(paths) == 0 {
		return nil
	}
	path, _ := paths[0].(map[string]any)
	if path == nil {
		return nil
	}
	distanceMeters := numberFromAmapField(path["distance"])
	durationSeconds := numberFromAmapField(path["duration"])
	polyline := polylineJSONFromDrivingPath(path)
	if polyline == "" {
		polyline = polylineJSON([][2]float64{{from.Lng, from.Lat}, {to.Lng, to.Lat}})
	}
	return &graph.RouteInput{
		FromPOIID:      from.ID,
		ToPOIID:        to.ID,
		TransportMode:  "driving",
		DistanceMeters: distanceMeters,
		DurationMin:    math.Ceil(durationSeconds / 60),
		Polyline:       polyline,
		Notes:          "已取得真实路线数据，用于当天动线权衡。",
	}
}

func polylineJSONFromDrivingPath(path map[string]any) string {
	steps, _ := path["steps"].([]any)
	points := make([][2]float64, 0)
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for _, segment := range strings.Split(amapTextField(step["polyline"]), ";") {
			lng, lat, err := parseAmapLngLat(segment)
			if err != nil || !isValidLngLat(lng, lat) {
				continue
			}
			if len(points) > 0 {
				last := points[len(points)-1]
				if last[0] == lng && last[1] == lat {
					continue
				}
			}
			points = append(points, [2]float64{lng, lat})
		}
	}
	if len(points) < 2 {
		return ""
	}
	return polylineJSON(points)
}

func polylineJSON(points [][2]float64) string {
	b, err := json.Marshal(points)
	if err != nil {
		return ""
	}
	return string(b)
}
