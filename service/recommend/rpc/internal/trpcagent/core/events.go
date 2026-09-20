package recommendationv2

import (
	"context"
	"sync"
)

type HookDecision int

const (
	HookContinue HookDecision = iota
	HookReject
)

type HookMode int

const (
	HookModeBlocking HookMode = iota
	HookModeAsync
)

type HookResult struct {
	Decision HookDecision
	Reason   string
}

type Hook interface {
	Name() string
	OnEvent(ctx context.Context, event BehaviorEvent) (HookResult, error)
}

type HookFailureReporter func(event BehaviorEvent, hookName string, mode HookMode, err error)

type HookRegistry struct {
	mu    sync.RWMutex
	hooks []registeredHook
}

type registeredHook struct {
	hook Hook
	mode HookMode
}

func NewHookRegistry() *HookRegistry {
	return &HookRegistry{hooks: make([]registeredHook, 0)}
}

func (r *HookRegistry) Register(h Hook) {
	r.RegisterBlocking(h)
}

func (r *HookRegistry) RegisterBlocking(h Hook) {
	if r == nil || h == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = append(r.hooks, registeredHook{hook: h, mode: HookModeBlocking})
}

func (r *HookRegistry) RegisterAsync(h Hook) {
	if r == nil || h == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = append(r.hooks, registeredHook{hook: h, mode: HookModeAsync})
}

func (b *EventBus) RegisterAsync(h Hook) {
	if b == nil || b.hooks == nil {
		return
	}
	b.hooks.RegisterAsync(h)
}

func (r *HookRegistry) Hooks() []Hook {
	return r.HooksByMode(HookModeBlocking)
}

func (r *HookRegistry) HooksByMode(mode HookMode) []Hook {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Hook, 0, len(r.hooks))
	for _, h := range r.hooks {
		if h.mode == mode {
			out = append(out, h.hook)
		}
	}
	return out
}

type EventBus struct {
	hooks    *HookRegistry
	reporter HookFailureReporter
}

func NewEventBus(hooks *HookRegistry) *EventBus {
	if hooks == nil {
		hooks = NewHookRegistry()
	}
	return &EventBus{hooks: hooks}
}

func (b *EventBus) SetFailureReporter(reporter HookFailureReporter) {
	if b == nil {
		return
	}
	b.reporter = reporter
}

func (b *EventBus) Publish(ctx context.Context, event BehaviorEvent) (bool, []string) {
	if b == nil || b.hooks == nil {
		return true, nil
	}
	var failures []string
	for _, h := range b.hooks.HooksByMode(HookModeBlocking) {
		result, err := h.OnEvent(ctx, event)
		if err != nil {
			b.reportFailure(event, h.Name(), HookModeBlocking, err)
			failures = append(failures, h.Name()+": "+err.Error())
			continue
		}
		if result.Decision == HookReject {
			reason := h.Name() + ": " + result.Reason
			b.reportFailure(event, h.Name(), HookModeBlocking, hookRejectedError(result.Reason))
			failures = append(failures, reason)
			return false, failures
		}
	}
	for _, h := range b.hooks.HooksByMode(HookModeAsync) {
		hook := h
		go func() {
			result, err := hook.OnEvent(ctx, event)
			if err != nil {
				b.reportFailure(event, hook.Name(), HookModeAsync, err)
				return
			}
			if result.Decision == HookReject {
				b.reportFailure(event, hook.Name(), HookModeAsync, hookRejectedError(result.Reason))
			}
		}()
	}
	return true, failures
}

func (b *EventBus) reportFailure(event BehaviorEvent, hookName string, mode HookMode, err error) {
	if b == nil || b.reporter == nil || err == nil {
		return
	}
	b.reporter(event, hookName, mode, err)
}

type hookRejectedError string

func (e hookRejectedError) Error() string {
	if e == "" {
		return "rejected"
	}
	return string(e)
}
