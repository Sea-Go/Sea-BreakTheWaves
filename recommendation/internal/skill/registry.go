// ============================================================================
// 该文件实现 SkillRegistry interface（定义于 domain 包）。
//
// Registry 是 Skill 注册中心，线程安全（sync.RWMutex 保护），管理 Skill
// 清单。对外暴露 domain.Skill（满足 domain.SkillRegistry 契约），内部存储
// 扩展 Skill 类型（含 Category 与 Execute）。
//
// 技能内容物化到 tool result（不污染 Prompt Cache 前缀）：Skill 执行时
// 返回 content 作为 tool result，而非注入 prompt。
//
// 二开扩展点：
//   - 注册自定义 Skill：通过 RegisterSkill 注入含 Execute 的实现
//   - 替换 Skill：注册同名 Skill 覆盖（需先注销，重复注册报错）
// ============================================================================

package skill

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"sea/internal/domain"
)

// 编译期断言：Registry 实现 domain.SkillRegistry interface。
var _ domain.SkillRegistry = (*Registry)(nil)

// Registry Skill 注册中心，实现 domain.SkillRegistry interface。
//
// 线程安全，并发安全读写。内部以 map[string]Skill 存储扩展 Skill
// （含 Category 与 Execute），对外通过 domain.Skill 暴露。
type Registry struct {
	// mu 读写锁，保护 skills 并发访问。
	mu sync.RWMutex
	// skills Skill 存储表，键为 Skill.Name。
	skills map[string]Skill
}

// NewRegistry 创建空的 Skill 注册中心。
func NewRegistry() *Registry {
	return &Registry{
		skills: make(map[string]Skill),
	}
}

// Register 注册 Skill（实现 domain.SkillRegistry interface）。
//
// 接收 domain.Skill（不含 Category 与 Execute），转为内部 Skill 后注册。
// name 唯一性校验：重复注册同名 Skill 返回错误。
//
// 二开点：若需注入 Category 或 Execute，请使用 RegisterSkill。
func (r *Registry) Register(s domain.Skill) error {
	return r.RegisterSkill(Skill{Skill: s})
}

// RegisterSkill 注册扩展 Skill（含 Category 与 Execute），二开点。
//
// name 唯一性校验：重复注册同名 Skill 返回错误。
// 二开方通过该方法注入自定义执行逻辑（Execute）。
func (r *Registry) RegisterSkill(s Skill) error {
	if s.Name == "" {
		return errors.New("skill: Skill 名称不能为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.skills[s.Name]; exists {
		return fmt.Errorf("skill: Skill 已注册，名称冲突: %s", s.Name)
	}
	r.skills[s.Name] = s
	return nil
}

// Get 获取 Skill（实现 domain.SkillRegistry interface）。
//
// name Skill 名称。返回 domain.Skill 与 error（未找到时返回错误）。
func (r *Registry) Get(name string) (domain.Skill, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.skills[name]
	if !ok {
		return domain.Skill{}, fmt.Errorf("skill: Skill 未找到: %s", name)
	}
	return s.Skill, nil
}

// List 列出所有 Skill（实现 domain.SkillRegistry interface）。
//
// 返回所有已注册 Skill 的 domain.Skill 列表。
func (r *Registry) List() []domain.Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]domain.Skill, 0, len(r.skills))
	for _, s := range r.skills {
		result = append(result, s.Skill)
	}
	return result
}

// Invoke 调用 Skill（实现 domain.SkillRegistry interface）。
//
// ctx 上下文；name Skill 名称；input 调用输入。
// 若 Skill 含 Execute 函数则调用之；否则将 Description 物化为 tool result
// 返回（不污染 Prompt Cache 前缀）。
func (r *Registry) Invoke(ctx context.Context, name string, input domain.SkillInput) (domain.SkillOutput, error) {
	r.mu.RLock()
	s, ok := r.skills[name]
	r.mu.RUnlock()
	if !ok {
		return domain.SkillOutput{}, fmt.Errorf("skill: Skill 未找到: %s", name)
	}
	// 二开点：若注入了 Execute 函数则调用之。
	if s.Execute != nil {
		return s.Execute(ctx, input)
	}
	// 默认行为：将 Description 物化为 tool result（不污染 Prompt Cache 前缀）。
	return domain.SkillOutput{Result: s.Description}, nil
}
