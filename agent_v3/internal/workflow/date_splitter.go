package workflow

import (
	domainplanning "agent_v3/internal/domain/planning"
	"agent_v3/internal/graph"
)

type PhaseDef = domainplanning.PhaseDef

func SplitTripToPhases(tripStart, tripEnd string, phaseDefs []PhaseDef) []graph.CreatePhaseInput {
	return domainplanning.SplitTripToPhases(tripStart, tripEnd, phaseDefs)
}

func SplitPhaseToMonths(phaseID string, phaseStart, phaseEnd string) []graph.CreateMonthInput {
	return domainplanning.SplitPhaseToMonths(phaseID, phaseStart, phaseEnd)
}

func SplitMonthToWeeks(monthStart, monthEnd string) []graph.CreateWeekInput {
	return domainplanning.SplitMonthToWeeks(monthStart, monthEnd)
}

func SplitWeekToDays(weekStart, weekEnd string, baseDayIndex int) []graph.CreateDayInput {
	return domainplanning.SplitWeekToDays(weekStart, weekEnd, baseDayIndex)
}

func SplitAllDays(phases []PhaseDef) int {
	return domainplanning.SplitAllDays(phases)
}
