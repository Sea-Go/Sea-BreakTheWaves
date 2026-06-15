package travel

import (
	"net/http"

	dili360agent "agent_v3/internal/agents/dili360"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
)

func Dili360Agent() agentcore.Agent {
	return dili360agent.Dili360Agent()
}

func NewDili360AGUIHandler() (http.Handler, func(), error) {
	return dili360agent.NewDili360AGUIHandler()
}
