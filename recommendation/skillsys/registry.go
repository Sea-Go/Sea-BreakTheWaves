package skillsys

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"

	"sea/metrics"

	"sea/zlog"

	"go.uber.org/zap"
)

var (
	ErrToolNotFound = errors.New("未找到工具")
)

// Registry 负责工具注册与调用。
// 设计目标：
// - 高可维护：工具是独立的实现，统一注册即可被 Agent 暴露给模型。
// - 高可扩展：未来可加权限、灰度、配额、审计等机制。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
	zlog.L().Info("注册工具", zap.String("tool", t.Name()))
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res := make([]string, 0, len(r.tools))
	for k := range r.tools {
		res = append(res, k)
	}
	return res
}

// Invoke 调用指定工具，并把输出序列化为 JSON 字符串（用于 OpenAI tool message）。
func (r *Registry) Invoke(ctx context.Context, toolName string, argsRaw json.RawMessage) (string, any, error) {
	t, ok := r.Get(toolName)
	if !ok {
		return "", nil, ErrToolNotFound
	}

	// 工具调用也纳入 agent trace/span（execute_tool.xxx）
	ctx2, sp := zlog.StartSpan(ctx, "execute_tool."+toolName)

	out, err := t.Invoke(ctx2, argsRaw)
	if err != nil {
		metrics.GenRecAgentToolCallsTotalMetric.WithLabelValues(toolName, "error").Inc()
		sp.End(zlog.StatusError, err)
		return "", nil, err
	}

	metrics.GenRecAgentToolCallsTotalMetric.WithLabelValues(toolName, "ok").Inc()

	str, err := MarshalResult(out)
	if err != nil {
		sp.End(zlog.StatusError, err)
		return "", nil, err
	}

	sp.End(zlog.StatusOK, nil)
	return str, out, nil
}

// List 返回当前已注册的工具列表（仅用于调试/自检）。
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var names []string
	for k := range r.tools {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
