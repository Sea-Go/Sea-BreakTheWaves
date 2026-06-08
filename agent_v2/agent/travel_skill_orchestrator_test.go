package agent

import "testing"

func TestBuildPlanningDecisionAsksDetailRoundForShortTrip(t *testing.T) {
	snap := TravelRequirementSnapshot{
		StartCity:        "北京",
		TotalDays:        3,
		DestinationScope: "河北",
	}

	first := buildPlanningDecision(snap, 0, 2, "从北京出发规划3天自驾")
	if first.Ready {
		t.Fatal("short trip with missing details should ask at least one round")
	}
	if !first.ShouldAskUser || len(first.Questions) == 0 {
		t.Fatalf("expected follow-up questions, got %#v", first)
	}

	second := buildPlanningDecision(snap, 1, 2, "预算中等，其他你看着安排")
	if !second.Ready {
		t.Fatalf("after one detail round, P0-complete trip should be ready: %#v", second)
	}
}

func TestBuildPlanningDecisionExplicitDefaultSkipsDetailRound(t *testing.T) {
	snap := TravelRequirementSnapshot{
		StartCity:        "北京",
		TotalDays:        3,
		DestinationScope: "河北",
	}

	decision := buildPlanningDecision(snap, 0, 2, "从北京出发规划3天自驾，按默认直接规划")
	if !decision.Ready {
		t.Fatalf("explicit default intent should allow planning: %#v", decision)
	}
}
