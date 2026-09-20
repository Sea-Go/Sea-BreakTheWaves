// internal/eval/cases.go — 评估集管理（Task 15.1）。
//
// 职责：
//   - 定义评估用例 EvalCase 与过滤条件 CaseFilter
//   - 提供 CaseStore interface 抽象持久化（CRUD），二开可替换为 PG/Mongo/Redis
//   - 提供 CaseService 业务层：CRUD 委托 + Validate 校验
//   - 内置 MemoryCaseStore 默认实现（sync.RWMutex + map），用于测试与单机默认
//
// 二开扩展点：
//   - 实现 CaseStore interface 替换持久化后端（如 Postgres）
//   - 调整 Validate 中的合法 Surface 白名单与字段校验规则
//   - 在 CaseService 之上叠加缓存、采样、版本化等业务逻辑
package eval

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ----------------------------------------------------------------------------
// 类型定义
// ----------------------------------------------------------------------------

// EvalCase 评估用例：一条 golden sample，用于离线评估与回归测试。
//
// 字段语义：
//   - ID：用例唯一 ID（创建时自动生成 UUID）
//   - Query：查询文本（推荐/搜索场景输入）
//   - Expected：期望命中的文章 ID 列表（golden set）
//   - Surface：曝光面（必须为合法 Surface，见 validSurfaces）
//   - Channel：频道（IP 频道）
//   - Intent：意图标签（如 "explore/exploit/related"）
//   - CreatedAt/UpdatedAt：Unix 秒时间戳
//   - Tags：自定义标签（用于细分评估维度）
type EvalCase struct {
	// ID 用例 ID。
	ID string `json:"id"`
	// Query 查询文本。
	Query string `json:"query"`
	// Expected 期望命中文章 ID 列表。
	Expected []string `json:"expected"`
	// Surface 曝光面。
	Surface string `json:"surface"`
	// Channel 频道。
	Channel string `json:"channel"`
	// Intent 意图标签。
	Intent string `json:"intent"`
	// CreatedAt 创建时间（Unix 秒）。
	CreatedAt int64 `json:"created_at"`
	// UpdatedAt 更新时间（Unix 秒）。
	UpdatedAt int64 `json:"updated_at"`
	// Tags 自定义标签。
	Tags []string `json:"tags"`
}

// CaseFilter 用例过滤条件。
//
// 字段为零值时表示不过滤该维度；Limit<=0 时默认返回全部。
type CaseFilter struct {
	// Surface 按曝光面过滤。
	Surface string
	// Channel 按频道过滤。
	Channel string
	// Intent 按意图过滤。
	Intent string
	// Limit 返回条数上限（<=0 表示不限）。
	Limit int
	// Offset 偏移量。
	Offset int
}

// validSurfaces 合法 Surface 白名单（与领域 channel/surface 对齐）。
var validSurfaces = map[string]bool{
	"default": true,
	"home":    true,
	"feed":    true,
	"search":  true,
	"channel": true,
	"detail":  true,
	"related": true,
}

// ----------------------------------------------------------------------------
// CaseStore interface
// ----------------------------------------------------------------------------

// CaseStore 评估用例持久化抽象。
//
// 二开：实现该 interface 替换为 PG/Mongo/Redis 等后端。
type CaseStore interface {
	// Create 创建评估用例。
	Create(ctx context.Context, c EvalCase) error
	// Get 按 ID 获取用例。
	Get(ctx context.Context, id string) (EvalCase, error)
	// List 按过滤条件列出用例。
	List(ctx context.Context, filter CaseFilter) ([]EvalCase, error)
	// Update 更新用例（按 c.ID 定位）。
	Update(ctx context.Context, c EvalCase) error
	// Delete 按 ID 删除用例。
	Delete(ctx context.Context, id string) error
}

// ----------------------------------------------------------------------------
// CaseService 业务层
// ----------------------------------------------------------------------------

// CaseService 评估用例业务层：CRUD 委托 + Validate 校验。
//
// 二开扩展点：可在 Create/Update 前后追加缓存、审计、版本化等逻辑。
type CaseService struct {
	// store 持久化后端。
	store CaseStore
}

// NewCaseService 构造 CaseService。
// store 持久化后端（不可为 nil）。
func NewCaseService(store CaseStore) *CaseService {
	return &CaseService{store: store}
}

