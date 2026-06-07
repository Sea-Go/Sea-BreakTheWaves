package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"agent_v2/config"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
)

type travelStreamMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type travelStreamRequest struct {
	ThreadID  string                `json:"threadId"`
	RunID     string                `json:"runId"`
	UserID    string                `json:"userId"`
	ResumeSeq int64                 `json:"resumeSeq"`
	Messages  []travelStreamMessage `json:"messages"`
}

var travelStreamAgentCache struct {
	mu    sync.Mutex
	agent agentcore.Agent
}

func NewTravelPlanningStreamHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/travel/stream", handleTravelStream)
	mux.HandleFunc("/travel/runs", handleTravelRunList)
	mux.HandleFunc("/travel/runs/", handleTravelRunResource)
	mux.HandleFunc("/travel/routes/", handleTravelRoutePolyline)
	return mux
}

func handleTravelStream(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		writeCORSHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req travelStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	userMessage := userMessageFromHistory(req.Messages)
	if strings.TrimSpace(userMessage) == "" {
		http.Error(w, "messages must contain user content", http.StatusBadRequest)
		return
	}
	if req.ThreadID == "" {
		req.ThreadID = fmt.Sprintf("thread-%d", time.Now().UnixNano())
	}
	if req.UserID == "" {
		req.UserID = "travel-user"
	}
	if req.RunID == "" {
		req.RunID = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}

	writeStreamHeaders(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	recorder := newTravelHistoryRecorder(req)
	if recorder != nil {
		recorder.Start(ctx, firstUserMessage(req.Messages), latestUserMessage(req.Messages))
	}
	emitter := NewTraceEmitter(req.RunID, recorder)
	defer emitter.Close()

	streamCtx := context.WithValue(ctx, traceEmitterContextKey{}, emitter)
	go runTravelPlanningForStream(streamCtx, req, userMessage, emitter)

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-emitter.Events():
			if !ok {
				return
			}
			if err := writePlanningSSE(w, ev); err != nil {
				log.Errorf("[travel-stream] write event failed: %v", err)
				return
			}
			flusher.Flush()
		}
	}
}

func runTravelPlanningForStream(ctx context.Context, req travelStreamRequest, userMessage string, emitter *TraceEmitter) {
	defer emitter.Close()

	emitter.EmitStage(ctx, "requirement_intake", "running", "识别需求", "正在读取出发地、时间、预算、交通方式和旅行偏好。")

	appName := config.Cfg.Agent.AppName + "travel-stream"
	rn := runner.NewRunner(appName, getTravelStreamAgent())
	defer rn.Close()

	eventCh, err := rn.Run(
		ctx,
		req.UserID,
		req.ThreadID,
		model.NewUserMessage(userMessage),
		agentcore.WithStream(true),
		agentcore.MergeRuntimeState(map[string]any{
			runtimeTraceEmitterKey: emitter,
			"travelRunID":          req.RunID,
			"threadID":             req.ThreadID,
		}),
	)
	if err != nil {
		emitter.EmitError(ctx, fmt.Sprintf("规划启动失败: %v", err))
		return
	}

	for evt := range eventCh {
		if evt == nil || evt.Response == nil {
			continue
		}
		for _, choice := range evt.Response.Choices {
			if choice.Delta.Content != "" {
				emitter.EmitChatDelta(ctx, choice.Delta.Content)
			} else if choice.Message.Content != "" {
				emitter.EmitChatDelta(ctx, choice.Message.Content)
			}
		}
	}

	emitter.EmitCompleted(ctx, "规划流已完成。")
}

func getTravelStreamAgent() agentcore.Agent {
	travelStreamAgentCache.mu.Lock()
	defer travelStreamAgentCache.mu.Unlock()
	if travelStreamAgentCache.agent == nil {
		travelStreamAgentCache.agent = TravelPlanningAgent()
	}
	return travelStreamAgentCache.agent
}

func handleTravelRoutePolyline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	routeID := strings.TrimPrefix(r.URL.Path, "/travel/routes/")
	routeID = strings.TrimSuffix(routeID, "/polyline")
	if routeID == "" || strings.Contains(routeID, "/") {
		http.Error(w, "invalid route id", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"routeId":  routeID,
		"polyline": [][2]float64{},
	})
}

func writeStreamHeaders(w http.ResponseWriter) {
	writeCORSHeaders(w)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

func writeCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func writePlanningSSE(w http.ResponseWriter, ev PublicPlanningEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, string(b))
	return err
}

func writeJSON(w http.ResponseWriter, payload any) {
	writeCORSHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(payload)
}

func userMessageFromHistory(messages []travelStreamMessage) string {
	userParts := make([]string, 0, len(messages))
	for _, message := range messages {
		if strings.EqualFold(message.Role, "user") && strings.TrimSpace(message.Content) != "" {
			userParts = append(userParts, strings.TrimSpace(message.Content))
		}
	}
	if len(userParts) == 1 {
		return userParts[0]
	}
	if len(userParts) > 1 {
		lines := make([]string, 0, len(userParts))
		for i, content := range userParts {
			lines = append(lines, fmt.Sprintf("用户第%d轮：%s", i+1, content))
		}
		return strings.Join(lines, "\n")
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.TrimSpace(messages[i].Content) != "" {
			return strings.TrimSpace(messages[i].Content)
		}
	}
	return ""
}

func firstUserMessage(messages []travelStreamMessage) string {
	for _, message := range messages {
		if strings.EqualFold(message.Role, "user") && strings.TrimSpace(message.Content) != "" {
			return strings.TrimSpace(message.Content)
		}
	}
	return latestUserMessage(messages)
}

func latestUserMessage(messages []travelStreamMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(messages[i].Role, "user") && strings.TrimSpace(messages[i].Content) != "" {
			return strings.TrimSpace(messages[i].Content)
		}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.TrimSpace(messages[i].Content) != "" {
			return strings.TrimSpace(messages[i].Content)
		}
	}
	return ""
}

func buildEmptyPlanningSnapshot(runID string) map[string]any {
	return map[string]any{
		"runId":       runID,
		"activeLevel": "overview",
		"points":      map[string]any{},
		"routes":      map[string]any{},
	}
}
