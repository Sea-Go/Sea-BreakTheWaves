package recall

import (
	"sync"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现召回器注册表 Registry，按名称注册与获取 domain.Recaller。
//
// Registry 是召回源的中央注册表，OrchestratorAgent/RecallPlannerAgent 通过
// 名称获取召回器实例。注册表本身线程安全（sync.RWMutex 保护）。
//
// 二开扩展点：
//   - 注册新召回器：实现 domain.Recaller interface，调用 Register(name, r)
//     注入，无需修改核心代码。例如业务方实现"活动文章召回器"并在 main 中
//     registry.Register("activity", NewActivityRecaller(...))，RecallPlanner
//     即可在规划中选用。
//   - 替换默认召回器：用同名 Register 覆盖默认实现（如自研向量召回替换
//     默认 ContentRecaller）。
// ============================================================================

// Registry 召回器注册表，按名称管理 domain.Recaller 实例。
// 线程安全，支持并发注册与获取。
type Registry struct {
	mu       sync.RWMutex
	recaller map[string]domain.Recaller
}

// NewRegistry 创建空的召回器注册表。
func NewRegistry() *Registry {
	return &Registry{
		recaller: make(map[string]domain.Recaller),
	}
}

// Register 注册召回器。若 name 已存在则覆盖（支持二开替换默认实现）。
//
// 二开说明：业务方实现 domain.Recaller 后，在应用启动阶段调用本方法注入
// 自定义召回器，OrchestratorAgent 即可在召回规划中按名称选用，无需改动
// 框架核心代码。
func (r *Registry) Register(name string, rc domain.Recaller) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recaller[name] = rc
}

// Get 按名称获取召回器。返回召回器与是否命中。
func (r *Registry) Get(name string) (domain.Recaller, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rc, ok := r.recaller[name]
	return rc, ok
}

// List 返回所有已注册召回器名称列表（无序）。
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.recaller))
	for name := range r.recaller {
		names = append(names, name)
	}
	return names
}
