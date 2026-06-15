package review

import (
	"strings"

	"agent_v3/internal/agents/modelrouter"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type ModelLevel = modelrouter.ModelLevel

const (
	ModelLevelHigh   = modelrouter.ModelLevelHigh
	ModelLevelMedium = modelrouter.ModelLevelMedium
	ModelLevelLow    = modelrouter.ModelLevelLow
)

type NamedAgent struct {
	Name  string
	Agent agentcore.Agent
}

func newModelForLevel(agentName string, level ModelLevel) model.Model {
	return modelrouter.NewModelForLevel(agentName, level)
}

func DefaultDayReviewAgents() []NamedAgent {
	return []NamedAgent{
		{Name: "workflow", Agent: ReviewWorkflowAgent()},
		{Name: "thinking", Agent: ReviewThinkingAgent()},
		{Name: "content", Agent: ReviewContentAgent()},
		{Name: "output", Agent: ReviewOutputAgent()},
		{Name: "laziness", Agent: ReviewLazinessAgent()},
	}
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
