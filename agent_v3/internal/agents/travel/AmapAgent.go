package travel

import (
	"context"
	"net/http"

	amapagent "agent_v3/internal/agents/amap"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
)

func AmapAgent() agentcore.Agent {
	return amapagent.AmapAgent()
}

func AmapAgentRun(userID, sessionID, userMessage string) (<-chan *event.Event, context.CancelFunc, error) {
	return amapagent.AmapAgentRun(userID, sessionID, userMessage)
}

func NewAmapAGUIHandler() (http.Handler, func(), error) {
	return amapagent.NewAmapAGUIHandler()
}
