package session

import (
	trpcsession "trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

// NewService returns the process-local framework backend used while a durable
// deployment backend is selected. The interface remains session.Service, so a
// Postgres/Redis implementation can replace it without changing Agent assembly.
func NewService() trpcsession.Service {
	return inmemory.NewSessionService()
}