// Create 创建评估用例（先 Validate，再委托 store）。
// 若 c.ID 为空则自动生成 UUID；CreatedAt/UpdatedAt 自动填充。
func (s *CaseService) Create(ctx context.Context, c EvalCase) error {
	if err := s.Validate(c); err != nil {
		return err
	}
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	now := time.Now().Unix()
	if c.CreatedAt == 0 {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	return s.store.Create(ctx, c)
}

// Get 按 ID 获取用例。
func (s *CaseService) Get(ctx context.Context, id string) (EvalCase, error) {
	if id == "" {
		return EvalCase{}, fmt.Errorf("id 不能为空")
	}
	return s.store.Get(ctx, id)
}

// List 按过滤条件列出用例。
func (s *CaseService) List(ctx context.Context, filter CaseFilter) ([]EvalCase, error) {
	return s.store.List(ctx, filter)
}

// Update 更新用例（先 Validate，再委托 store）。
// 自动更新 UpdatedAt。
func (s *CaseService) Update(ctx context.Context, c EvalCase) error {
	if err := s.Validate(c); err != nil {
		return err
	}
	if c.ID == "" {
		return fmt.Errorf("id 不能为空")
	}
	c.UpdatedAt = time.Now().Unix()
	return s.store.Update(ctx, c)
}

// Delete 按 ID 删除用例。
func (s *CaseService) Delete(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("id 不能为空")
	}
	return s.store.Delete(ctx, id)
}

// Validate 校验评估用例合法性。
// 规则：Query 非空、Expected 非空、Surface 合法（白名单内）。
func (s *CaseService) Validate(c EvalCase) error {
	if c.Query == "" {
		return fmt.Errorf("query 不能为空")
	}
	if len(c.Expected) == 0 {
		return fmt.Errorf("expected 不能为空")
	}
	if c.Surface == "" {
		return fmt.Errorf("surface 不能为空")
	}
	if !validSurfaces[c.Surface] {
		return fmt.Errorf("surface %q 非法", c.Surface)
	}
	return nil
}

// ----------------------------------------------------------------------------
// MemoryCaseStore 默认实现
// ----------------------------------------------------------------------------

// MemoryCaseStore 内存版 CaseStore，sync.RWMutex + map。
//
// 用途：测试与单机默认实现。二开可替换为 PG/Mongo 后端。
type MemoryCaseStore struct {
	mu    sync.RWMutex
	cases map[string]EvalCase
}

// NewMemoryCaseStore 构造 MemoryCaseStore。
func NewMemoryCaseStore() *MemoryCaseStore {
	return &MemoryCaseStore{cases: make(map[string]EvalCase)}
}

// Create 创建用例（ID 重复时返回错误）。
func (m *MemoryCaseStore) Create(_ context.Context, c EvalCase) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.cases[c.ID]; exists {
		return fmt.Errorf("用例 %q 已存在", c.ID)
	}
	m.cases[c.ID] = c
	return nil
}

// Get 按 ID 获取用例（不存在时返回错误）。
func (m *MemoryCaseStore) Get(_ context.Context, id string) (EvalCase, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.cases[id]
	if !ok {
		return EvalCase{}, fmt.Errorf("用例 %q 不存在", id)
	}
	return c, nil
}

// List 按过滤条件列出用例（支持 Surface/Channel/Intent + Limit/Offset）。
func (m *MemoryCaseStore) List(_ context.Context, filter CaseFilter) ([]EvalCase, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]EvalCase, 0, len(m.cases))
	for _, c := range m.cases {
		if filter.Surface != "" && c.Surface != filter.Surface {
			continue
		}
		if filter.Channel != "" && c.Channel != filter.Channel {
			continue
		}
		if filter.Intent != "" && c.Intent != filter.Intent {
			continue
		}
		out = append(out, c)
	}
	// Offset/Limit 分页。
	start := 0
	if filter.Offset > 0 && filter.Offset < len(out) {
		start = filter.Offset
	}
	out = out[start:]
	if filter.Limit > 0 && filter.Limit < len(out) {
		out = out[:filter.Limit]
	}
	return out, nil
}

// Update 更新用例（不存在时返回错误）。
func (m *MemoryCaseStore) Update(_ context.Context, c EvalCase) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.cases[c.ID]; !exists {
		return fmt.Errorf("用例 %q 不存在", c.ID)
	}
	// 保留原 CreatedAt。
	prev := m.cases[c.ID]
	c.CreatedAt = prev.CreatedAt
	m.cases[c.ID] = c
	return nil
}

// Delete 按 ID 删除用例（不存在时返回错误）。
func (m *MemoryCaseStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.cases[id]; !exists {
		return fmt.Errorf("用例 %q 不存在", id)
	}
	delete(m.cases, id)
	return nil
}
