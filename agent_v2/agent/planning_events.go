package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
)

const runtimeTraceEmitterKey = "travel_trace_emitter"

type traceEmitterContextKey struct{}

const (
	EventChatMessageDelta      = "chat_message_delta"
	EventPlanningStageChanged  = "planning_stage_changed"
	EventMapScopeChanged       = "map_scope_changed"
	EventMapPointAdded         = "map_point_added"
	EventMapPointUpdated       = "map_point_updated"
	EventMapPointSoftDeleted   = "map_point_soft_deleted"
	EventRouteCandidateAdded   = "route_candidate_added"
	EventRouteCandidateUpdated = "route_candidate_updated"
	EventRouteSelected         = "route_selected"
	EventRouteDimmed           = "route_dimmed"
	EventMapAnnotationAdded    = "map_annotation_added"
	EventMapAnnotationUpdated  = "map_annotation_updated"
	EventMapAnnotationDimmed   = "map_annotation_dimmed"
	EventMapBatch              = "map_batch"
	EventPlanningCompleted     = "planning_completed"
	EventPlanningError         = "planning_error"
)

type PublicPlanningEvent struct {
	Type           string                `json:"type"`
	RunID          string                `json:"runId"`
	Seq            int64                 `json:"seq"`
	Level          string                `json:"level,omitempty"`
	NodeID         string                `json:"nodeId,omitempty"`
	RouteID        string                `json:"routeId,omitempty"`
	Stage          string                `json:"stage,omitempty"`
	Status         string                `json:"status,omitempty"`
	PublicAction   string                `json:"publicAction,omitempty"`
	ThoughtSummary string                `json:"thoughtSummary,omitempty"`
	RecordedFacts  []string              `json:"recordedFacts,omitempty"`
	Point          *PublicMapPoint       `json:"point,omitempty"`
	Popup          *PublicMapPopup       `json:"popup,omitempty"`
	Viewport       *PublicMapViewport    `json:"viewport,omitempty"`
	Route          *PublicRouteCandidate `json:"route,omitempty"`
	Annotation     *PublicMapAnnotation  `json:"annotation,omitempty"`
	Reason         string                `json:"reason,omitempty"`
	Message        string                `json:"message,omitempty"`
	Events         []PublicPlanningEvent `json:"events,omitempty"`
	Metadata       map[string]any        `json:"metadata,omitempty"`
	Usage          *PublicModelUsage     `json:"usage,omitempty"`
	CreatedAt      string                `json:"createdAt"`
}

type PublicModelUsage struct {
	AgentLabel       string `json:"agentLabel,omitempty"`
	Model            string `json:"model,omitempty"`
	ModelLevel       string `json:"modelLevel,omitempty"`
	PromptTokens     int    `json:"promptTokens,omitempty"`
	CompletionTokens int    `json:"completionTokens,omitempty"`
	TotalTokens      int    `json:"totalTokens,omitempty"`
}

type PublicMapPoint struct {
	Lng           float64 `json:"lng"`
	Lat           float64 `json:"lat"`
	Label         string  `json:"label"`
	Kind          string  `json:"kind"`
	Accuracy      string  `json:"accuracy,omitempty"`
	Source        string  `json:"source,omitempty"`
	Address       string  `json:"address,omitempty"`
	City          string  `json:"city,omitempty"`
	District      string  `json:"district,omitempty"`
	Category      string  `json:"category,omitempty"`
	Description   string  `json:"description,omitempty"`
	Notes         string  `json:"notes,omitempty"`
	VisitOrder    int     `json:"visitOrder,omitempty"`
	StartTime     string  `json:"startTime,omitempty"`
	EndTime       string  `json:"endTime,omitempty"`
	DurationMin   int     `json:"durationMin,omitempty"`
	EstimatedCost float64 `json:"estimatedCost,omitempty"`
	PhaseID       string  `json:"phaseId,omitempty"`
	PhaseSeq      int     `json:"phaseSeq,omitempty"`
	PhaseName     string  `json:"phaseName,omitempty"`
	DayID         string  `json:"dayId,omitempty"`
	DayIndex      int     `json:"dayIndex,omitempty"`
}

