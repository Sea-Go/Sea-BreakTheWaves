package trace

import (
	"context"
	"testing"

	"agent_v3/internal/graph"
	zhihutools "agent_v3/internal/tools/zhihu"
)

func TestTraceEmitterSeqIncreases(t *testing.T) {
	emitter := NewTraceEmitter("run-test")
	emitter.EmitStage(context.Background(), "macro_planning", "running", "建立大规划", "开始规划")
	emitter.EmitCompleted(context.Background(), "完成")

	first := <-emitter.Events()
	second := <-emitter.Events()
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("unexpected sequence numbers: %d %d", first.Seq, second.Seq)
	}
}

func TestPublicPlanningEventSanitizesInternalNames(t *testing.T) {
	ev := SanitizePublicPlanningEvent(PublicPlanningEvent{
		Type:  EventPlanningStageChanged,
		Stage: "internal_stage",
	})
	if ev.Stage == "" {
		t.Fatal("expected sanitized stage to stay non-empty")
	}
}

func TestTraceEmitterEmitsModelUsageAnnotation(t *testing.T) {
	emitter := NewTraceEmitter("run-usage")
	emitter.EmitModelUsage(context.Background(), PublicModelUsage{
		AgentLabel:       "宏观规划",
		Model:            "test-model",
		ModelLevel:       "MEDIUM",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}, "macro_planning")

	ev := <-emitter.Events()
	if ev.Type != EventMapAnnotationAdded {
		t.Fatalf("event type = %s, want %s", ev.Type, EventMapAnnotationAdded)
	}
	if ev.Stage != "macro_planning" || ev.Level != "overview" {
		t.Fatalf("unexpected stage/level: stage=%s level=%s", ev.Stage, ev.Level)
	}
	if ev.Usage == nil || ev.Usage.TotalTokens != 150 {
		t.Fatalf("usage not attached: %#v", ev.Usage)
	}
}

func TestEmitPhaseOverviewMapEvents(t *testing.T) {
	emitter := NewTraceEmitter("run-phase")
	emitPhaseOverviewMapEvents(context.Background(), emitter, &graph.TripOverview{
		Phases: []map[string]any{
			{"seq": 1.0, "name": "华北段", "region": "北京", "startDate": "2026-01-01", "endDate": "2026-01-03", "dayCount": 3.0},
		},
	})

	ev := <-emitter.Events()
	if ev.Type != EventMapScopeChanged {
		t.Fatalf("event type = %s, want %s", ev.Type, EventMapScopeChanged)
	}
	if ev.Viewport == nil {
		t.Fatal("expected viewport")
	}
}

func TestEmitRouteSegmentMapEvents(t *testing.T) {
	emitter := NewTraceEmitter("run-route")
	emitRouteSegmentMapEvents(
		context.Background(),
		emitter,
		"day",
		"day-1",
		[]graph.POIInput{
			{ID: "poi-1", Name: "A", Lng: 10, Lat: 20},
			{ID: "poi-2", Name: "B", Lng: 11, Lat: 21},
		},
		[]graph.RouteInput{
			{
				FromPOIID:      "poi-1",
				ToPOIID:        "poi-2",
				TransportMode:  "walking",
				DistanceMeters: 1000,
				DurationMin:    15,
				Polyline:       "10,20;11,21",
			},
		},
		RouteDisplayContext{PhaseID: "phase-1", PhaseSeq: 1, PhaseName: "北京段", DayID: "day-1", DayIndex: 1},
	)

	ev := <-emitter.Events()
	if ev.Type != EventMapBatch {
		t.Fatalf("event type = %s, want %s", ev.Type, EventMapBatch)
	}
	if len(ev.Events) == 0 {
		t.Fatal("expected batched child events")
	}
}

func TestZhihuCandidateAnnotationUsesScopeAnchor(t *testing.T) {
	annotation := annotationFromZhihuCandidate("trip-1", guideEvidenceTopic{
		Topic:       "杭州 西湖 攻略",
		Level:       "phase",
		NodeID:      "phase-hangzhou",
		AnchorLabel: "杭州",
		Region:      "杭州",
	}, zhihutools.ZhihuGuideCandidate{
		Title:        "杭州西湖两日游避坑经验",
		URL:          "https://www.zhihu.com/question/1/answer/2",
		AuthorName:   "知乎作者",
		Summary:      "早晚更适合游览西湖，湖滨夜景可以和同一天动线合并。",
		VoteUpCount:  120,
		CommentCount: 8,
		SourceQuery:  "杭州西湖 攻略",
		SearchScope:  "zhihu_search",
		SourceIntent: "pitfall",
		Status:       zhihutools.ZhihuGuideStatusAccepted,
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
	if annotation.Anchor.Type != "scope" {
		t.Fatalf("anchor type = %s, want scope", annotation.Anchor.Type)
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
}
