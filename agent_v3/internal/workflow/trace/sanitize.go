package trace

import domainevents "agent_v3/internal/domain/events"

func SanitizePublicPlanningEvent(ev PublicPlanningEvent) PublicPlanningEvent {
	return domainevents.SanitizePublicPlanningEvent(ev)
}

func publicLevelForStage(stage string) string {
	return domainevents.PublicLevelForStage(stage)
}

func defaultPublicAnnotationTitle(value, fallback string) string {
	return domainevents.DefaultPublicAnnotationTitle(value, fallback)
}
