package travel

import (
	"context"

	"agent_v3/internal/agents/modelrouter"

	"trpc.group/trpc-go/trpc-agent-go/model"
)

type ModelLevel = modelrouter.ModelLevel

const (
	ModelLevelHigh   = modelrouter.ModelLevelHigh
	ModelLevelMedium = modelrouter.ModelLevelMedium
	ModelLevelLow    = modelrouter.ModelLevelLow
)

func SelectModel(level ModelLevel) string {
	return modelrouter.SelectModel(level)
}

func newModelForLevel(agentName string, level ModelLevel) model.Model {
	return modelrouter.NewModelForLevel(agentName, level)
}

func newSummaryModel(agentName string) model.Model {
	return modelrouter.NewSummaryModel(agentName)
}

func NewSummaryModel(agentName string) model.Model {
	return modelrouter.NewSummaryModel(agentName)
}

func contextWithModelUsageEmitter(ctx context.Context, emitter modelrouter.UsageEmitter) context.Context {
	return modelrouter.ContextWithUsageEmitter(ctx, emitter)
}
