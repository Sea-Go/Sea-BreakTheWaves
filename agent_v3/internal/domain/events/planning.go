package events

import "context"

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
