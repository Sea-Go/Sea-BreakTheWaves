// Package runtime assembles the framework Runner and preserves its event lifecycle.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/plugin"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session"
)

var (
	ErrIncomplete          = errors.New("runner stream closed without completion")
	ErrDuplicateCompletion = errors.New("runner emitted duplicate completion")
	ErrClosed              = errors.New("runtime closed")
	ErrRunActive           = errors.New("run ID is already active")
)

// SubjectRef is the complete H01 scope; no delimiter concatenation can alias users.
type SubjectRef struct {
	AuthorityID string `json:"authority_id"`
	TenantID    string `json:"tenant_id"`
	SubjectID   string `json:"subject_id"`
}

func (s SubjectRef) UserKey() (string, error) {
	if s.AuthorityID == "" || s.TenantID == "" || s.SubjectID == "" {
		return "", errors.New("complete subject reference required")
	}
	raw, _ := json.Marshal(s)
	sum := sha256.Sum256(raw)
	return "subject:" + hex.EncodeToString(sum[:]), nil
}

type Request struct {
	Subject   SubjectRef
	SessionID string
	RunID     string
	Message   model.Message
	Options   []agent.RunOption
}
type Result struct {
	RunID     string
	Events    int
	Completed bool
}
type Sink func(context.Context, *event.Event) error

type Runtime struct {
	runner       runner.Runner
	sessions     session.Service
	ownedSession bool
	mu           sync.Mutex
	closed       bool
	active       map[string]context.CancelFunc
	wg           sync.WaitGroup
	closeOnce    sync.Once
	closeError   error
}

// New borrows sessions. The caller closes it after Runtime.Close returns.
func New(app string, ag agent.Agent, sessions session.Service) (*Runtime, error) {
	if app == "" || ag == nil || sessions == nil {
		return nil, errors.New("runtime requires app, agent and session service")
	}
	tracked := &trackedSessions{Service: sessions}
	return &Runtime{runner: runner.NewRunner(app, ag, runner.WithSessionService(tracked), runner.WithPlugins(errorOwnership{})), sessions: sessions, active: map[string]context.CancelFunc{}}, nil
}

// Run forwards every event through EOF. Completion is a runtime boundary, not a
// product publication or knowledge acceptance receipt. Sink failure cancels work.
func (r *Runtime) Run(parent context.Context, q Request, sink Sink) (result Result, err error) {
	result.RunID = q.RunID
	user, err := q.Subject.UserKey()
	if err != nil {
		return result, err
	}
	if q.RunID == "" || q.SessionID == "" {
		return result, errors.New("run ID and session ID required")
	}
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cancel()
		return result, ErrClosed
	}
	if _, ok := r.active[q.RunID]; ok {
		r.mu.Unlock()
		cancel()
		return result, ErrRunActive
	}
	r.active[q.RunID] = cancel
	r.wg.Add(1)
	r.mu.Unlock()
	defer func() { cancel(); r.mu.Lock(); delete(r.active, q.RunID); r.mu.Unlock(); r.wg.Done() }()
	persistence := &persistenceErrors{}
	ctx = context.WithValue(ctx, persistenceKey{}, persistence)
	opts := append([]agent.RunOption(nil), q.Options...)
	opts = append(opts, agent.WithRequestID(q.RunID))
	events, err := r.runner.Run(ctx, user, q.SessionID, q.Message, opts...)
	if err != nil {
		return result, fmt.Errorf("start runner: %w", err)
	}
	if events == nil {
		return result, ErrIncomplete
	}
	var first error
	cancelled := ctx.Done()
	sinkEnabled := true
	for {
		select {
		case <-cancelled:
			first = errors.Join(first, ctx.Err())
			cancelled = nil
			sinkEnabled = false
		case e, ok := <-events:
			if !ok {
				if !result.Completed {
					first = errors.Join(first, ErrIncomplete)
				}
				return result, errors.Join(first, ctx.Err(), persistence.get())
			}
			if e == nil {
				continue
			}
			result.Events++
			if e.RequestID != q.RunID {
				first = errors.Join(first, errors.New("runner request ID mismatch"))
			}
			if e.IsTerminalError() && first == nil {
				first = fmt.Errorf("runner %s: %s", e.Error.Type, e.Error.Message)
			}
			if e.IsRunnerCompletion() {
				if result.Completed {
					first = errors.Join(first, ErrDuplicateCompletion)
				}
				result.Completed = true
			}
			if sinkEnabled && sink != nil {
				if err := sink(ctx, e); err != nil {
					cancel()
					first = errors.Join(first, err)
					sinkEnabled = false
				}
			}
		}
	}
}
func (r *Runtime) Cancel(runID string) bool {
	r.mu.Lock()
	cancel, ok := r.active[runID]
	r.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}
func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		for _, cancel := range r.active {
			cancel()
		}
		r.mu.Unlock()
		r.wg.Wait()
		r.closeError = r.runner.Close()
		if r.ownedSession {
			r.closeError = errors.Join(r.closeError, r.sessions.Close())
		}
	})
	return r.closeError
}

type persistenceKey struct{}
type persistenceErrors struct {
	mu  sync.Mutex
	err error
}

func (p *persistenceErrors) add(err error) {
	if err != nil {
		p.mu.Lock()
		p.err = errors.Join(p.err, err)
		p.mu.Unlock()
	}
}
func (p *persistenceErrors) get() error { p.mu.Lock(); defer p.mu.Unlock(); return p.err }

type trackedSessions struct{ session.Service }

func (s *trackedSessions) AppendEvent(ctx context.Context, sess *session.Session, e *event.Event, opts ...session.Option) error {
	// The framework may rewrite historical tool choices on the next model step.
	// Persist an owned public Event.Clone so the outward stream does not alias it.
	err := s.Service.AppendEvent(ctx, sess, e.Clone(), opts...)
	if p, ok := ctx.Value(persistenceKey{}).(*persistenceErrors); ok && err != nil {
		p.add(fmt.Errorf("persist session event: %w", err))
	}
	return err
}

// errorOwnership runs before Runner's in-place error-content repair. A copy at
// this public hook keeps arbitrary Agents/Models safe, not only our DC adapter.
type errorOwnership struct{}

func (errorOwnership) Name() string { return "sea.error-event-ownership" }
func (errorOwnership) Register(r *plugin.Registry) {
	r.OnEvent(func(_ context.Context, _ *agent.Invocation, e *event.Event) (*event.Event, error) {
		if e != nil && e.Response != nil && e.Error != nil {
			return e.Clone(), nil
		}
		return nil, nil
	})
}
