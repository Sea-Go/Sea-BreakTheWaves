package workflow

import (
	"context"
	"fmt"
	"log"
	"time"

	"agent_v2/graph"
)

// Stage represents a discrete state in the planning workflow.
type Stage string

const (
	StageInit           Stage = "init"
	StageTripCreated    Stage = "trip_created"
	StagePhaseOutlined  Stage = "phase_outlined"
	StagePhaseApproved  Stage = "phase_approved"
	StageMonthOutlined  Stage = "month_outlined"
	StageMonthApproved  Stage = "month_approved"
	StageWeekOutlined   Stage = "week_outlined"
	StageWeekApproved   Stage = "week_approved"
	StageDayOutlined    Stage = "day_outlined"
	StageDayReviewed    Stage = "day_reviewed"
	StageOutputReady    Stage = "output_ready"
	StageDone           Stage = "done"
	StageFailed         Stage = "failed"
)

// State holds the current position in the workflow state machine.
type State struct {
	Stage      Stage
	TripPlanID string
	RetryCount int
	MaxRetries int
}

// Orchestrator drives the planning workflow using a Go state machine.
// LLM agents are called only for content generation and review — never for flow control.
type Orchestrator struct {
	graph *graph.Client
}

// NewOrchestrator creates a new workflow orchestrator.
func NewOrchestrator(gc *graph.Client) *Orchestrator {
	return &Orchestrator{graph: gc}
}

// TripPlanRequest is the input for starting a planning workflow.
type TripPlanRequest struct {
	UserID       string
	SessionID    string
	RequestID    string
	UserMessage  string
	StartDate    string
	EndDate      string
	TotalDays    int
	TravelStyle  string
	RawReq       string
	PhaseDefs    []PhaseDef // user-defined phases, or nil to let LLM suggest
}

// PlanningResult holds the final output of the workflow.
type PlanningResult struct {
	TripPlanID string
	DayCount   int
	Status     Stage
}

// Run executes the full planning workflow.
func (o *Orchestrator) Run(ctx context.Context, req TripPlanRequest) (*PlanningResult, error) {
	state := &State{
		Stage:      StageInit,
		MaxRetries: 3,
	}

	// 1. Create TripPlan (Go, deterministic)
	trip := graph.TripPlanNode{
		Name:          req.UserMessage,
		StartDate:     req.StartDate,
		EndDate:       req.EndDate,
		TotalDays:     req.TotalDays,
		TravelStyle:   req.TravelStyle,
		RawRequirements: req.RawReq,
		UserID:        req.UserID,
		SessionID:     req.SessionID,
		RequestID:     req.RequestID,
	}
	tripPlanID, err := o.graph.CreateTripPlan(ctx, trip)
	if err != nil {
		return nil, fmt.Errorf("create trip plan: %w", err)
	}
	state.TripPlanID = tripPlanID
	state.Stage = StageTripCreated
	log.Printf("[workflow] TripPlan created: %s", tripPlanID)

	// 2. Create Phases (Go, deterministic from user input or LLM suggestion)
	if len(req.PhaseDefs) == 0 {
		return nil, fmt.Errorf("phase definitions required — LLM suggestion not yet integrated")
	}
	phaseInputs := SplitTripToPhases(req.StartDate, req.EndDate, req.PhaseDefs)
	phaseIDs, err := o.graph.CreatePhases(ctx, tripPlanID, phaseInputs)
	if err != nil {
		return nil, fmt.Errorf("create phases: %w", err)
	}
	state.Stage = StagePhaseOutlined
	log.Printf("[workflow] %d Phases created", len(phaseIDs))

	// 3. For each Phase, split to Months, then Weeks, then Days
	totalDays := 0
	for i, phaseID := range phaseIDs {
		pd := req.PhaseDefs[i]

		months := SplitPhaseToMonths(phaseID, pd.StartDate, pd.EndDate)
		monthIDs, err := o.graph.CreateMonths(ctx, phaseID, months)
		if err != nil {
			return nil, fmt.Errorf("create months for phase %s: %w", phaseID, err)
		}
		log.Printf("[workflow] Phase %d: %d Months created", pd.Seq, len(monthIDs))

		for j, monthID := range monthIDs {
			m := months[j]
			monthStart, _ := parseDate(m.YearMonth + "-01")
			monthEnd := monthStart.AddDate(0, 1, -1)

			weeks := SplitMonthToWeeks(
				monthStart.Format("2006-01-02"),
				monthEnd.Format("2006-01-02"),
			)
			weekIDs, err := o.graph.CreateWeeks(ctx, monthID, weeks)
			if err != nil {
				return nil, fmt.Errorf("create weeks for month %s: %w", monthID, err)
			}
			log.Printf("[workflow] Month %s: %d Weeks created", m.YearMonth, len(weekIDs))

			for k, weekID := range weekIDs {
				w := weeks[k]
				days := SplitWeekToDays(w.StartDate, w.EndDate, totalDays+1)
				dayIDs, err := o.graph.CreateDays(ctx, weekID, days)
				if err != nil {
					return nil, fmt.Errorf("create days for week %s: %w", weekID, err)
				}
				totalDays += len(dayIDs)
			}
		}
	}
	state.Stage = StageDayOutlined
	log.Printf("[workflow] Total %d Days created across %d Phases", totalDays, len(phaseIDs))

	return &PlanningResult{
		TripPlanID: tripPlanID,
		DayCount:   totalDays,
		Status:     state.Stage,
	}, nil
}

func parseDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}