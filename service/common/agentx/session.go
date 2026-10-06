package agentx

import (
	"context"
	"sync"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/session"
)

// synchronizedSession adapts v1.10.0's unsynchronized UpdatedAt write using only
// the public Session/Service API. Tool state snapshots call Session.Clone while
// Runner persists events. Persist a detached snapshot, then publish events and
// timestamp under the same EventMu used by Clone. Remove when the upstream
// contract supplies equivalent synchronization and the race regression passes.
type synchronizedSession struct {
	session.Service
	mu sync.Mutex
}

func (s *synchronizedSession) AppendEvent(ctx context.Context, active *session.Session, e *event.Event, opts ...session.Option) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := active.Clone()
	if err := s.Service.AppendEvent(ctx, snapshot, e, opts...); err != nil {
		return err
	}
	active.EventMu.Lock()
	active.Events = append([]event.Event(nil), snapshot.Events...)
	active.UpdatedAt = snapshot.UpdatedAt
	active.EventMu.Unlock()
	active.ApplyEventStateDelta(e)
	return nil
}
