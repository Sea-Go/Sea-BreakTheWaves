package trace

import (
	"context"

	domaintravel "agent_v3/internal/domain/travel"

	"agent_v3/internal/agents/modelrouter"
)

type TravelRequirementSnapshot = domaintravel.TravelRequirementSnapshot
type DestinationAnchorSnapshot = domaintravel.DestinationAnchorSnapshot

const (
	MaxGuideAnnotationSummary  = maxGuideAnnotationSummary
	MaxGuideAnnotationEvidence = maxGuideAnnotationEvidence
)

func contextWithModelUsageEmitter(ctx context.Context, emitter modelrouter.UsageEmitter) context.Context {
	return modelrouter.ContextWithUsageEmitter(ctx, emitter)
}