type PublicMapPopup struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type PublicMapViewport struct {
	Center [2]float64 `json:"center"`
	Zoom   int        `json:"zoom"`
}

type PublicMapAnnotation struct {
	ID         string                    `json:"id"`
	Kind       string                    `json:"kind"`
	Source     string                    `json:"source,omitempty"`
	Title      string                    `json:"title"`
	Summary    string                    `json:"summary,omitempty"`
	URL        string                    `json:"url,omitempty"`
	AuthorName string                    `json:"authorName,omitempty"`
	Score      float64                   `json:"score,omitempty"`
	Status     string                    `json:"status"`
	Tags       []string                  `json:"tags,omitempty"`
	Reasons    []string                  `json:"reasons,omitempty"`
	Evidence   []string                  `json:"evidence,omitempty"`
	Anchor     PublicMapAnnotationAnchor `json:"anchor"`
}

type PublicMapAnnotationAnchor struct {
	Type    string          `json:"type"`
	NodeID  string          `json:"nodeId,omitempty"`
	RouteID string          `json:"routeId,omitempty"`
	Label   string          `json:"label,omitempty"`
	Point   *PublicMapPoint `json:"point,omitempty"`
}

type PublicRouteCandidate struct {
	ID             string       `json:"id"`
	Label          string       `json:"label"`
	Status         string       `json:"status"`
	Mode           string       `json:"mode"`
	Accuracy       string       `json:"accuracy,omitempty"`
	Source         string       `json:"source,omitempty"`
	PhaseID        string       `json:"phaseId,omitempty"`
	PhaseSeq       int          `json:"phaseSeq,omitempty"`
	PhaseName      string       `json:"phaseName,omitempty"`
	DayID          string       `json:"dayId,omitempty"`
	DayIndex       int          `json:"dayIndex,omitempty"`
	SegmentIndex   int          `json:"segmentIndex,omitempty"`
	FromNodeID     string       `json:"fromNodeId,omitempty"`
	ToNodeID       string       `json:"toNodeId,omitempty"`
	ConnectionType string       `json:"connectionType,omitempty"`
	DistanceMeters float64      `json:"distanceMeters,omitempty"`
	DurationMin    float64      `json:"durationMin,omitempty"`
	EstimatedCost  float64      `json:"estimatedCost,omitempty"`
	Polyline       [][2]float64 `json:"polyline,omitempty"`
	Reason         string       `json:"reason,omitempty"`
	Score          float64      `json:"score,omitempty"`
}

type PublicPlanningEventRecorder interface {
	RecordPlanningEvent(ctx context.Context, ev PublicPlanningEvent)
}

type TraceEmitter struct {
	runID    string
	mu       sync.Mutex
	seq      int64
	out      chan PublicPlanningEvent
	done     bool
	recorder PublicPlanningEventRecorder
}

func NewTraceEmitter(runID string, recorders ...PublicPlanningEventRecorder) *TraceEmitter {
	if strings.TrimSpace(runID) == "" {
		runID = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	var recorder PublicPlanningEventRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &TraceEmitter{
		runID:    runID,
		out:      make(chan PublicPlanningEvent, 256),
		recorder: recorder,
	}
}

func (e *TraceEmitter) Events() <-chan PublicPlanningEvent {
	return e.out
}

func (e *TraceEmitter) RunID() string {
	if e == nil {
		return ""
	}
	return e.runID
}

func (e *TraceEmitter) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return
	}
	e.done = true
	close(e.out)
}

