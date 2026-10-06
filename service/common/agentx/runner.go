// Package agentx manages framework execution, not search or recommendation policy.
package agentx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/google/uuid"
	"io"
	"strings"
	"sync"
	"time"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type Config struct {
	MaxOutputTokens int `json:",default=4096"`
	MaxLLMCalls     int
	MaxToolCalls    int
}

type Engine struct {
	app      string
	runner   runner.Runner
	sessions session.Service
	mu       sync.Mutex
	closed   bool
	runs     map[string]context.CancelFunc
	wg       sync.WaitGroup
}

func New(app string, m model.Model, instruction string, tools []tool.Tool, c Config) (*Engine, error) {
	if m == nil || app == "" || c.MaxLLMCalls < 1 || c.MaxToolCalls < 1 {
		return nil, item.ErrInvalid
	}
	sessions := &synchronizedSession{Service: inmemory.NewSessionService(inmemory.WithSessionTTL(5 * time.Minute))}
	tokens := c.MaxOutputTokens
	if tokens < 1 {
		tokens = 4096
	}
	a := llmagent.New(app, llmagent.WithModel(m), llmagent.WithGenerationConfig(model.GenerationConfig{MaxTokens: &tokens, Stream: true}), llmagent.WithInstruction(instruction), llmagent.WithTools(tools), llmagent.WithMaxLLMCalls(c.MaxLLMCalls), llmagent.WithMaxToolIterations(c.MaxToolCalls))
	return &Engine{app: app, runner: runner.NewRunner(app, a, runner.WithSessionService(sessions)), sessions: sessions, runs: map[string]context.CancelFunc{}}, nil
}
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	for _, cancel := range e.runs {
		cancel()
	}
	e.mu.Unlock()
	e.wg.Wait()
	return errors.Join(e.runner.Close(), e.sessions.Close())
}
func (e *Engine) Run(ctx context.Context, user, prompt string, emit func(string) error) (string, error) {
	if user == "" {
		user = "worker"
	}
	id := uuid.NewString()
	ctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		cancel()
		return "", item.ErrUnavailable
	}
	e.runs[id] = cancel
	e.wg.Add(1)
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		delete(e.runs, id)
		e.mu.Unlock()
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = e.sessions.DeleteSession(cleanup, session.Key{AppName: e.app, UserID: user, SessionID: id})
		e.wg.Done()
	}()
	events, err := e.runner.Run(ctx, user, id, model.NewUserMessage(prompt))
	if err != nil {
		return "", fmt.Errorf("start model run: %w", err)
	}
	if events == nil {
		return "", io.ErrUnexpectedEOF
	}
	var text string
	var terminal error
	complete := false
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case ev, ok := <-events:
			if !ok {
				if terminal != nil {
					return "", terminal
				}
				if !complete {
					return "", io.ErrUnexpectedEOF
				}
				return text, nil
			}
			if ev == nil {
				continue
			}
			if ev.IsTerminalError() {
				terminal = fmt.Errorf("model run: %w", ev.Error)
			}
			if ev.Response != nil && len(ev.Choices) > 0 && terminal == nil {
				choice := ev.Choices[0]
				if ev.IsPartial && choice.Delta.Content != "" && emit != nil {
					if err = emit(choice.Delta.Content); err != nil {
						return "", err
					}
				}
				if !ev.IsPartial && choice.Message.Role == model.RoleAssistant && len(choice.Message.ToolCalls) == 0 {
					text = choice.Message.Content
				}
			}
			if ev.IsRunnerCompletion() {
				complete = true
				if err = ctx.Err(); err != nil {
					return "", err
				}
				if terminal != nil {
					return "", terminal
				}
				return text, nil
			}
		}
	}
}
func Decode(text string, out any) error {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		i := strings.Index(text, "\n")
		if i >= 0 {
			text = strings.TrimSuffix(strings.TrimSpace(text[i+1:]), "```")
		}
	}
	d := json.NewDecoder(strings.NewReader(text))
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("model structured output: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("model returned trailing output")
	}
	return nil
}
