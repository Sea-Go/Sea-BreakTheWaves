package graph

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// --- Typed write operations ---

// CreatePhaseInput carries all fields for creating a Phase node.
type CreatePhaseInput struct {
	ID              string
	Name            string
	Seq             int
	StartDate       string
	EndDate         string
	StartCity       string
	EndCity         string
	Regions         []string
	Season          string
	Theme           string
	ClimateSummary  string
	MemorySummary   string
	EstimatedBudget float64
}

// CreateMonthInput carries all fields for creating a Month node.
type CreateMonthInput struct {
	ID            string
	Name          string
	YearMonth     string
	Seq           int
	Region        string
	PrimaryCity   string
	WeekCount     int
	MonthlyBudget float64
	MemorySummary string
}

// CreateWeekInput carries all fields for creating a Week node.
type CreateWeekInput struct {
	ID                    string
	Name                  string
	Seq                   int
	StartDate             string
	EndDate               string
	Region                string
	PrimaryLocation       string
	Theme                 string
	RestDayCount          int
	TransferDayCount      int
	HighIntensityDayCount int
	MemorySummary         string
}

// CreateDayInput carries all fields for creating a Day node.
type CreateDayInput struct {
	ID            string
	Date          string
	DayIndex      int
	Theme         string
	StartPoint    string
	PrimaryArea   string
	RouteOverview string
	Intensity     string
	ThinkingNotes string
	MemorySummary string
}

const cypherCreatePhases = `
MATCH (tp:TripPlan {id: $tripPlanID})
SET tp.status = 'decomposed'
WITH tp
UNWIND $children AS child
CREATE (tp)-[:HAS_PHASE]->(p:Phase {
    id: child.id, name: child.name, seq: child.seq,
    startDate: child.startDate, endDate: child.endDate,
    region: child.region, season: child.season, theme: child.theme,
    climateSummary: child.climateSummary, estimatedBudget: child.estimatedBudget,
    startCity: child.startCity, endCity: child.endCity,
    regions: child.regions, memorySummary: child.memorySummary,
    status: 'outlined'
})
WITH collect(p) AS nodes
UNWIND range(0, size(nodes)-2) AS i
WITH nodes[i] AS a, nodes[i+1] AS b
CREATE (a)-[:NEXT_PHASE]->(b)
RETURN [n IN nodes | n.id] AS ids
`

const cypherCreateMonths = `
MATCH (phase:Phase {id: $phaseID})
SET phase.status = 'decomposed'
WITH phase
UNWIND $children AS child
CREATE (phase)-[:HAS_MONTH]->(m:Month {
    id: child.id, name: child.name, yearMonth: child.yearMonth,
    seq: child.seq, region: child.region, primaryCity: child.primaryCity,
    weekCount: child.weekCount, monthlyBudget: child.monthlyBudget,
    memorySummary: child.memorySummary, status: 'outlined'
})
WITH collect(m) AS nodes
UNWIND range(0, size(nodes)-2) AS i
WITH nodes[i] AS a, nodes[i+1] AS b
CREATE (a)-[:NEXT_MONTH]->(b)
RETURN [n IN nodes | n.id] AS ids
`

const cypherCreateWeeks = `
MATCH (month:Month {id: $monthID})
SET month.status = 'decomposed'
WITH month
UNWIND $children AS child
CREATE (month)-[:HAS_WEEK]->(w:Week {
    id: child.id, name: child.name, seq: child.seq,
    startDate: child.startDate, endDate: child.endDate,
    region: child.region, primaryLocation: child.primaryLocation,
    theme: child.theme, restDayCount: child.restDayCount,
    transferDayCount: child.transferDayCount,
    highIntensityDayCount: child.highIntensityDayCount,
    memorySummary: child.memorySummary, status: 'outlined'
})
WITH collect(w) AS nodes
UNWIND range(0, size(nodes)-2) AS i
WITH nodes[i] AS a, nodes[i+1] AS b
CREATE (a)-[:NEXT_WEEK]->(b)
RETURN [n IN nodes | n.id] AS ids
`

const cypherCreateDays = `
MATCH (week:Week {id: $weekID})
SET week.status = 'decomposed'
WITH week
UNWIND $children AS child
CREATE (week)-[:HAS_DAY]->(d:Day {
    id: child.id, date: child.date, dayIndex: child.dayIndex,
    theme: child.theme, startPoint: child.startPoint,
    primaryArea: child.primaryArea, routeOverview: child.routeOverview,
    intensity: child.intensity, thinkingNotes: child.thinkingNotes,
    memorySummary: child.memorySummary, status: 'outlined'
})
WITH collect(d) AS nodes
UNWIND range(0, size(nodes)-2) AS i
WITH nodes[i] AS a, nodes[i+1] AS b
CREATE (a)-[:NEXT_DAY]->(b)
RETURN [n IN nodes | n.id] AS ids
`

