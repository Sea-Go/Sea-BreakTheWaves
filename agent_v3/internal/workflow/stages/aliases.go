package stages

import (
	"context"

	"agent_v3/internal/agents/modelrouter"
	domaingeo "agent_v3/internal/domain/geo"
	domaintravel "agent_v3/internal/domain/travel"
	"agent_v3/internal/graph"
	"agent_v3/internal/review"
	workflowruntime "agent_v3/internal/workflow/runtime"
	workflowtrace "agent_v3/internal/workflow/trace"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type TraceEmitter = workflowtrace.TraceEmitter
type PublicPlanningEvent = workflowtrace.PublicPlanningEvent
type PublicMapAnnotation = workflowtrace.PublicMapAnnotation
type PublicMapAnnotationAnchor = workflowtrace.PublicMapAnnotationAnchor
type PublicMapPopup = workflowtrace.PublicMapPopup
type PublicMapPoint = workflowtrace.PublicMapPoint
type PublicMapViewport = workflowtrace.PublicMapViewport
type PublicRouteCandidate = workflowtrace.PublicRouteCandidate
type routeDisplayContext = workflowtrace.RouteDisplayContext

const (
	StageRequirementIntake = workflowruntime.StageRequirementIntake
	StageAwaitingUserInfo  = workflowruntime.StageAwaitingUserInfo
	StageRequirementMerge  = workflowruntime.StageRequirementMerge
	StageMacroPlanning     = workflowruntime.StageMacroPlanning
	StageGraphSplitting    = workflowruntime.StageGraphSplitting
	StageDayExpansion      = workflowruntime.StageDayExpansion
	StageReview            = workflowruntime.StageReview
	StageFinalOutput       = workflowruntime.StageFinalOutput

	EventChatMessageDelta      = workflowtrace.EventChatMessageDelta
	EventPlanningStageChanged  = workflowtrace.EventPlanningStageChanged
	EventMapScopeChanged       = workflowtrace.EventMapScopeChanged
	EventMapPointAdded         = workflowtrace.EventMapPointAdded
	EventMapPointUpdated       = workflowtrace.EventMapPointUpdated
	EventMapPointSoftDeleted   = workflowtrace.EventMapPointSoftDeleted
	EventRouteCandidateAdded   = workflowtrace.EventRouteCandidateAdded
	EventRouteCandidateUpdated = workflowtrace.EventRouteCandidateUpdated
	EventRouteSelected         = workflowtrace.EventRouteSelected
	EventRouteDimmed           = workflowtrace.EventRouteDimmed
	EventMapAnnotationAdded    = workflowtrace.EventMapAnnotationAdded
	EventMapAnnotationUpdated  = workflowtrace.EventMapAnnotationUpdated
	EventMapAnnotationDimmed   = workflowtrace.EventMapAnnotationDimmed
	EventMapBatch              = workflowtrace.EventMapBatch
	EventPlanningCompleted     = workflowtrace.EventPlanningCompleted
	EventPlanningError         = workflowtrace.EventPlanningError

	maxGuideAnnotationSummary = workflowtrace.MaxGuideAnnotationSummary
)

type ModelLevel = modelrouter.ModelLevel

const (
	ModelLevelHigh   = modelrouter.ModelLevelHigh
	ModelLevelMedium = modelrouter.ModelLevelMedium
	ModelLevelLow    = modelrouter.ModelLevelLow
)

func newModelForLevel(agentName string, level ModelLevel) model.Model {
	return modelrouter.NewModelForLevel(agentName, level)
}

func contextWithModelUsageEmitter(ctx context.Context, emitter modelrouter.UsageEmitter) context.Context {
	return modelrouter.ContextWithUsageEmitter(ctx, emitter)
}

func enrichRequirementWithDeterministicFields(snap *domaintravel.TravelRequirementSnapshot, userMessage string) {
	domaintravel.EnrichRequirementWithDeterministicFields(snap, userMessage)
}

func anchorSearchTermsFromText(text string) []domaintravel.DestinationAnchorSnapshot {
	return domaintravel.AnchorSearchTermsFromText(text)
}

func dedupeDestinationAnchors(values []domaintravel.DestinationAnchorSnapshot) []domaintravel.DestinationAnchorSnapshot {
	return domaintravel.DedupeDestinationAnchors(values)
}

func containsAny(text string, needles []string) bool {
	return domaintravel.ContainsAny(text, needles)
}

const (
	anchorOriginUserExplicit   = domaintravel.OriginUserExplicit
	anchorOriginSystemInferred = domaintravel.OriginSystemInferred
)

func buildTravelGeoConstraint(req domaintravel.TravelRequirementSnapshot, extraText string) domaingeo.TravelGeoConstraint {
	return domaingeo.BuildTravelGeoConstraint(req, extraText)
}

func buildTravelGeoConstraintFromOverview(overview *graph.TripOverview) domaingeo.TravelGeoConstraint {
	return domaingeo.BuildTravelGeoConstraintFromOverview(overview)
}

func traceEmitterFromInvocation(inv *agentcore.Invocation) *TraceEmitter {
	return workflowtrace.TraceEmitterFromInvocation(inv)
}

func emitRequirementMapEvents(ctx context.Context, emitter *TraceEmitter, req domaintravel.TravelRequirementSnapshot) {
	workflowtrace.EmitRequirementMapEvents(ctx, emitter, req)
}

func emitPhaseOverviewMapEvents(ctx context.Context, emitter *TraceEmitter, overview *graph.TripOverview) {
	workflowtrace.EmitPhaseOverviewMapEvents(ctx, emitter, overview)
}

func emitGraphSplittingMapEvents(ctx context.Context, emitter *TraceEmitter, overview *graph.TripOverview) {
	workflowtrace.EmitGraphSplittingMapEvents(ctx, emitter, overview)
}

func emitMacroRouteMapEvents(ctx context.Context, emitter *TraceEmitter, req domaintravel.TravelRequirementSnapshot, overview *graph.TripOverview) {
	workflowtrace.EmitMacroRouteMapEvents(ctx, emitter, req, overview)
}

func emitRouteSegmentMapEvents(ctx context.Context, emitter *TraceEmitter, level, parentNodeID string, pois []graph.POIInput, routes []graph.RouteInput, contexts ...workflowtrace.RouteDisplayContext) {
	workflowtrace.EmitRouteSegmentMapEvents(ctx, emitter, level, parentNodeID, pois, routes, contexts...)
}

func emitExactPOIMapBatch(ctx context.Context, emitter *TraceEmitter, level, parentNodeID string, pois []graph.POIInput, contexts ...workflowtrace.RouteDisplayContext) {
	workflowtrace.EmitExactPOIMapBatch(ctx, emitter, level, parentNodeID, pois, contexts...)
}

func emitReviewAnnotation(ctx context.Context, emitter *TraceEmitter, level, nodeID, label, agentName, anchorType string, review graph.ReviewInput) {
	workflowtrace.EmitReviewAnnotation(ctx, emitter, level, nodeID, label, agentName, anchorType, review)
}

func emitGuideEvidenceForTrip(ctx context.Context, emitter *TraceEmitter, graphClient *graph.Client, tripPlanID string, overview *graph.TripOverview) {
	workflowtrace.EmitGuideEvidenceForTrip(ctx, emitter, graphClient, tripPlanID, overview)
}

func filterPOIsByGeoConstraint(ctx context.Context, emitter *TraceEmitter, dayID string, pois []graph.POIInput, constraint domaingeo.TravelGeoConstraint) []graph.POIInput {
	return review.FilterPOIsByGeoConstraint(ctx, emitter, dayID, pois, constraint)
}

func emitGeoScopeViolationAnnotations(ctx context.Context, emitter *TraceEmitter, level, nodeID string, violations []domaingeo.TravelGeoViolation) {
	review.EmitGeoScopeViolationAnnotations(ctx, emitter, level, nodeID, violations)
}

func stablePlanningAnnotationID(parts ...string) string {
	return workflowtrace.StablePlanningAnnotationID(parts...)
}

func truncateGuideText(value string, limit int) string {
	return workflowtrace.TruncateGuideText(value, limit)
}
