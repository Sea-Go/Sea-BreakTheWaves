package agent

import (
	"context"
	"strings"
	"testing"

	"agent_v2/graph"
	"agent_v2/tools"
)

func TestTraceEmitterSeqIncreases(t *testing.T) {
	emitter := NewTraceEmitter("run-test")
	defer emitter.Close()

	emitter.EmitStage(context.Background(), "macro_planning", "running", "建立大规划", "开始规划")
	emitter.EmitCompleted(context.Background(), "完成")

	first := <-emitter.Events()
	second := <-emitter.Events()
	third := <-emitter.Events()

	if first.Seq != 1 {
		t.Fatalf("first seq = %d, want 1", first.Seq)
	}
	if second.Seq != 2 {
		t.Fatalf("second seq = %d, want 2", second.Seq)
	}
	if second.Type != EventMapAnnotationAdded {
		t.Fatalf("second type = %s, want %s", second.Type, EventMapAnnotationAdded)
	}
	if third.Seq != 3 {
		t.Fatalf("third seq = %d, want 3", third.Seq)
	}
}

func TestPublicPlanningEventSanitizesInternalNames(t *testing.T) {
	ev := SanitizePublicPlanningEvent(PublicPlanningEvent{
		Type:           EventPlanningStageChanged,
		PublicAction:   "调用 create_trip_plan",
		ThoughtSummary: "使用 amap_route_driving 和 split_parent_node",
		RecordedFacts:  []string{"get_weather_context 已返回"},
		Usage: &PublicModelUsage{
			AgentLabel: "amap_agent",
			Model:      "qwen3-max",
			ModelLevel: "MEDIUM",
		},
		Popup: &PublicMapPopup{
			Title:   "amap_poi_keyword_search",
			Content: "create_trip_plan",
		},
		Route: &PublicRouteCandidate{
			Label:          "amap_route_driving",
			Accuracy:       "amap_route_driving",
			Source:         "amap_route_driving",
			PhaseID:        "split_parent_node",
			PhaseName:      "create_trip_plan",
			DayID:          "write_guide_insight",
			FromNodeID:     "amap_poi_keyword_search",
			ToNodeID:       "zhihu_guide_material",
			ConnectionType: "create_trip_plan",
			Reason:         "split_parent_node",
		},
		Annotation: &PublicMapAnnotation{
			ID:         "ann-1",
			Kind:       "zhihu_guide_material",
			Source:     "zhihu_guide_material",
			Title:      "write_guide_insight",
			Summary:    "amap_poi_keyword_search",
			AuthorName: "Tool 作者",
			Status:     "selected",
			Tags:       []string{"amap_route_driving"},
			Reasons:    []string{"write_guide_insight"},
			Evidence:   []string{"split_parent_node"},
			Anchor: PublicMapAnnotationAnchor{
				Type:  "scope",
				Label: "amap_poi_keyword_search",
			},
		},
	})

	serialized := strings.Join([]string{
		ev.PublicAction,
		ev.ThoughtSummary,
		strings.Join(ev.RecordedFacts, " "),
		ev.Popup.Title,
		ev.Popup.Content,
		ev.Route.Label,
		ev.Route.Accuracy,
		ev.Route.Source,
		ev.Route.PhaseID,
		ev.Route.PhaseName,
		ev.Route.DayID,
		ev.Route.FromNodeID,
		ev.Route.ToNodeID,
		ev.Route.ConnectionType,
		ev.Route.Reason,
		ev.Annotation.Kind,
		ev.Annotation.Source,
		ev.Annotation.Title,
		ev.Annotation.Summary,
		ev.Annotation.AuthorName,
		strings.Join(ev.Annotation.Tags, " "),
		strings.Join(ev.Annotation.Reasons, " "),
		strings.Join(ev.Annotation.Evidence, " "),
		ev.Annotation.Anchor.Label,
		ev.Usage.AgentLabel,
		ev.Usage.Model,
		ev.Usage.ModelLevel,
	}, " ")

	for _, forbidden := range []string{
		"amap_",
		"create_trip_plan",
		"split_parent_node",
		"get_weather_context",
		"zhihu_guide_material",
		"write_guide_insight",
	} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("sanitized event still contains %q: %s", forbidden, serialized)
		}
	}
}