func (e *TraceEmitter) Emit(ctx context.Context, ev PublicPlanningEvent) bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	if e.done {
		e.mu.Unlock()
		return false
	}
	e.seq++
	ev.Seq = e.seq
	ev.RunID = e.runID
	ev.CreatedAt = time.Now().Format(time.RFC3339Nano)
	ev = SanitizePublicPlanningEvent(ev)
	recorder := e.recorder
	e.mu.Unlock()

	if recorder != nil {
		recorder.RecordPlanningEvent(ctx, ev)
	}

	select {
	case e.out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *TraceEmitter) EmitChatDelta(ctx context.Context, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	e.Emit(ctx, PublicPlanningEvent{
		Type:    EventChatMessageDelta,
		Message: text,
	})
}

func (e *TraceEmitter) EmitStage(ctx context.Context, stage, status, action, summary string) {
	e.Emit(ctx, PublicPlanningEvent{
		Type:           EventPlanningStageChanged,
		Stage:          stage,
		Status:         status,
		PublicAction:   action,
		ThoughtSummary: summary,
	})
	if status != "waiting" && (strings.TrimSpace(action) != "" || strings.TrimSpace(summary) != "") {
		e.Emit(ctx, PublicPlanningEvent{
			Type:           EventMapAnnotationAdded,
			Level:          publicLevelForStage(stage),
			Stage:          stage,
			Status:         "active",
			PublicAction:   action,
			ThoughtSummary: summary,
			Annotation: &PublicMapAnnotation{
				ID:      fmt.Sprintf("ann-thought-%s-%d", strings.ReplaceAll(stage, "_", "-"), time.Now().UnixNano()),
				Kind:    "thought",
				Source:  "planning",
				Title:   defaultPublicAnnotationTitle(action, "规划思考"),
				Summary: summary,
				Status:  "active",
				Anchor: PublicMapAnnotationAnchor{
					Type:  "scope",
					Label: defaultPublicAnnotationTitle(stage, "当前规划阶段"),
				},
			},
		})
	}
}

func (e *TraceEmitter) EmitError(ctx context.Context, message string) {
	e.Emit(ctx, PublicPlanningEvent{
		Type:    EventPlanningError,
		Status:  "failed",
		Message: message,
	})
}

func (e *TraceEmitter) EmitCompleted(ctx context.Context, message string) {
	e.Emit(ctx, PublicPlanningEvent{
		Type:    EventPlanningCompleted,
		Status:  "completed",
		Message: message,
	})
}

func (e *TraceEmitter) EmitModelUsage(ctx context.Context, usage PublicModelUsage, stage string) {
	if usage.TotalTokens <= 0 && usage.PromptTokens <= 0 && usage.CompletionTokens <= 0 {
		return
	}
	stage = defaultIfEmpty(stage, "planning")
	label := defaultIfEmpty(usage.AgentLabel, "模型调用")
	e.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapAnnotationAdded,
		Level:          publicLevelForStage(stage),
		Stage:          stage,
		Status:         "completed",
		PublicAction:   "记录模型用量",
		ThoughtSummary: fmt.Sprintf("%s 使用 %s，消耗 %d token。", label, defaultIfEmpty(usage.Model, "模型"), usage.TotalTokens),
		Usage:          &usage,
		Annotation: &PublicMapAnnotation{
			ID:      fmt.Sprintf("ann-model-usage-%s-%d", strings.ReplaceAll(stage, "_", "-"), time.Now().UnixNano()),
			Kind:    "model_usage",
			Source:  "model",
			Title:   label,
			Summary: fmt.Sprintf("模型：%s；Token：%d（输入 %d / 输出 %d）。", defaultIfEmpty(usage.Model, "未知模型"), usage.TotalTokens, usage.PromptTokens, usage.CompletionTokens),
			Status:  "completed",
			Tags:    []string{"模型", "Token"},
			Anchor: PublicMapAnnotationAnchor{
				Type:  "scope",
				Label: defaultPublicAnnotationTitle(stage, "当前规划阶段"),
			},
		},
	})
}

func traceEmitterFromInvocation(inv *agentcore.Invocation) *TraceEmitter {
	if inv == nil || inv.RunOptions.RuntimeState == nil {
		return nil
	}
	if emitter, ok := inv.RunOptions.RuntimeState[runtimeTraceEmitterKey].(*TraceEmitter); ok {
		return emitter
	}
	return nil
}

