package search

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestFastMediumModelPlanCannotChangeOriginalScopeOrBudget(t *testing.T) {
	original := "What does WhaleHall's desktop pet do?"
	for _, scenario := range []struct {
		name string
		raw  string
	}{
		{"empty", `{"queries":[]}`},
		{"repeat_original", `{"queries":["what does whalehall's desktop pet do?"]}`},
		{"duplicate", `{"queries":["pet feedback"," PET  feedback "]}`},
		{"duplicate_key", `{"queries":["pet feedback"],"queries":["desktop reflection"]}`},
		{"unicode_duplicate_key", `{"queries":["pet feedback"],"\u0071ueries":["desktop reflection"]}`},
		{"unicode_key_spelling", `{"\u0071ueries":["pet feedback"]}`},
		{"three_new", `{"queries":["one","two","three"]}`},
		{"unknown_scope", `{"queries":["pet feedback"],"module_id":"other"}`},
		{"two_objects", `{"queries":["pet feedback"]}{"queries":["other"]}`},
		{"null_query", `{"queries":[null]}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if _, err := ParseFastMediumPlan(scenario.raw, original); !errors.Is(err, ErrFastMediumPlan) {
				t.Fatalf("invalid model plan accepted: %v", err)
			}
		})
	}
	newQueries, err := ParseFastMediumPlan(`{"queries":[" pet proactive feedback ","agent reflection in desktop pet"]}`, original)
	if err != nil || !reflect.DeepEqual(newQueries, []string{"pet proactive feedback", "agent reflection in desktop pet"}) {
		t.Fatalf("valid model queries = %v, err=%v", newQueries, err)
	}
	ctx, err := WithFastMediumPlan(context.Background(), original, newQueries)
	if err != nil {
		t.Fatal(err)
	}
	newQueries[0] = "mutated"
	queries, err := PlanFastMedium(ctx, PlanInput{Query: original, Depth: Fast, Intelligence: Medium, Round: 1, RemainingSubqueries: 3})
	if err != nil || !reflect.DeepEqual(queries, []string{original, "pet proactive feedback", "agent reflection in desktop pet"}) {
		t.Fatalf("checked one-batch plan = %v, err=%v", queries, err)
	}
	if _, err := PlanFastMedium(ctx, PlanInput{Query: original, Depth: Fast, Intelligence: Medium, Round: 1, RemainingSubqueries: 2}); !errors.Is(err, ErrBudget) {
		t.Fatalf("model plan escaped fixed query budget: %v", err)
	}
	if _, err := PlanFastMedium(context.Background(), PlanInput{Query: original, Depth: Fast, Intelligence: Medium, Round: 1, RemainingSubqueries: 3}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("medium request without model stage was downgraded: %v", err)
	}
	if _, err := PlanFastMedium(ctx, PlanInput{Query: "changed scope", Depth: Fast, Intelligence: Medium, Round: 1, RemainingSubqueries: 3}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("model changed authoritative original query: %v", err)
	}
}
