package trace

import (
	"context"

	"agent_v3/internal/graph"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
)

func TraceEmitterFromInvocation(inv *agentcore.Invocation) *TraceEmitter {
	return traceEmitterFromInvocation(inv)
}

func StablePlanningAnnotationID(parts ...string) string {
	return stablePlanningAnnotationID(parts...)
}

func TruncateGuideText(value string, limit int) string {
	return truncateGuideText(value, limit)
}

func PublicReviewAgentLabel(name string) string {
	return publicReviewAgentLabel(name)
}

func EmitRequirementMapEvents(ctx context.Context, emitter *TraceEmitter, req TravelRequirementSnapshot) {
	emitRequirementMapEvents(ctx, emitter, req)
}

func EmitPhaseOverviewMapEvents(ctx context.Context, emitter *TraceEmitter, overview *graph.TripOverview) {
	emitPhaseOverviewMapEvents(ctx, emitter, overview)
}

func EmitGraphSplittingMapEvents(ctx context.Context, emitter *TraceEmitter, overview *graph.TripOverview) {
	emitGraphSplittingMapEvents(ctx, emitter, overview)
}

func EmitMacroRouteMapEvents(ctx context.Context, emitter *TraceEmitter, req TravelRequirementSnapshot, overview *graph.TripOverview) {
	emitMacroRouteMapEvents(ctx, emitter, req, overview)
}

func EmitRouteSegmentMapEvents(ctx context.Context, emitter *TraceEmitter, level, parentNodeID string, pois []graph.POIInput, routes []graph.RouteInput, contexts ...RouteDisplayContext) {
	internal := make([]routeDisplayContext, 0, len(contexts))
	for _, item := range contexts {
		internal = append(internal, routeDisplayContext(item))
	}
	emitRouteSegmentMapEvents(ctx, emitter, level, parentNodeID, pois, routes, internal...)
}

func EmitExactPOIMapBatch(ctx context.Context, emitter *TraceEmitter, level, parentNodeID string, pois []graph.POIInput, contexts ...RouteDisplayContext) {
	internal := make([]routeDisplayContext, 0, len(contexts))
	for _, item := range contexts {
		internal = append(internal, routeDisplayContext(item))
	}
	emitExactPOIMapBatch(ctx, emitter, level, parentNodeID, pois, internal...)
}

func EmitReviewAnnotation(ctx context.Context, emitter *TraceEmitter, level, nodeID, label, agentName, anchorType string, review graph.ReviewInput) {
	emitReviewAnnotation(ctx, emitter, level, nodeID, label, agentName, anchorType, review)
}

func EmitGuideEvidenceForTrip(ctx context.Context, emitter *TraceEmitter, graphClient *graph.Client, tripPlanID string, overview *graph.TripOverview) {
	emitGuideEvidenceForTrip(ctx, emitter, graphClient, tripPlanID, overview)
}
