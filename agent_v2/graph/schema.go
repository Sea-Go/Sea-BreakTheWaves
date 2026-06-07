package graph

import (
	"context"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// EnsureSchema creates uniqueness constraints and indexes if they don't exist.
// Safe to call multiple times — uses IF NOT EXISTS semantics via SHOW CONSTRAINTS.
func (c *Client) EnsureSchema(ctx context.Context) error {
	constraints := []struct {
		name   string
		cypher string
	}{
		{"trip_plan_id", "CREATE CONSTRAINT trip_plan_id IF NOT EXISTS FOR (n:TripPlan) REQUIRE n.id IS UNIQUE"},
		{"phase_id", "CREATE CONSTRAINT phase_id IF NOT EXISTS FOR (n:Phase) REQUIRE n.id IS UNIQUE"},
		{"month_id", "CREATE CONSTRAINT month_id IF NOT EXISTS FOR (n:Month) REQUIRE n.id IS UNIQUE"},
		{"week_id", "CREATE CONSTRAINT week_id IF NOT EXISTS FOR (n:Week) REQUIRE n.id IS UNIQUE"},
		{"day_id", "CREATE CONSTRAINT day_id IF NOT EXISTS FOR (n:Day) REQUIRE n.id IS UNIQUE"},
		{"poi_id", "CREATE CONSTRAINT poi_id IF NOT EXISTS FOR (n:POI) REQUIRE n.id IS UNIQUE"},
		{"review_result_id", "CREATE CONSTRAINT review_result_id IF NOT EXISTS FOR (n:ReviewResult) REQUIRE n.id IS UNIQUE"},
		{"exploration_run_id", "CREATE CONSTRAINT exploration_run_id IF NOT EXISTS FOR (n:ExplorationRun) REQUIRE n.id IS UNIQUE"},
		{"exploration_step_id", "CREATE CONSTRAINT exploration_step_id IF NOT EXISTS FOR (n:ExplorationStep) REQUIRE n.id IS UNIQUE"},
		{"map_anchor_id", "CREATE CONSTRAINT map_anchor_id IF NOT EXISTS FOR (n:MapAnchor) REQUIRE n.id IS UNIQUE"},
		{"route_candidate_id", "CREATE CONSTRAINT route_candidate_id IF NOT EXISTS FOR (n:RouteCandidate) REQUIRE n.id IS UNIQUE"},
	}

	indexes := []struct {
		name   string
		cypher string
	}{
		{"trip_plan_user_id", "CREATE INDEX trip_plan_user_id IF NOT EXISTS FOR (n:TripPlan) ON (n.userId)"},
		{"trip_plan_session_id", "CREATE INDEX trip_plan_session_id IF NOT EXISTS FOR (n:TripPlan) ON (n.sessionId)"},
		{"exploration_run_user_updated", "CREATE INDEX exploration_run_user_updated IF NOT EXISTS FOR (n:ExplorationRun) ON (n.userId, n.updatedAt)"},
		{"exploration_step_run_seq", "CREATE INDEX exploration_step_run_seq IF NOT EXISTS FOR (n:ExplorationStep) ON (n.runId, n.seq)"},
		{"map_anchor_visibility", "CREATE INDEX map_anchor_visibility IF NOT EXISTS FOR (n:MapAnchor) ON (n.visibilityStatus)"},
		{"route_candidate_visibility", "CREATE INDEX route_candidate_visibility IF NOT EXISTS FOR (n:RouteCandidate) ON (n.visibilityStatus)"},
	}

	// Check existing constraints to avoid errors on re-run
	existing, err := c.listConstraints(ctx)
	if err != nil {
		return fmt.Errorf("graph: list constraints: %w", err)
	}

	for _, ct := range constraints {
		if existing[ct.name] {
			continue
		}
		if _, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			_, err := tx.Run(ctx, ct.cypher, nil)
			return nil, err
		}); err != nil {
			return fmt.Errorf("graph: create constraint %s: %w", ct.name, err)
		}
	}

	for _, ix := range indexes {
		if existing[ix.name] {
			continue
		}
		if _, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			_, err := tx.Run(ctx, ix.cypher, nil)
			return nil, err
		}); err != nil {
			return fmt.Errorf("graph: create index %s: %w", ix.name, err)
		}
	}

	return nil
}

func (c *Client) listConstraints(ctx context.Context) (map[string]bool, error) {
	result, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, "SHOW CONSTRAINTS", nil)
		if err != nil {
			return nil, err
		}
		var names []string
		for rec.Next(ctx) {
			if name, ok := rec.Record().Get("name"); ok {
				names = append(names, fmt.Sprint(name))
			}
		}
		return names, rec.Err()
	})
	if err != nil {
		return nil, err
	}
	names, _ := result.([]string)
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set, nil
}
