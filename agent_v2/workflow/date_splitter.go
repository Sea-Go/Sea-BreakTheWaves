package workflow

import (
	"fmt"
	"time"

	"agent_v2/graph"
)

// PhaseDef describes one phase of the trip.
type PhaseDef struct {
	Seq        int
	Name       string
	StartDate  string
	EndDate    string
	StartCity  string
	EndCity    string
	Regions    []string
	Season     string
	Theme      string
}

// SplitTripToPhases creates PhaseInputs from phase definitions.
func SplitTripToPhases(tripStart, tripEnd string, phaseDefs []PhaseDef) []graph.CreatePhaseInput {
	phases := make([]graph.CreatePhaseInput, len(phaseDefs))
	for i, pd := range phaseDefs {
		phases[i] = graph.CreatePhaseInput{
			Name:            pd.Name,
			Seq:             pd.Seq,
			StartDate:       pd.StartDate,
			EndDate:         pd.EndDate,
			StartCity:       pd.StartCity,
			EndCity:         pd.EndCity,
			Regions:         pd.Regions,
			Season:          pd.Season,
			Theme:           pd.Theme,
			EstimatedBudget: 0,
		}
	}
	return phases
}

// SplitPhaseToMonths creates Month inputs based on calendar months within a Phase's date range.
func SplitPhaseToMonths(phaseID string, phaseStart, phaseEnd string) []graph.CreateMonthInput {
	start, _ := time.Parse("2006-01-02", phaseStart)
	end, _ := time.Parse("2006-01-02", phaseEnd)

	var months []graph.CreateMonthInput
	seq := 1
	current := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)

	for !current.After(end) {
		monthStart := current
		if monthStart.Before(start) {
			monthStart = start
		}
		monthEnd := time.Date(current.Year(), current.Month()+1, 1, 0, 0, 0, 0, time.UTC).Add(-24 * time.Hour)
		if monthEnd.After(end) {
			monthEnd = end
		}

		daysInMonth := int(monthEnd.Sub(monthStart).Hours()/24) + 1
		weekCount := daysInMonth / 7
		if daysInMonth%7 > 0 {
			weekCount++
		}

		months = append(months, graph.CreateMonthInput{
			Name:      fmt.Sprintf("%d月", int(current.Month())),
			YearMonth: current.Format("2006-01"),
			Seq:       seq,
			WeekCount: weekCount,
		})
		seq++
		current = current.AddDate(0, 1, 0)
	}

	return months
}

// SplitMonthToWeeks creates Week inputs based on natural weeks within a Month's date range.
func SplitMonthToWeeks(monthStart, monthEnd string) []graph.CreateWeekInput {
	start, _ := time.Parse("2006-01-02", monthStart)
	end, _ := time.Parse("2006-01-02", monthEnd)

	var weeks []graph.CreateWeekInput
	seq := 1
	current := start

	for !current.After(end) {
		weekEnd := current.AddDate(0, 0, 6)
		if weekEnd.After(end) {
			weekEnd = end
		}

		weeks = append(weeks, graph.CreateWeekInput{
			Name:      fmt.Sprintf("第%d周", seq),
			Seq:       seq,
			StartDate: current.Format("2006-01-02"),
			EndDate:   weekEnd.Format("2006-01-02"),
		})
		seq++
		current = weekEnd.AddDate(0, 0, 1)
	}

	return weeks
}

// SplitWeekToDays creates Day inputs for each day in a Week.
func SplitWeekToDays(weekStart, weekEnd string, baseDayIndex int) []graph.CreateDayInput {
	start, _ := time.Parse("2006-01-02", weekStart)
	end, _ := time.Parse("2006-01-02", weekEnd)

	var days []graph.CreateDayInput
	dayIndex := baseDayIndex
	current := start

	for !current.After(end) {
		days = append(days, graph.CreateDayInput{
			Date:     current.Format("2006-01-02"),
			DayIndex: dayIndex,
			Intensity: "medium",
		})
		dayIndex++
		current = current.AddDate(0, 0, 1)
	}

	return days
}

// SplitAllDays deterministically creates all Day nodes from Phase definitions.
// Returns the total count of generated days.
func SplitAllDays(phases []PhaseDef) int {
	totalDays := 0
	for _, pd := range phases {
		months := SplitPhaseToMonths("", pd.StartDate, pd.EndDate)
		for _, m := range months {
			monthStart, _ := time.Parse("2006-01-02", pd.StartDate)
			monthEnd, _ := time.Parse("2006-01-02", pd.EndDate)
			daysInMonth := int(monthEnd.Sub(monthStart).Hours()/24) + 1
			if daysInMonth < 1 {
				daysInMonth = 1
			}
			_ = m
			weeks := SplitMonthToWeeks(pd.StartDate, pd.EndDate)
			for _, w := range weeks {
				days := SplitWeekToDays(w.StartDate, w.EndDate, totalDays+1)
				totalDays += len(days)
			}
		}
	}
	return totalDays
}