func TestTraceEmitterEmitsModelUsageEvent(t *testing.T) {
	emitter := NewTraceEmitter("run-usage")
	defer emitter.Close()

	emitter.EmitModelUsage(context.Background(), PublicModelUsage{
		AgentLabel:       "宏观规划",
		Model:            "qwen3-max",
		ModelLevel:       "MEDIUM",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}, string(StageMacroPlanning))

	ev := <-emitter.Events()
	if ev.Type != EventMapAnnotationAdded {
		t.Fatalf("event type = %s, want %s", ev.Type, EventMapAnnotationAdded)
	}
	if ev.Stage != string(StageMacroPlanning) || ev.Level != "overview" {
		t.Fatalf("unexpected stage/level: stage=%s level=%s", ev.Stage, ev.Level)
	}
	if ev.Usage == nil || ev.Usage.TotalTokens != 150 || ev.Usage.Model != "qwen3-max" {
		t.Fatalf("usage not preserved: %#v", ev.Usage)
	}
	if ev.Annotation == nil || ev.Annotation.Kind != "model_usage" {
		t.Fatalf("model usage annotation missing: %#v", ev.Annotation)
	}
}

func TestEmitPhaseOverviewMapEventsDoesNotEmitApproximatePoints(t *testing.T) {
	emitter := NewTraceEmitter("run-phase")
	defer emitter.Close()

	emitPhaseOverviewMapEvents(context.Background(), emitter, &graph.TripOverview{
		Phases: []map[string]any{
			{
				"name":      "阶段 1",
				"region":    "华北",
				"seq":       float64(1),
				"startDate": "2026-01-01",
				"endDate":   "2026-01-10",
				"dayCount":  float64(10),
			},
		},
	})

	ev := <-emitter.Events()
	if ev.Type != EventMapScopeChanged {
		t.Fatalf("event type = %s, want %s", ev.Type, EventMapScopeChanged)
	}
	if len(ev.Events) != 0 {
		t.Fatalf("overview emitted child map events: %#v", ev.Events)
	}
	if ev.Point != nil || ev.Route != nil {
		t.Fatalf("overview emitted concrete geometry: point=%#v route=%#v", ev.Point, ev.Route)
	}
}

func TestEmitExactPOIMapBatchEmitsOnlyExactValidPoints(t *testing.T) {
	emitter := NewTraceEmitter("run-poi")
	defer emitter.Close()

	emitExactPOIMapBatch(
		context.Background(),
		emitter,
		"day",
		"day-1",
		[]graph.POIInput{
			{
				ID:      "poi-palace",
				Name:    "故宫博物院",
				Lng:     116.397026,
				Lat:     39.918058,
				Address: "北京市东城区景山前街4号",
			},
			{
				ID:   "poi-region",
				Name: "华北阶段",
				Lng:  0,
				Lat:  0,
			},
			{
				ID:   "poi-invalid",
				Name: "越界地点",
				Lng:  200,
				Lat:  95,
			},
		},
		routeDisplayContext{PhaseID: "phase-1", PhaseSeq: 2, PhaseName: "华北段", DayID: "day-1", DayIndex: 3},
	)

	ev := <-emitter.Events()
	if ev.Type != EventMapBatch {
		t.Fatalf("event type = %s, want %s", ev.Type, EventMapBatch)
	}
	if len(ev.Events) != 1 {
		t.Fatalf("batch event count = %d, want 1: %#v", len(ev.Events), ev.Events)
	}
	child := ev.Events[0]
	if child.Type != EventMapPointAdded {
		t.Fatalf("child event type = %s, want %s", child.Type, EventMapPointAdded)
	}
	if child.Point == nil {
		t.Fatal("child point is nil")
	}
	if child.Point.Accuracy != "exact" {
		t.Fatalf("point accuracy = %s, want exact", child.Point.Accuracy)
	}
	if child.Point.Label != "故宫博物院" {
		t.Fatalf("point label = %s, want 故宫博物院", child.Point.Label)
	}
	if child.Point.PhaseID != "phase-1" || child.Point.PhaseSeq != 2 || child.Point.DayIndex != 3 {
		t.Fatalf("point metadata not preserved: %#v", child.Point)
	}
}

