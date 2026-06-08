package agent

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"agent_v2/graph"
)

type travelRunSummary struct {
	RunID        string `json:"runId"`
	ThreadID     string `json:"threadId"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	Stage        string `json:"stage"`
	LastMessage  string `json:"lastMessage"`
	FinalSummary string `json:"finalSummary"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

type travelRunListResponse struct {
	Runs       []travelRunSummary `json:"runs"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

type travelHistoryMessage struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
}

type travelRunDetailResponse struct {
	Run         travelRunSummary       `json:"run"`
	Messages    []travelHistoryMessage `json:"messages"`
	Events      []PublicPlanningEvent  `json:"events"`
	MapSnapshot publicMapSnapshot      `json:"mapSnapshot"`
	FinalResult string                 `json:"finalResult"`
}

type publicMapSnapshot struct {
	ActiveLevel       string                           `json:"activeLevel"`
	FocusedNodeID     string                           `json:"focusedNodeId,omitempty"`
	Viewport          *PublicMapViewport               `json:"viewport,omitempty"`
	Points            map[string]publicPointState      `json:"points"`
	Routes            map[string]publicRouteState      `json:"routes"`
	Annotations       map[string]publicAnnotationState `json:"annotations"`
	AnnotationFilters publicAnnotationFilters          `json:"annotationFilters"`
	ShowDimmed        bool                             `json:"showDimmed"`
	LatestEvent       *PublicPlanningEvent             `json:"latestEvent,omitempty"`
}

type publicPointState struct {
	ID     string          `json:"id"`
	Level  string          `json:"level"`
	Status string          `json:"status"`
	Point  PublicMapPoint  `json:"point"`
	Popup  *PublicMapPopup `json:"popup,omitempty"`
	Reason string          `json:"reason,omitempty"`
}

type publicRouteState struct {
	ID     string               `json:"id"`
	Level  string               `json:"level"`
	Status string               `json:"status"`
	Route  PublicRouteCandidate `json:"route"`
	Reason string               `json:"reason,omitempty"`
}

type publicAnnotationState struct {
	ID         string              `json:"id"`
	Level      string              `json:"level"`
	Status     string              `json:"status"`
	Annotation PublicMapAnnotation `json:"annotation"`
	Reason     string              `json:"reason,omitempty"`
}

type publicAnnotationFilters struct {
	Zhihu    bool `json:"zhihu"`
	Thought  bool `json:"thought"`
	Decision bool `json:"decision"`
	Review   bool `json:"review"`
	Rejected bool `json:"rejected"`
}

func handleTravelRunList(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		writeCORSHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		http.Error(w, "userId is required", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	client := graph.GetClient()
	if client == nil || !client.IsEnabled() {
		writeJSON(w, travelRunListResponse{Runs: []travelRunSummary{}})
		return
	}
	runs, nextCursor, err := client.ListExplorationRunsByUser(r.Context(), userID, limit, cursor)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]travelRunSummary, 0, len(runs))
	for _, run := range runs {
		out = append(out, toTravelRunSummary(run))
	}
	writeJSON(w, travelRunListResponse{Runs: out, NextCursor: nextCursor})
}

func handleTravelRunResource(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		writeCORSHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/travel/runs/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}
	runID := parts[0]
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		http.Error(w, "userId is required", http.StatusBadRequest)
		return
	}

	detail, err := loadTravelRunDetail(r, userID, runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if detail == nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	response := buildTravelRunDetailResponse(detail)
	if len(parts) == 2 && parts[1] == "snapshot" {
		writeJSON(w, response.MapSnapshot)
		return
	}
	if len(parts) > 1 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, response)
}

func loadTravelRunDetail(r *http.Request, userID, runID string) (*graph.ExplorationRunDetail, error) {
	client := graph.GetClient()
	if client == nil || !client.IsEnabled() {
		return nil, nil
	}
	return client.GetExplorationRunDetail(r.Context(), userID, runID)
}

func buildTravelRunDetailResponse(detail *graph.ExplorationRunDetail) travelRunDetailResponse {
	events := make([]PublicPlanningEvent, 0, len(detail.Steps))
	messages := make([]travelHistoryMessage, 0)
	var assistant *travelHistoryMessage

	flushAssistant := func() {
		if assistant != nil && strings.TrimSpace(assistant.Content) != "" {
			messages = append(messages, *assistant)
		}
		assistant = nil
	}

	for _, step := range detail.Steps {
		if step.PayloadJSON != "" {
			var ev PublicPlanningEvent
			if err := json.Unmarshal([]byte(step.PayloadJSON), &ev); err == nil && ev.Type != "" {
				events = append(events, ev)
			}
		}
		switch {
		case step.ActionType == historyActionChatUser:
			flushAssistant()
			messages = append(messages, travelHistoryMessage{
				ID:        step.ID,
				Role:      "user",
				Content:   step.Message,
				Status:    "done",
				CreatedAt: step.CreatedAt,
			})
		case step.EventType == EventChatMessageDelta:
			if assistant == nil {
				assistant = &travelHistoryMessage{
					ID:        step.ID,
					Role:      "assistant",
					Status:    "done",
					CreatedAt: step.CreatedAt,
				}
			}
			assistant.Content += step.Message
		case step.EventType == EventPlanningError && strings.TrimSpace(step.Message) != "":
			flushAssistant()
			messages = append(messages, travelHistoryMessage{
				ID:        step.ID,
				Role:      "assistant",
				Content:   step.Message,
				Status:    "error",
				CreatedAt: step.CreatedAt,
			})
		}
	}
	flushAssistant()

	finalResult := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && strings.TrimSpace(messages[i].Content) != "" {
			finalResult = messages[i].Content
			break
		}
	}

	return travelRunDetailResponse{
		Run:         toTravelRunSummary(detail.Run),
		Messages:    messages,
		Events:      events,
		MapSnapshot: buildPublicMapSnapshot(events),
		FinalResult: finalResult,
	}
}

func toTravelRunSummary(run graph.ExplorationRunNode) travelRunSummary {
	return travelRunSummary{
		RunID:        run.ID,
		ThreadID:     run.ThreadID,
		Title:        defaultHistoryText(run.Title, "旅行规划"),
		Status:       run.Status,
		Stage:        run.Stage,
		LastMessage:  run.LastMessage,
		FinalSummary: run.FinalSummary,
		CreatedAt:    run.CreatedAt,
		UpdatedAt:    run.UpdatedAt,
	}
}

func defaultHistoryText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func buildPublicMapSnapshot(events []PublicPlanningEvent) publicMapSnapshot {
	state := publicMapSnapshot{
		ActiveLevel: "overview",
		Points:      map[string]publicPointState{},
		Routes:      map[string]publicRouteState{},
		Annotations: map[string]publicAnnotationState{},
		AnnotationFilters: publicAnnotationFilters{
			Zhihu:    true,
			Thought:  true,
			Decision: true,
			Review:   true,
			Rejected: true,
		},
		ShowDimmed: true,
	}
	for _, ev := range events {
		applyPublicMapEvent(&state, ev)
	}
	return state
}

func applyPublicMapEvent(state *publicMapSnapshot, ev PublicPlanningEvent) {
	if ev.Type == EventMapBatch {
		for _, child := range ev.Events {
			applyPublicMapEvent(state, child)
		}
		setLatestMapEvent(state, ev)
		return
	}
	switch ev.Type {
	case EventMapScopeChanged:
		if ev.Level != "" {
			state.ActiveLevel = ev.Level
		}
		if ev.Viewport != nil {
			state.Viewport = ev.Viewport
		}
		if ev.NodeID != "" {
			state.FocusedNodeID = ev.NodeID
		}
	case EventMapPointAdded, EventMapPointUpdated, EventMapPointSoftDeleted:
		if ev.NodeID == "" || !isExactPublicMapPoint(ev.Point) {
			break
		}
		status := ev.Status
		if ev.Type == EventMapPointSoftDeleted {
			status = "dimmed"
		}
		if status == "" {
			status = "active"
		}
		level := ev.Level
		if level == "" {
			level = state.ActiveLevel
		}
		state.Points[ev.NodeID] = publicPointState{
			ID:     ev.NodeID,
			Level:  level,
			Status: status,
			Point:  *ev.Point,
			Popup:  ev.Popup,
			Reason: ev.Reason,
		}
		state.FocusedNodeID = ev.NodeID
	case EventRouteCandidateAdded, EventRouteCandidateUpdated, EventRouteSelected, EventRouteDimmed:
		routeID := ev.RouteID
		if routeID == "" && ev.Route != nil {
			routeID = ev.Route.ID
		}
		if routeID == "" || ev.Route == nil {
			break
		}
		status := ev.Status
		if ev.Type == EventRouteSelected {
			status = "selected"
		}
		if ev.Type == EventRouteDimmed {
			status = "dimmed"
		}
		if status == "" {
			status = ev.Route.Status
		}
		if status == "" {
			status = "candidate"
		}
		level := ev.Level
		if level == "" {
			level = state.ActiveLevel
		}
		route := *ev.Route
		route.Status = status
		state.Routes[routeID] = publicRouteState{
			ID:     routeID,
			Level:  level,
			Status: status,
			Route:  route,
			Reason: ev.Reason,
		}
	case EventMapAnnotationAdded, EventMapAnnotationUpdated, EventMapAnnotationDimmed:
		if ev.Annotation == nil || strings.TrimSpace(ev.Annotation.ID) == "" {
			break
		}
		status := ev.Status
		if ev.Type == EventMapAnnotationDimmed {
			status = "dimmed"
		}
		if status == "" {
			status = ev.Annotation.Status
		}
		if status == "" {
			status = "active"
		}
		level := ev.Level
		if level == "" {
			level = state.ActiveLevel
		}
		annotation := *ev.Annotation
		annotation.Status = status
		state.Annotations[annotation.ID] = publicAnnotationState{
			ID:         annotation.ID,
			Level:      level,
			Status:     status,
			Annotation: annotation,
			Reason:     ev.Reason,
		}
	}
	setLatestMapEvent(state, ev)
}

func isExactPublicMapPoint(point *PublicMapPoint) bool {
	return point != nil && point.Accuracy == "exact" && isValidLngLat(point.Lng, point.Lat)
}

func setLatestMapEvent(state *publicMapSnapshot, ev PublicPlanningEvent) {
	copied := ev
	state.LatestEvent = &copied
}
