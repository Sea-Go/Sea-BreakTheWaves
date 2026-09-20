package agent

import (
	"fmt"

	trpcagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	trpcmodel "trpc.group/trpc-go/trpc-agent-go/model"
	trpctool "trpc.group/trpc-go/trpc-agent-go/tool"
)

type Dependencies struct {
	Model        trpcmodel.Model
	Instruction  string
	Tools        []trpctool.Tool
	MaxToolCalls int
}

func validate(deps Dependencies) error {
	if deps.Model == nil {
		return fmt.Errorf("recommend agent model is required")
	}
	if deps.Instruction == "" {
		return fmt.Errorf("recommend agent instruction is required")
	}
	return nil
}

func NewRecommendAgent(deps Dependencies) (trpcagent.Agent, error) {
	if err := validate(deps); err != nil {
		return nil, err
	}
	opts := []llmagent.Option{
		llmagent.WithModel(deps.Model),
		llmagent.WithInstruction(deps.Instruction),
		llmagent.WithTools(deps.Tools),
	}
	if deps.MaxToolCalls > 0 {
		opts = append(opts, llmagent.WithMaxToolIterations(deps.MaxToolCalls))
	}
	return llmagent.New("sea_recommend", opts...), nil
}