// CreatePhases creates Phase nodes under a TripPlan, with NEXT_PHASE chain.
func (c *Client) CreatePhases(ctx context.Context, tripPlanID string, phases []CreatePhaseInput) ([]string, error) {
	children := make([]map[string]any, len(phases))
	for i, p := range phases {
		if p.ID == "" {
			p.ID = uuid.NewString()
		}
		region := p.StartCity
		if len(p.Regions) > 0 {
			region = strings.Join(p.Regions, ", ")
		}
		children[i] = map[string]any{
			"id": p.ID, "name": p.Name, "seq": p.Seq,
			"startDate": p.StartDate, "endDate": p.EndDate,
			"region": region, "season": p.Season, "theme": p.Theme,
			"climateSummary": p.ClimateSummary, "estimatedBudget": p.EstimatedBudget,
			"startCity": p.StartCity, "endCity": p.EndCity,
			"regions": p.Regions, "memorySummary": p.MemorySummary,
		}
	}
	return c.createTypedChildren(ctx, cypherCreatePhases, tripPlanID, children, "Phase")
}

// CreateMonths creates Month nodes under a Phase, with NEXT_MONTH chain.
func (c *Client) CreateMonths(ctx context.Context, phaseID string, months []CreateMonthInput) ([]string, error) {
	children := make([]map[string]any, len(months))
	for i, m := range months {
		if m.ID == "" {
			m.ID = uuid.NewString()
		}
		children[i] = map[string]any{
			"id": m.ID, "name": m.Name, "yearMonth": m.YearMonth,
			"seq": m.Seq, "region": m.Region, "primaryCity": m.PrimaryCity,
			"weekCount": m.WeekCount, "monthlyBudget": m.MonthlyBudget,
			"memorySummary": m.MemorySummary,
		}
	}
	return c.createTypedChildren(ctx, cypherCreateMonths, phaseID, children, "Month")
}

// CreateWeeks creates Week nodes under a Month, with NEXT_WEEK chain.
func (c *Client) CreateWeeks(ctx context.Context, monthID string, weeks []CreateWeekInput) ([]string, error) {
	children := make([]map[string]any, len(weeks))
	for i, w := range weeks {
		if w.ID == "" {
			w.ID = uuid.NewString()
		}
		children[i] = map[string]any{
			"id": w.ID, "name": w.Name, "seq": w.Seq,
			"startDate": w.StartDate, "endDate": w.EndDate,
			"region": w.Region, "primaryLocation": w.PrimaryLocation,
			"theme": w.Theme, "restDayCount": w.RestDayCount,
			"transferDayCount":      w.TransferDayCount,
			"highIntensityDayCount": w.HighIntensityDayCount,
			"memorySummary":         w.MemorySummary,
		}
	}
	return c.createTypedChildren(ctx, cypherCreateWeeks, monthID, children, "Week")
}

// CreateDays creates Day nodes under a Week, with NEXT_DAY chain.
func (c *Client) CreateDays(ctx context.Context, weekID string, days []CreateDayInput) ([]string, error) {
	children := make([]map[string]any, len(days))
	for i, d := range days {
		if d.ID == "" {
			d.ID = uuid.NewString()
		}
		children[i] = map[string]any{
			"id": d.ID, "date": d.Date, "dayIndex": d.DayIndex,
			"theme": d.Theme, "startPoint": d.StartPoint,
			"primaryArea": d.PrimaryArea, "routeOverview": d.RouteOverview,
			"intensity": d.Intensity, "thinkingNotes": d.ThinkingNotes,
			"memorySummary": d.MemorySummary,
		}
	}
	return c.createTypedChildren(ctx, cypherCreateDays, weekID, children, "Day")
}

func (c *Client) createTypedChildren(ctx context.Context, cypher, parentID string, children []map[string]any, childType string) ([]string, error) {
	result, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, cypher, map[string]any{
			"tripPlanID": parentID,
			"phaseID":    parentID,
			"monthID":    parentID,
			"weekID":     parentID,
			"children":   children,
		})
		if err != nil {
			return nil, err
		}
		if rec.Next(ctx) {
			ids, _ := rec.Record().Get("ids")
			return ids, nil
		}
		return nil, rec.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("graph: create typed children %s: %w", childType, err)
	}
	idList, _ := result.([]any)
	ids := make([]string, len(idList))
	for i, v := range idList {
		ids[i] = fmt.Sprint(v)
	}
	return ids, nil
}