func TestEmitRouteSegmentMapEventsEmitsRouteAndDecision(t *testing.T) {
	emitter := NewTraceEmitter("run-route")
	defer emitter.Close()

	emitRouteSegmentMapEvents(
		context.Background(),
		emitter,
		"day",
		"day-1",
		[]graph.POIInput{
			{ID: "poi-1", Name: "故宫博物院", Lng: 116.397026, Lat: 39.918058},
			{ID: "poi-2", Name: "景山公园", Lng: 116.39687, Lat: 39.925052},
		},
		[]graph.RouteInput{
			{
				FromPOIID:      "poi-1",
				ToPOIID:        "poi-2",
				TransportMode:  "driving",
				DistanceMeters: 1200,
				DurationMin:    8,
				Notes:          "距离短，适合当天串联。",
			},
		},
		routeDisplayContext{PhaseID: "phase-1", PhaseSeq: 1, PhaseName: "北京段", DayID: "day-1", DayIndex: 1},
	)

	ev := <-emitter.Events()
	if ev.Type != EventMapBatch {
		t.Fatalf("event type = %s, want %s", ev.Type, EventMapBatch)
	}
	var selectedRoute bool
	var decisionAnnotation bool
	for _, child := range ev.Events {
		if child.Type == EventRouteSelected && child.Route != nil && len(child.Route.Polyline) >= 2 {
			if child.Route.Accuracy != "connector" {
				t.Fatalf("route accuracy = %s, want connector", child.Route.Accuracy)
			}
			if child.Route.PhaseID != "phase-1" || child.Route.DayID != "day-1" || child.Route.DayIndex != 1 {
				t.Fatalf("route metadata not preserved: %#v", child.Route)
			}
			selectedRoute = true
		}
		if child.Type == EventMapAnnotationAdded && child.Annotation != nil && child.Annotation.Kind == "decision" {
			decisionAnnotation = true
		}
	}
	if !selectedRoute {
		t.Fatalf("route_selected child missing or has no polyline: %#v", ev.Events)
	}
	if !decisionAnnotation {
		t.Fatalf("decision annotation child missing: %#v", ev.Events)
	}
}

func TestZhihuCandidateAnnotationUsesScopeAnchor(t *testing.T) {
	annotation := annotationFromZhihuCandidate("trip-1", guideEvidenceTopic{
		Topic:       "杭州 西湖 攻略",
		Level:       "phase",
		NodeID:      "phase-hangzhou",
		AnchorLabel: "杭州",
		Region:      "杭州",
	}, tools.ZhihuGuideCandidate{
		Title:        "杭州西湖两日游避坑经验",
		URL:          "https://www.zhihu.com/question/1/answer/2",
		AuthorName:   "知乎作者",
		Summary:      "早晚更适合游览西湖，湖滨夜景可以和同一天动线合并。",
		VoteUpCount:  120,
		CommentCount: 8,
		SourceQuery:  "杭州西湖 攻略",
		SearchScope:  "zhihu_search",
		SourceIntent: "pitfall",
		Status:       tools.ZhihuGuideStatusAccepted,
		Score:        82.5,
		Reasons:      []string{"主题相关", "互动质量高"},
	}, map[string]bool{
		"https://www.zhihu.com/question/1/answer/2": true,
	})

	if annotation == nil {
		t.Fatal("annotation is nil")
	}
	if annotation.Kind != "zhihu_source" {
		t.Fatalf("kind = %s, want zhihu_source", annotation.Kind)
	}
	if annotation.Status != "selected" {
		t.Fatalf("status = %s, want selected", annotation.Status)
	}
	if annotation.Anchor.Type != "scope" {
		t.Fatalf("anchor type = %s, want scope", annotation.Anchor.Type)
	}
	if annotation.Anchor.Point != nil {
		t.Fatalf("annotation should not create fake point: %#v", annotation.Anchor.Point)
	}
	if len(annotation.Evidence) == 0 {
		t.Fatal("annotation evidence is empty")
	}
}

