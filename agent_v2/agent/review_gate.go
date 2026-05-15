package agent

import (
	"context"
	"fmt"

	"agent_v2/graph"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

// ReviewGate blocks downstream progression until a layer's reviews all pass.
type ReviewGate struct {
	graphClient *graph.Client
	maxRetries  int
}

// NewReviewGate creates a ReviewGate with the given client and retry limit.
func NewReviewGate(gc *graph.Client, maxRetries int) *ReviewGate {
	if maxRetries <= 0 {
		maxRetries = 3
	}
	return &ReviewGate{graphClient: gc, maxRetries: maxRetries}
}

// CheckLayer returns true if ALL child nodes of parentID pass their reviews.
func (g *ReviewGate) CheckLayer(ctx context.Context, parentID string) (passed bool, failedIDs []string, err error) {
	statuses, err := g.graphClient.GetLayerReviewStatus(ctx, parentID)
	if err != nil {
		return false, nil, fmt.Errorf("get layer review status: %w", err)
	}

	for _, s := range statuses {
		nodeID, _ := s["nodeID"].(string)
		reviewPassed, _ := s["reviewPassed"].(bool)
		if !reviewPassed && nodeID != "" {
			failedIDs = append(failedIDs, nodeID)
		}
	}

	return len(failedIDs) == 0, failedIDs, nil
}

// CheckNodeSubtree returns true if the node and all its descendants pass reviews.
func (g *ReviewGate) CheckNodeSubtree(ctx context.Context, nodeID string) (passed bool, failedIDs []string, err error) {
	violations, err := g.graphClient.GetConstraintViolations(ctx, nodeID)
	if err != nil {
		return false, nil, fmt.Errorf("get constraint violations: %w", err)
	}

	for _, v := range violations {
		if id, ok := v["nodeID"].(string); ok && id != "" {
			failedIDs = append(failedIDs, id)
		}
	}

	return len(failedIDs) == 0, failedIDs, nil
}

// RepairAndRetry attempts to repair a failed node and re-review it.
// Returns nil if repair succeeded, or ErrRepairExhausted if max retries exceeded.
func (g *ReviewGate) RepairAndRetry(ctx context.Context, nodeID string, runReviews func(ctx context.Context, nodeID string) ([]graph.ReviewInput, error), runRepair func(ctx context.Context, nodeID string, reviews []graph.ReviewInput) error) error {
	for attempt := 1; attempt <= g.maxRetries; attempt++ {
		log.Infof("[review-gate] repair attempt %d/%d for node %s", attempt, g.maxRetries, nodeID)

		reviews, err := runReviews(ctx, nodeID)
		if err != nil {
			log.Errorf("[review-gate] review failed for node %s: %v", nodeID, err)
			continue
		}

		allPassed := true
		for _, r := range reviews {
			if !r.Passed {
				allPassed = false
				break
			}
		}

		if allPassed {
			_ = g.graphClient.UpdateNode(ctx, nodeID, map[string]any{
				"status":       "approved",
				"reviewPassed": true,
			})
			log.Infof("[review-gate] node %s passed after %d attempts", nodeID, attempt)
			return nil
		}

		if err := runRepair(ctx, nodeID, reviews); err != nil {
			log.Errorf("[review-gate] repair failed for node %s: %v", nodeID, err)
		}
	}

	_ = g.graphClient.UpdateNode(ctx, nodeID, map[string]any{
		"status": "blocked",
	})
	log.Errorf("[review-gate] node %s blocked after %d retries", nodeID, g.maxRetries)
	return fmt.Errorf("repair exhausted for node %s after %d retries", nodeID, g.maxRetries)
}