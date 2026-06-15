package travel

import (
	"os"
	"strings"
	"testing"
)

func TestTravelPlanningAgentRegistersToolsAndTeam(t *testing.T) {
	body, err := os.ReadFile("TravelPlanningAgent.go")
	if err != nil {
		t.Fatalf("read TravelPlanningAgent.go: %v", err)
	}
	source := string(body)

	for _, want := range []string{
		"zhihutools.NewDefaultZhihuTools()",
		"bilibilitools.NewDefaultBilibiliTools()",
		"llmagent.WithTools(guideTools)",
		"AmapAgent()",
		"review.ReviewWorkflowAgent()",
		"review.ReviewThinkingAgent()",
		"review.ReviewContentAgent()",
		"review.ReviewOutputAgent()",
		"review.ReviewLazinessAgent()",
		"workflowstages.NewGraphWorkflowAgent(",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("TravelPlanningAgent.go missing %q", want)
		}
	}
}