func traceEmitterFromContext(ctx context.Context) *TraceEmitter {
	if ctx == nil {
		return nil
	}
	if emitter, ok := ctx.Value(traceEmitterContextKey{}).(*TraceEmitter); ok {
		return emitter
	}
	return nil
}

func SanitizePublicPlanningEvent(ev PublicPlanningEvent) PublicPlanningEvent {
	ev.PublicAction = sanitizePublicText(ev.PublicAction)
	ev.ThoughtSummary = sanitizePublicText(ev.ThoughtSummary)
	ev.Reason = sanitizePublicText(ev.Reason)
	ev.Message = sanitizePublicText(ev.Message)
	if ev.Popup != nil {
		ev.Popup.Title = sanitizePublicText(ev.Popup.Title)
		ev.Popup.Content = sanitizePublicText(ev.Popup.Content)
	}
	if ev.Point != nil {
		ev.Point.Label = sanitizePublicText(ev.Point.Label)
		ev.Point.Kind = sanitizePublicText(ev.Point.Kind)
		ev.Point.Accuracy = sanitizePublicText(ev.Point.Accuracy)
		ev.Point.Source = sanitizePublicText(ev.Point.Source)
		ev.Point.Address = sanitizePublicText(ev.Point.Address)
		ev.Point.City = sanitizePublicText(ev.Point.City)
		ev.Point.District = sanitizePublicText(ev.Point.District)
		ev.Point.Category = sanitizePublicText(ev.Point.Category)
		ev.Point.Description = sanitizePublicText(ev.Point.Description)
		ev.Point.Notes = sanitizePublicText(ev.Point.Notes)
		ev.Point.StartTime = sanitizePublicText(ev.Point.StartTime)
		ev.Point.EndTime = sanitizePublicText(ev.Point.EndTime)
		ev.Point.PhaseID = sanitizePublicText(ev.Point.PhaseID)
		ev.Point.PhaseName = sanitizePublicText(ev.Point.PhaseName)
		ev.Point.DayID = sanitizePublicText(ev.Point.DayID)
	}
	if ev.Route != nil {
		ev.Route.Label = sanitizePublicText(ev.Route.Label)
		ev.Route.Accuracy = sanitizePublicText(ev.Route.Accuracy)
		ev.Route.Source = sanitizePublicText(ev.Route.Source)
		ev.Route.PhaseID = sanitizePublicText(ev.Route.PhaseID)
		ev.Route.PhaseName = sanitizePublicText(ev.Route.PhaseName)
		ev.Route.DayID = sanitizePublicText(ev.Route.DayID)
		ev.Route.FromNodeID = sanitizePublicText(ev.Route.FromNodeID)
		ev.Route.ToNodeID = sanitizePublicText(ev.Route.ToNodeID)
		ev.Route.ConnectionType = sanitizePublicText(ev.Route.ConnectionType)
		ev.Route.Reason = sanitizePublicText(ev.Route.Reason)
	}
	if ev.Usage != nil {
		ev.Usage.AgentLabel = sanitizePublicText(ev.Usage.AgentLabel)
		ev.Usage.Model = sanitizePublicText(ev.Usage.Model)
		ev.Usage.ModelLevel = sanitizePublicText(ev.Usage.ModelLevel)
	}
	if ev.Annotation != nil {
		ev.Annotation.ID = sanitizePublicText(ev.Annotation.ID)
		ev.Annotation.Kind = sanitizePublicText(ev.Annotation.Kind)
		ev.Annotation.Source = sanitizePublicText(ev.Annotation.Source)
		ev.Annotation.Title = sanitizePublicText(ev.Annotation.Title)
		ev.Annotation.Summary = sanitizePublicText(ev.Annotation.Summary)
		ev.Annotation.AuthorName = sanitizePublicText(ev.Annotation.AuthorName)
		ev.Annotation.Status = sanitizePublicText(ev.Annotation.Status)
		for i := range ev.Annotation.Tags {
			ev.Annotation.Tags[i] = sanitizePublicText(ev.Annotation.Tags[i])
		}
		for i := range ev.Annotation.Reasons {
			ev.Annotation.Reasons[i] = sanitizePublicText(ev.Annotation.Reasons[i])
		}
		for i := range ev.Annotation.Evidence {
			ev.Annotation.Evidence[i] = sanitizePublicText(ev.Annotation.Evidence[i])
		}
		ev.Annotation.Anchor.Type = sanitizePublicText(ev.Annotation.Anchor.Type)
		ev.Annotation.Anchor.NodeID = sanitizePublicText(ev.Annotation.Anchor.NodeID)
		ev.Annotation.Anchor.RouteID = sanitizePublicText(ev.Annotation.Anchor.RouteID)
		ev.Annotation.Anchor.Label = sanitizePublicText(ev.Annotation.Anchor.Label)
		if ev.Annotation.Anchor.Point != nil {
			ev.Annotation.Anchor.Point.Label = sanitizePublicText(ev.Annotation.Anchor.Point.Label)
			ev.Annotation.Anchor.Point.Kind = sanitizePublicText(ev.Annotation.Anchor.Point.Kind)
			ev.Annotation.Anchor.Point.Accuracy = sanitizePublicText(ev.Annotation.Anchor.Point.Accuracy)
			ev.Annotation.Anchor.Point.Source = sanitizePublicText(ev.Annotation.Anchor.Point.Source)
			ev.Annotation.Anchor.Point.Address = sanitizePublicText(ev.Annotation.Anchor.Point.Address)
			ev.Annotation.Anchor.Point.City = sanitizePublicText(ev.Annotation.Anchor.Point.City)
			ev.Annotation.Anchor.Point.District = sanitizePublicText(ev.Annotation.Anchor.Point.District)
			ev.Annotation.Anchor.Point.Category = sanitizePublicText(ev.Annotation.Anchor.Point.Category)
			ev.Annotation.Anchor.Point.Description = sanitizePublicText(ev.Annotation.Anchor.Point.Description)
			ev.Annotation.Anchor.Point.Notes = sanitizePublicText(ev.Annotation.Anchor.Point.Notes)
			ev.Annotation.Anchor.Point.StartTime = sanitizePublicText(ev.Annotation.Anchor.Point.StartTime)
			ev.Annotation.Anchor.Point.EndTime = sanitizePublicText(ev.Annotation.Anchor.Point.EndTime)
		}
	}
	for i := range ev.RecordedFacts {
		ev.RecordedFacts[i] = sanitizePublicText(ev.RecordedFacts[i])
	}
	for i := range ev.Events {
		ev.Events[i] = SanitizePublicPlanningEvent(ev.Events[i])
	}
	return ev
}

func publicLevelForStage(stage string) string {
	switch stage {
	case "day_expansion", "review", "final_output":
		return "day"
	case "graph_splitting":
		return "phase"
	case "macro_planning":
		return "overview"
	default:
		return "overview"
	}
}

func defaultPublicAnnotationTitle(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func sanitizePublicText(text string) string {
	if text == "" {
		return ""
	}
	replacements := map[string]string{
		"amap_":                "地图能力",
		"zhihu_guide_material": "攻略素材能力",
		"write_guide_insight":  "攻略洞察能力",
		"create_trip_plan":     "规划写入能力",
		"split_parent_node":    "层级拆分能力",
		"get_weather_context":  "天气查询能力",
		"get_trip_overview":    "规划读取能力",
		"review-workflow":      "流程审核",
		"review-thinking":      "思路审核",
		"review-content":       "内容审核",
		"review-output":        "输出审核",
		"review-laziness":      "完整性审核",
		"review-poi":           "地点审核",
		"review-week":          "周级审核",
		"tool":                 "能力",
		"Tool":                 "能力",
	}
	out := text
	for old, next := range replacements {
		out = strings.ReplaceAll(out, old, next)
	}
	return out
}
