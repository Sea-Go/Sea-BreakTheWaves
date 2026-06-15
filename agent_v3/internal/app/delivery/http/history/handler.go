package history

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"

	domainevents "agent_v3/internal/domain/events"
	"agent_v3/internal/graph"
	historyrec "agent_v3/internal/history"
)

func RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/travel/runs", handleTravelRunList)
	mux.HandleFunc("/travel/runs/", handleTravelRunResource)
}

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
	Run         travelRunSummary                   `json:"run"`
	Messages    []travelHistoryMessage             `json:"messages"`
	Events      []domainevents.PublicPlanningEvent `json:"events"`
	MapSnapshot publicMapSnapshot                  `json:"mapSnapshot"`
	FinalResult string                             `json:"finalResult"`
}

type publicMapSnapshot struct {
	ActiveLevel       string                            `json:"activeLevel"`
	FocusedNodeID     string                            `json:"focusedNodeId,omitempty"`
	Viewport          *domainevents.PublicMapViewport   `json:"viewport,omitempty"`
	Points            map[string]publicPointState       `json:"points"`
	Routes            map[string]publicRouteState       `json:"routes"`
	Annotations       map[string]publicAnnotationState  `json:"annotations"`
	AnnotationFilters publicAnnotationFilters           `json:"annotationFilters"`
	ShowDimmed        bool                              `json:"showDimmed"`
	LatestEvent       *domainevents.PublicPlanningEvent `json:"latestEvent,omitempty"`
}

type publicPointState struct {
	ID     string                       `json:"id"`
	Level  string                       `json:"level"`
	Status string                       `json:"status"`
	Point  domainevents.PublicMapPoint  `json:"point"`
	Popup  *domainevents.PublicMapPopup `json:"popup,omitempty"`
	Reason string                       `json:"reason,omitempty"`
}

type publicRouteState struct {
	ID     string                            `json:"id"`
	Level  string                            `json:"level"`
	Status string                            `json:"status"`
	Route  domainevents.PublicRouteCandidate `json:"route"`
	Reason string                            `json:"reason,omitempty"`
}

type publicAnnotationState struct {
	ID         string                           `json:"id"`
	Level      string                           `json:"level"`
	Status     string                           `json:"status"`
	Annotation domainevents.PublicMapAnnotation `json:"annotation"`
	Reason     string                           `json:"reason,omitempty"`
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
	events := make([]domainevents.PublicPlanningEvent, 0, len(detail.Steps))
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
			var ev domainevents.PublicPlanningEvent
			if err := json.Unmarshal([]byte(step.PayloadJSON), &ev); err == nil && ev.Type != "" {
				events = append(events, ev)
			}
		}
		switch {
		case step.ActionType == historyrec.ActionChatUser:
			flushAssistant()
			messages = append(messages, travelHistoryMessage{
				ID:        step.ID,
				Role:      "user",
				Content:   step.Message,
				Status:    "done",
				CreatedAt: step.CreatedAt,
			})
		case step.EventType == domainevents.EventChatMessageDelta:
			if assistant == nil {
				assistant = &travelHistoryMessage{
					ID:        step.ID,
					Role:      "assistant",
					Status:    "done",
					CreatedAt: step.CreatedAt,
				}
			}
			assistant.Content += step.Message
		case step.EventType == domainevents.EventPlanningError && strings.TrimSpace(step.Message) != "":
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

func buildPublicMapSnapshot(events []domainevents.PublicPlanningEvent) publicMapSnapshot {
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

func applyPublicMapEvent(state *publicMapSnapshot, ev domainevents.PublicPlanningEvent) {
	if ev.Type == domainevents.EventMapBatch {
		for _, child := range ev.Events {
			applyPublicMapEvent(state, child)
		}
		setLatestMapEvent(state, ev)
		return
	}
	switch ev.Type {
	case domainevents.EventMapScopeChanged:
		if ev.Level != "" {
			state.ActiveLevel = ev.Level
		}
		if ev.Viewport != nil {
			state.Viewport = ev.Viewport
		}
		if ev.NodeID != "" {
			state.FocusedNodeID = ev.NodeID
		}
	case domainevents.EventMapPointAdded, domainevents.EventMapPointUpdated, domainevents.EventMapPointSoftDeleted:
		if ev.NodeID == "" || !isExactPublicMapPoint(ev.Point) {
			break
		}
		status := ev.Status
		if ev.Type == domainevents.EventMapPointSoftDeleted {
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
	case domainevents.EventRouteCandidateAdded, domainevents.EventRouteCandidateUpdated, domainevents.EventRouteSelected, domainevents.EventRouteDimmed:
		routeID := ev.RouteID
		if routeID == "" && ev.Route != nil {
			routeID = ev.Route.ID
		}
		if routeID == "" || ev.Route == nil {
			break
		}
		status := ev.Status
		if ev.Type == domainevents.EventRouteSelected {
			status = "selected"
		}
		if ev.Type == domainevents.EventRouteDimmed {
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
	case domainevents.EventMapAnnotationAdded, domainevents.EventMapAnnotationUpdated, domainevents.EventMapAnnotationDimmed:
		if ev.Annotation == nil || strings.TrimSpace(ev.Annotation.ID) == "" {
			break
		}
		status := ev.Status
		if ev.Type == domainevents.EventMapAnnotationDimmed {
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

func isExactPublicMapPoint(point *domainevents.PublicMapPoint) bool {
	return point != nil && point.Accuracy == "exact" && isValidLngLat(point.Lng, point.Lat)
}

func setLatestMapEvent(state *publicMapSnapshot, ev domainevents.PublicPlanningEvent) {
	copied := ev
	state.LatestEvent = &copied
}

func isValidLngLat(lng, lat float64) bool {
	if math.IsNaN(lng) || math.IsNaN(lat) || math.IsInf(lng, 0) || math.IsInf(lat, 0) {
		return false
	}
	if lng == 0 && lat == 0 {
		return false
	}
	return lng >= -180 && lng <= 180 && lat >= -90 && lat <= 90
}

func writeCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func writeJSON(w http.ResponseWriter, payload any) {
	writeCORSHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(payload)
}
