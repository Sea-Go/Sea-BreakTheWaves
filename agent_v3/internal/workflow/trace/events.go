package trace

import (
	"context"
	"sync"

	domainevents "agent_v3/internal/domain/events"
)

const runtimeTraceEmitterKey = "travel_trace_emitter"

type traceEmitterContextKey struct{}

const (
	EventChatMessageDelta      = domainevents.EventChatMessageDelta
	EventPlanningStageChanged  = domainevents.EventPlanningStageChanged
	EventMapScopeChanged       = domainevents.EventMapScopeChanged
	EventMapPointAdded         = domainevents.EventMapPointAdded
	EventMapPointUpdated       = domainevents.EventMapPointUpdated
	EventMapPointSoftDeleted   = domainevents.EventMapPointSoftDeleted
	EventRouteCandidateAdded   = domainevents.EventRouteCandidateAdded
	EventRouteCandidateUpdated = domainevents.EventRouteCandidateUpdated
	EventRouteSelected         = domainevents.EventRouteSelected
	EventRouteDimmed           = domainevents.EventRouteDimmed
	EventMapAnnotationAdded    = domainevents.EventMapAnnotationAdded
	EventMapAnnotationUpdated  = domainevents.EventMapAnnotationUpdated
	EventMapAnnotationDimmed   = domainevents.EventMapAnnotationDimmed
	EventMapBatch              = domainevents.EventMapBatch
	EventPlanningCompleted     = domainevents.EventPlanningCompleted
	EventPlanningError         = domainevents.EventPlanningError
)

type PublicPlanningEvent = domainevents.PublicPlanningEvent
type PublicModelUsage = domainevents.PublicModelUsage
type PublicMapPoint = domainevents.PublicMapPoint
type PublicMapPopup = domainevents.PublicMapPopup
type PublicMapViewport = domainevents.PublicMapViewport
type PublicMapAnnotation = domainevents.PublicMapAnnotation
type PublicMapAnnotationAnchor = domainevents.PublicMapAnnotationAnchor
type PublicRouteCandidate = domainevents.PublicRouteCandidate
type PublicPlanningEventRecorder = domainevents.PublicPlanningEventRecorder

type TraceEmitter struct {
	runID    string
	mu       sync.Mutex
	seq      int64
	out      chan PublicPlanningEvent
	done     bool
	recorder PublicPlanningEventRecorder
}

func ContextWithTraceEmitter(ctx context.Context, emitter *TraceEmitter) context.Context {
	ctx = context.WithValue(ctx, traceEmitterContextKey{}, emitter)
	return contextWithModelUsageEmitter(ctx, emitter)
}

func RuntimeStateWithTraceEmitter(emitter *TraceEmitter, values map[string]any) map[string]any {
	out := make(map[string]any, len(values)+1)
	for key, value := range values {
		out[key] = value
	}
	out[runtimeTraceEmitterKey] = emitter
	return out
}
