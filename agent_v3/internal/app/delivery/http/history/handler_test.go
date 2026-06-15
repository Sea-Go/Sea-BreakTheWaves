package history

import (
	"encoding/json"
	"testing"

	domainevents "agent_v3/internal/domain/events"
	"agent_v3/internal/graph"
	historyrec "agent_v3/internal/history"
)

func TestTravelRunDetailCoalescesAssistantDeltas(t *testing.T) {
	first := domainevents.PublicPlanningEvent{Type: domainevents.EventChatMessageDelta, RunID: "run-1", Seq: 1, Message: "第一段"}
	second := domainevents.PublicPlanningEvent{Type: domainevents.EventChatMessageDelta, RunID: "run-1", Seq: 2, Message: "第二段"}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)

	detail := buildTravelRunDetailResponse(&graph.ExplorationRunDetail{
		Run: graph.ExplorationRunNode{ID: "run-1", ThreadID: "thread-1", UserID: "user-1"},
		Steps: []graph.ExplorationStepNode{
			{ID: "user-step", ActionType: historyrec.ActionChatUser, MessageRole: "user", Message: "规划北京"},
			{ID: "assistant-1", EventType: domainevents.EventChatMessageDelta, Message: "第一段", PayloadJSON: string(firstJSON)},
			{ID: "assistant-2", EventType: domainevents.EventChatMessageDelta, Message: "第二段", PayloadJSON: string(secondJSON)},
		},
	})

	if len(detail.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(detail.Messages))
	}
	if got := detail.Messages[1].Content; got != "第一段第二段" {
		t.Fatalf("assistant content = %q", got)
	}
	if detail.FinalResult != "第一段第二段" {
		t.Fatalf("final result = %q", detail.FinalResult)
	}
}

func TestBuildPublicMapSnapshotFromRunEvents(t *testing.T) {
	events := []domainevents.PublicPlanningEvent{
		{
			Type:  domainevents.EventMapScopeChanged,
			Level: "overview",
			Viewport: &domainevents.PublicMapViewport{
				Center: [2]float64{116.4074, 39.9042},
				Zoom:   6,
			},
		},
		{
			Type:   domainevents.EventMapPointAdded,
			Level:  "overview",
			NodeID: "start-beijing",
			Point:  &domainevents.PublicMapPoint{Lng: 116.4074, Lat: 39.9042, Label: "北京", Kind: "start", Accuracy: "exact"},
			Popup:  &domainevents.PublicMapPopup{Title: "出发地", Content: "北京"},
		},
		{
			Type:      domainevents.EventMapPointSoftDeleted,
			Level:     "overview",
			NodeID:    "backup",
			Point:     &domainevents.PublicMapPoint{Lng: 117, Lat: 40, Label: "备选", Kind: "poi", Accuracy: "exact"},
			Reason:    "暂不采用",
			CreatedAt: "2026-06-05T00:00:00Z",
		},
		{
			Type:   domainevents.EventMapPointAdded,
			Level:  "overview",
			NodeID: "old-region",
			Point:  &domainevents.PublicMapPoint{Lng: 115.8, Lat: 39.7, Label: "华北", Kind: "phase"},
		},
		{
			Type:   domainevents.EventMapAnnotationAdded,
			Level:  "day",
			NodeID: "day-1",
			Annotation: &domainevents.PublicMapAnnotation{
				ID:      "ann-zhihu-1",
				Kind:    "zhihu_source",
				Source:  "zhihu",
				Title:   "西湖避坑经验",
				Summary: "早晚避开人流。",
				Status:  "selected",
				Anchor:  domainevents.PublicMapAnnotationAnchor{Type: "scope", Label: "西湖"},
			},
		},
		{
			Type:    domainevents.EventRouteSelected,
			Level:   "day",
			RouteID: "route-1",
			Route: &domainevents.PublicRouteCandidate{
				ID:       "route-1",
				Label:    "西湖 → 灵隐寺",
				Status:   "selected",
				Mode:     "driving",
				Polyline: [][2]float64{{120.1, 30.2}, {120.2, 30.3}},
			},
		},
		{
			Type:   domainevents.EventMapAnnotationAdded,
			Level:  "day",
			NodeID: "poi-1",
			Annotation: &domainevents.PublicMapAnnotation{
				ID:      "ann-review-1",
				Kind:    "review",
				Source:  "review",
				Title:   "地点审核",
				Summary: "审核通过。",
				Status:  "selected",
				Anchor:  domainevents.PublicMapAnnotationAnchor{Type: "point", NodeID: "poi-1", Label: "西湖"},
			},
		},
	}

	snapshot := buildPublicMapSnapshot(events)
	if snapshot.ActiveLevel != "overview" {
		t.Fatalf("active level = %q", snapshot.ActiveLevel)
	}
	if snapshot.Viewport == nil || snapshot.Viewport.Zoom != 6 {
		t.Fatalf("viewport not restored: %#v", snapshot.Viewport)
	}
	if snapshot.Points["start-beijing"].Point.Label != "北京" {
		t.Fatalf("start point missing: %#v", snapshot.Points)
	}
	if snapshot.Points["backup"].Status != "dimmed" {
		t.Fatalf("soft-deleted point status = %q", snapshot.Points["backup"].Status)
	}
	if _, ok := snapshot.Points["old-region"]; ok {
		t.Fatalf("old approximate point should be hidden: %#v", snapshot.Points["old-region"])
	}
	if snapshot.Annotations["ann-zhihu-1"].Annotation.Title != "西湖避坑经验" {
		t.Fatalf("annotation missing: %#v", snapshot.Annotations)
	}
	if snapshot.Routes["route-1"].Status != "selected" {
		t.Fatalf("route missing: %#v", snapshot.Routes)
	}
	if snapshot.Annotations["ann-review-1"].Annotation.Kind != "review" {
		t.Fatalf("review annotation missing: %#v", snapshot.Annotations)
	}
	if !snapshot.AnnotationFilters.Zhihu || !snapshot.AnnotationFilters.Thought || !snapshot.AnnotationFilters.Decision || !snapshot.AnnotationFilters.Review {
		t.Fatalf("annotation filters not initialized: %#v", snapshot.AnnotationFilters)
	}
}
