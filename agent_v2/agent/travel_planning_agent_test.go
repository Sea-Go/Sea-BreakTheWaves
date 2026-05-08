package agent

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
		"tools.NewDefaultZhihuTools()",
		"tools.NewDefaultBilibiliTools()",
		"llmagent.WithTools(guideTools)",
		"AmapAgent()",
		"ReviewWorkflowAgent()",
		"ReviewThinkingAgent()",
		"ReviewContentAgent()",
		"ReviewOutputAgent()",
		"ReviewLazinessAgent()",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("TravelPlanningAgent.go missing %q", want)
		}
	}
}
