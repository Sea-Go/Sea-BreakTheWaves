package rank

import (
	"fmt"
	"sort"
	"sync"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现 Registry，排序器注册表。
// 提供按名称注册、获取、列出排序器的能力，是排序器依赖注入的入口。
// 线程安全，支持并发注册与获取。
//
// 二开扩展点：
//   - 注册新排序器：实现 domain.Ranker 并通过 Register(name, r) 注入
//   - 业务规则注入：实现 RuleRanker 注入活动期间等业务规则排序
//   - 多模型切换：注册多个模型版本，运行时按频道/场景/AB 分桶选取
// ============================================================================

// Registry 排序器注册表，按名称管理 domain.Ranker。
//
// 线程安全，使用 sync.RWMutex 保护并发访问。
// 二开扩展点：业务方实现 domain.Ranker 后通过 Register 注入，
// OrchestratorAgent 通过 Get 按名称获取排序器。
type Registry struct {
	mu      sync.RWMutex
	rankers map[string]domain.Ranker
}

// NewRegistry 创建空的排序器注册表。
func NewRegistry() *Registry {
	return &Registry{rankers: make(map[string]domain.Ranker)}
}

// Register 注册排序器。
//
// name 为排序器名称（需与 Ranker.Name() 一致）；ranker 为排序器实现。
// 若 name 已存在则返回 error，避免覆盖已注册实现。
//
// 二开：业务方在 init() 或装配阶段调用此方法注入自定义排序器。
func (r *Registry) Register(name string, ranker domain.Ranker) error {
	if name == "" {
		return fmt.Errorf("ranker name cannot be empty")
	}
	if ranker == nil {
		return fmt.Errorf("ranker cannot be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.rankers[name]; exists {
		return fmt.Errorf("ranker %q already registered", name)
	}
	r.rankers[name] = ranker
	return nil
}

// Get 按名称获取排序器。
//
// 若 name 不存在返回 error。调用方应处理 not found 场景（如回退默认排序器）。
func (r *Registry) Get(name string) (domain.Ranker, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ranker, ok := r.rankers[name]
	if !ok {
		return nil, fmt.Errorf("ranker %q not found", name)
	}
	return ranker, nil
}

// List 列出所有已注册排序器名称（按字典序）。
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.rankers))
	for name := range r.rankers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
