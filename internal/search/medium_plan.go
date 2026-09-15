package search

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const maxFastMediumNewQueries = 2
const maxFastMediumQueryBytes = 512
const maxFastMediumPlanBytes = 4096
const fastMediumInstruction = "Rephrase the user's search question into one or two distinct, useful retrieval queries. Return exactly one JSON object: {\"queries\":[\"query 1\",\"query 2\"]}. Do not repeat the original question. Do not answer it. Do not add scope, filters, explanation or other keys."

var ErrFastMediumPlan = errors.New("fast medium model plan invalid")

type fastMediumPlanKey struct{}

type fastMediumPlan struct {
	original string
	new      []string
}

// ParseFastMediumPlan accepts one model-produced JSON plan. It never permits
// the model to change the original question, publication scope or budgets.
func ParseFastMediumPlan(raw, original string) ([]string, error) {
	if strings.TrimSpace(original) == "" || len(raw) > maxFastMediumPlanBytes {
		return nil, ErrFastMediumPlan
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, ErrFastMediumPlan
	}
	keyStart := decoder.InputOffset()
	key, err := decoder.Token()
	if err != nil || key != "queries" || strings.TrimSpace(raw[keyStart:decoder.InputOffset()]) != `"queries"` {
		return nil, ErrFastMediumPlan
	}
	var queries []string
	if decoder.Decode(&queries) != nil {
		return nil, ErrFastMediumPlan
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, ErrFastMediumPlan
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrFastMediumPlan
	}
	return normalizeFastMediumQueries(original, queries)
}

func normalizeFastMediumQueries(original string, proposed []string) ([]string, error) {
	if len(proposed) < 1 || len(proposed) > maxFastMediumNewQueries {
		return nil, ErrFastMediumPlan
	}
	seen := map[string]bool{canonicalFastMediumQuery(original): true}
	owned := make([]string, 0, len(proposed))
	for _, raw := range proposed {
		q := strings.TrimSpace(raw)
		canonical := canonicalFastMediumQuery(q)
		if q == "" || len(q) > maxFastMediumQueryBytes || seen[canonical] {
			return nil, ErrFastMediumPlan
		}
		seen[canonical] = true
		owned = append(owned, q)
	}
	return owned, nil
}

func canonicalFastMediumQuery(q string) string {
	return strings.ToLower(strings.Join(strings.Fields(q), " "))
}

// WithFastMediumPlan carries only checked new query strings through the
// existing root Graph's search node. It is private to this Runner invocation.
func WithFastMediumPlan(ctx context.Context, original string, newQueries []string) (context.Context, error) {
	if ctx == nil || strings.TrimSpace(original) == "" {
		return nil, ErrFastMediumPlan
	}
	owned, err := normalizeFastMediumQueries(original, newQueries)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, fastMediumPlanKey{}, fastMediumPlan{original: original, new: owned}), nil
}

// PlanFastMedium returns one batch: the immutable original plus one or two
// model-proposed queries. Search Service still enforces the profile budget.
func PlanFastMedium(ctx context.Context, in PlanInput) ([]string, error) {
	if ctx == nil || in.Depth != Fast || in.Intelligence != Medium || in.Round != 1 {
		return nil, ErrUnavailable
	}
	plan, ok := ctx.Value(fastMediumPlanKey{}).(fastMediumPlan)
	if !ok || plan.original != in.Query || len(plan.new) < 1 || len(plan.new) > maxFastMediumNewQueries {
		return nil, ErrUnavailable
	}
	if len(plan.new)+1 > in.RemainingSubqueries {
		return nil, ErrBudget
	}
	return append([]string{in.Query}, plan.new...), nil
}
