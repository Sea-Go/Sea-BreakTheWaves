package runner

import (
	"fmt"

	trpcagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/runner"

	"sea/service/common/trpcagent/session"
)

type Dependencies struct {
	AppName        string
	Agent          trpcagent.Agent
	SessionService bool
}

func New(deps Dependencies) (runner.Runner, error) {
	if deps.AppName == "" {
		return nil, fmt.Errorf("runner app name is required")
	}
	if deps.Agent == nil {
		return nil, fmt.Errorf("runner agent is required")
	}
	opts := []runner.Option{}
	if deps.SessionService {
		opts = append(opts, runner.WithSessionService(session.NewService()))
	}
	return runner.NewRunner(deps.AppName, deps.Agent, opts...), nil
}
