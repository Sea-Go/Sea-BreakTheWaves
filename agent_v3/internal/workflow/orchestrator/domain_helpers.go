package orchestrator

import (
	domaintravel "agent_v3/internal/domain/travel"
	workflowruntime "agent_v3/internal/workflow/runtime"
)

func enrichRequirementWithDeterministicFields(snap *workflowruntime.TravelRequirementSnapshot, userMessage string) {
	domaintravel.EnrichRequirementWithDeterministicFields(snap, userMessage)
}

func latestUserTurnText(userMessage string) string {
	return domaintravel.LatestUserTurnText(userMessage)
}

func isLikelyNewPlanningRequest(userMessage string) bool {
	return domaintravel.IsLikelyNewPlanningRequest(userMessage)
}

func requiresHighAltitudeCheck(snap workflowruntime.TravelRequirementSnapshot) bool {
	return domaintravel.RequiresHighAltitudeCheck(snap)
}

func requiresDrivingIntensityCheck(snap workflowruntime.TravelRequirementSnapshot) bool {
	return domaintravel.RequiresDrivingIntensityCheck(snap)
}