func TestParseAmapLngLatAndValidation(t *testing.T) {
	lng, lat, err := parseAmapLngLat("116.397026,39.918058")
	if err != nil {
		t.Fatalf("parseAmapLngLat returned error: %v", err)
	}
	if !isValidLngLat(lng, lat) {
		t.Fatalf("coordinate should be valid: %.6f,%.6f", lng, lat)
	}
	if isValidLngLat(0, 0) {
		t.Fatal("zero coordinate should be treated as missing")
	}
	if isValidLngLat(200, 95) {
		t.Fatal("out-of-range coordinate should be invalid")
	}
}

func TestPOIInputsFromAmapSearchRequireExactLocation(t *testing.T) {
	pois := poiInputsFromAmapSearch(tools.AmapResponse{
		OK: true,
		Raw: map[string]any{
			"pois": []any{
				map[string]any{
					"id":       "B001",
					"name":     "翠湖公园",
					"location": "102.704412,25.050972",
					"address":  "翠湖南路",
					"cityname": "昆明市",
					"adname":   "五华区",
				},
				map[string]any{
					"id":      "B002",
					"name":    "缺坐标地点",
					"address": "只应进入证据，不应成为地图点",
				},
			},
		},
	}, dayPOISearchSpec{Kind: "景点", Reason: "当天核心体验地点"})

	if len(pois) != 1 {
		t.Fatalf("poi count = %d, want 1: %#v", len(pois), pois)
	}
	if pois[0].Name != "翠湖公园" || pois[0].Lng != 102.704412 || pois[0].Lat != 25.050972 {
		t.Fatalf("unexpected poi: %#v", pois[0])
	}
	if pois[0].VerifiedBy != "map_search" {
		t.Fatalf("verifiedBy = %s, want map_search", pois[0].VerifiedBy)
	}
}

func TestRouteInputFromDrivingResponseParsesPolyline(t *testing.T) {
	route := routeInputFromDrivingResponse(tools.AmapResponse{
		OK: true,
		Raw: map[string]any{
			"route": map[string]any{
				"paths": []any{
					map[string]any{
						"distance": "2500",
						"duration": "600",
						"steps": []any{
							map[string]any{"polyline": "102.704412,25.050972;102.710000,25.052000"},
							map[string]any{"polyline": "102.710000,25.052000;102.720000,25.060000"},
						},
					},
				},
			},
		},
	}, graph.POIInput{ID: "poi-1", Name: "翠湖公园", Lng: 102.704412, Lat: 25.050972}, graph.POIInput{ID: "poi-2", Name: "云南大学", Lng: 102.72, Lat: 25.06})

	if route == nil {
		t.Fatal("route is nil")
	}
	if route.DistanceMeters != 2500 || route.DurationMin != 10 {
		t.Fatalf("unexpected distance/duration: %#v", route)
	}
	points := publicPolylineFromRoute(*route, graph.POIInput{}, graph.POIInput{})
	if len(points) != 3 {
		t.Fatalf("polyline points = %d, want 3: %#v", len(points), points)
	}
}
