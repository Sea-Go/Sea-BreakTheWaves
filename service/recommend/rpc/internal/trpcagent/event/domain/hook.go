package event

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现 Hook interface 体系与 Registry（Hook 注册表）。
// 设计要点：
//   - Hook 基础 interface 与 domain.Hook 同构（type alias），确保 Registry
//     可同时满足 domain.HookRegistry 与 domain.EventEmitter interface
//   - 按 Agent 推荐链路细分 8 个扩展 Hook interface（RecommendHook/ToolCallHook/
//     LLMCallHook/QualityJudgeHook/SearchHook/RerankHook/GraphQueryHook/SkillInvokeHook），
//     二开方可选择性实现，EmitTyped 按事件类型做类型断言分发
//   - Registry 顺序调用 Hook，错误隔离（单个 Hook 失败不影响其他 Hook），
//     聚合错误通过 PartialHookErrors 返回
// 所有代码仅依赖标准库与 domain 包，确保离线可编译。
// ============================================================================

// ----------------------------------------------------------------------------
// Hook interface 体系
// ----------------------------------------------------------------------------

// Hook 行为事件 Hook 基础 interface（与 domain.Hook 同构，type alias）。
// 通过 type alias 使 event.Registry.Register(h Hook) 与 domain.HookRegistry.Register(hook domain.Hook)
// 签名一致，从而 Registry 可直接满足 domain.HookRegistry interface。
//
// 二开扩展点：实现该 interface 并通过 Registry.Register 注入；
// 错误隔离不影响主流程与其他 Hook。
type Hook = domain.Hook

// RecommendEvent 推荐事件，BehaviorEvent 的类型别名。
// 用于 RecommendHook.OnRecommend 类型分发，承载快/慢/混合路径、曝光、点击、
// 点赞、dislike、收藏、完成阅读、Agent 结束等推荐主流程事件。
type RecommendEvent = domain.BehaviorEvent

// ToolCallEvent 工具调用事件，BehaviorEvent 的类型别名。
type ToolCallEvent = domain.BehaviorEvent

// LLMCallEvent LLM 调用事件，BehaviorEvent 的类型别名。
type LLMCallEvent = domain.BehaviorEvent

// QualityEvent 质量评判事件，BehaviorEvent 的类型别名。
type QualityEvent = domain.BehaviorEvent

// SearchEvent 搜索事件，BehaviorEvent 的类型别名。
type SearchEvent = domain.BehaviorEvent

// RerankEvent 重排事件，BehaviorEvent 的类型别名。
type RerankEvent = domain.BehaviorEvent

// GraphEvent 图谱查询事件，BehaviorEvent 的类型别名。
type GraphEvent = domain.BehaviorEvent

// SkillEvent Skill 调用事件，BehaviorEvent 的类型别名。
type SkillEvent = domain.BehaviorEvent

// RecommendHook 推荐主流程 Hook，二开可选择性实现 OnRecommend。
// 触发事件：fast_path_hit/slow_path_hit/hybrid_merge/impression/click/like/
// dislike/favorite/read_complete/agent_end。
type RecommendHook interface {
	Hook
	OnRecommend(ctx context.Context, e RecommendEvent) error
}

// ToolCallHook 工具调用 Hook，二开可选择性实现 OnToolCall。
// 触发事件：tool_call。
type ToolCallHook interface {
	Hook
	OnToolCall(ctx context.Context, e ToolCallEvent) error
}

// LLMCallHook LLM 调用 Hook，二开可选择性实现 OnLLMCall。
// 触发事件：llm_call。
type LLMCallHook interface {
	Hook
	OnLLMCall(ctx context.Context, e LLMCallEvent) error
}

// QualityJudgeHook 质量评判 Hook，二开可选择性实现 OnQualityJudge。
// 触发事件：quality_judge。
type QualityJudgeHook interface {
	Hook
	OnQualityJudge(ctx context.Context, e QualityEvent) error
}

// SearchHook 搜索 Hook，二开可选择性实现 OnSearch。
// 说明：当前事件常量未单独定义 search 事件类型，SearchHook 暂不参与
// EmitTyped 自动分发；二开方可通过 OnEvent 内自行判断或后续扩展
// EventSearch 常量后接入分发。
type SearchHook interface {
	Hook
	OnSearch(ctx context.Context, e SearchEvent) error
}

// RerankHook 重排 Hook，二开可选择性实现 OnRerank。
// 触发事件：rerank。
type RerankHook interface {
	Hook
	OnRerank(ctx context.Context, e RerankEvent) error
}

// GraphQueryHook 图谱查询 Hook，二开可选择性实现 OnGraphQuery。
// 触发事件：graph_query/cypher_generated。
type GraphQueryHook interface {
	Hook
	OnGraphQuery(ctx context.Context, e GraphEvent) error
}

// SkillInvokeHook Skill 调用 Hook，二开可选择性实现 OnSkillInvoke。
// 触发事件：skill_invoked。
type SkillInvokeHook interface {
	Hook
	OnSkillInvoke(ctx context.Context, e SkillEvent) error
}

// ----------------------------------------------------------------------------
// Registry Hook 注册表
// ----------------------------------------------------------------------------

// Registry Hook 注册表，顺序调用 Hook 并做错误隔离。
// 同时实现 domain.HookRegistry（Register + Invoke）与 domain.EventEmitter（Emit）。
//
// 二开扩展点：
//   - 通过 NewRegistry 创建实例
//   - 通过 Register 注册自定义 Hook（基础 Hook 或扩展 Hook）
//   - 通过 Emit 发射事件（触发所有 Hook 的 OnEvent）
//   - 通过 EmitTyped 发射事件并按事件类型分发到扩展 Hook 方法（OnToolCall 等）
type Registry struct {
	mu    sync.RWMutex
	hooks []Hook
}

// NewRegistry 创建 Hook 注册表。
func NewRegistry() *Registry {
	return &Registry{
		hooks: make([]Hook, 0),
	}
}

// Register 注册 Hook。
// 返回 error 以满足 domain.HookRegistry.Register(hook Hook) error interface 签名；
// 当前实现不会失败（始终返回 nil），保留 error 返回值便于二开方覆盖注册校验逻辑
// （如重名检查、Hook 数量上限等）。
//
// 二开扩展点：可在二开实现中重写此方法，加入 Hook 名称唯一性校验。
func (r *Registry) Register(h Hook) error {
	if h == nil {
		return errors.New("event: register nil hook")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = append(r.hooks, h)
	return nil
}

// Emit 顺序调用所有 Hook 的 OnEvent，错误隔离。
// 单个 Hook 失败不影响其他 Hook 执行，所有错误聚合为 PartialHookErrors 返回。
// 若全部 Hook 成功，返回 nil。
//
// 该方法使 Registry 满足 domain.EventEmitter interface。
func (r *Registry) Emit(ctx context.Context, e domain.BehaviorEvent) error {
	hooks := r.snapshot()
	var errs []error
	for _, h := range hooks {
		if err := h.OnEvent(ctx, e); err != nil {
			errs = append(errs, fmt.Errorf("hook %q OnEvent: %w", h.Name(), err))
		}
	}
	return wrapErrors(errs)
}

// EmitTyped 根据事件类型调用对应的扩展 interface 方法（用类型断言分发）。
// 对实现了对应扩展 interface 的 Hook，调用其类型化方法（如 OnToolCall）；
// 对未实现对应扩展 interface 的 Hook，回退调用 OnEvent，确保每个 Hook 都被触发一次。
// 错误隔离：单个 Hook 失败不影响其他 Hook，聚合为 PartialHookErrors 返回。
//
// 事件类型 → 扩展 interface 分发映射：
//   - tool_call                → ToolCallHook.OnToolCall
//   - llm_call                 → LLMCallHook.OnLLMCall
//   - quality_judge            → QualityJudgeHook.OnQualityJudge
//   - rerank                   → RerankHook.OnRerank
//   - graph_query/cypher_generated → GraphQueryHook.OnGraphQuery
//   - skill_invoked            → SkillInvokeHook.OnSkillInvoke
//   - fast_path_hit/slow_path_hit/hybrid_merge/impression/click/like/
//     dislike/favorite/read_complete/agent_end → RecommendHook.OnRecommend
//   - 其他事件类型              → 仅调用 OnEvent
func (r *Registry) EmitTyped(ctx context.Context, e domain.BehaviorEvent) error {
	hooks := r.snapshot()
	var errs []error
	for _, h := range hooks {
		if err := r.dispatch(ctx, h, e); err != nil {
			errs = append(errs, err)
		}
	}
	return wrapErrors(errs)
}

// Invoke 顺序调用所有 Hook 处理事件（错误隔离），聚合错误返回。
// 该方法使 Registry 满足 domain.HookRegistry interface，内部委托给 Emit。
func (r *Registry) Invoke(ctx context.Context, event domain.BehaviorEvent) error {
	return r.Emit(ctx, event)
}

// snapshot 在读锁下拷贝当前 Hook 列表，避免遍历期间持锁阻塞 Register。
func (r *Registry) snapshot() []Hook {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Hook, len(r.hooks))
	copy(out, r.hooks)
	return out
}

// dispatch 对单个 Hook 按事件类型做类型断言分发。
// 若 Hook 实现了对应扩展 interface，调用类型化方法；否则回退调用 OnEvent。
// 返回的 error 已包含 Hook 名称上下文。
func (r *Registry) dispatch(ctx context.Context, h Hook, e domain.BehaviorEvent) error {
	switch e.EventType {
	case domain.EventToolCall:
		if hh, ok := h.(ToolCallHook); ok {
			if err := hh.OnToolCall(ctx, ToolCallEvent(e)); err != nil {
				return fmt.Errorf("hook %q OnToolCall: %w", h.Name(), err)
			}
			return nil
		}
	case domain.EventLLMCall:
		if hh, ok := h.(LLMCallHook); ok {
			if err := hh.OnLLMCall(ctx, LLMCallEvent(e)); err != nil {
				return fmt.Errorf("hook %q OnLLMCall: %w", h.Name(), err)
			}
			return nil
		}
	case domain.EventQualityJudge:
		if hh, ok := h.(QualityJudgeHook); ok {
			if err := hh.OnQualityJudge(ctx, QualityEvent(e)); err != nil {
				return fmt.Errorf("hook %q OnQualityJudge: %w", h.Name(), err)
			}
			return nil
		}
	case domain.EventRerank:
		if hh, ok := h.(RerankHook); ok {
			if err := hh.OnRerank(ctx, RerankEvent(e)); err != nil {
				return fmt.Errorf("hook %q OnRerank: %w", h.Name(), err)
			}
			return nil
		}
	case domain.EventGraphQuery, domain.EventCypherGenerated:
		if hh, ok := h.(GraphQueryHook); ok {
			if err := hh.OnGraphQuery(ctx, GraphEvent(e)); err != nil {
				return fmt.Errorf("hook %q OnGraphQuery: %w", h.Name(), err)
			}
			return nil
		}
	case domain.EventSkillInvoked:
		if hh, ok := h.(SkillInvokeHook); ok {
			if err := hh.OnSkillInvoke(ctx, SkillEvent(e)); err != nil {
				return fmt.Errorf("hook %q OnSkillInvoke: %w", h.Name(), err)
			}
			return nil
		}
	case domain.EventFastPathHit, domain.EventSlowPathHit, domain.EventHybridMerge,
		domain.EventImpression, domain.EventClick, domain.EventLike, domain.EventDislike,
		domain.EventFavorite, domain.EventReadComplete, domain.EventAgentEnd:
		if hh, ok := h.(RecommendHook); ok {
			if err := hh.OnRecommend(ctx, RecommendEvent(e)); err != nil {
				return fmt.Errorf("hook %q OnRecommend: %w", h.Name(), err)
			}
			return nil
		}
	}
	// 未匹配到扩展 interface，回退调用 OnEvent。
	if err := h.OnEvent(ctx, e); err != nil {
		return fmt.Errorf("hook %q OnEvent: %w", h.Name(), err)
	}
	return nil
}

// ----------------------------------------------------------------------------
// PartialHookErrors 聚合错误
// ----------------------------------------------------------------------------

// PartialHookErrors 聚合多个 Hook 的错误（错误隔离产物）。
// 实现 error interface，并暴露 Errors 切片与 Unwrap 便于 errors.Is/As 检索。
type PartialHookErrors struct {
	// Errors 收集的所有 Hook 错误（已含 Hook 名称上下文）。
	Errors []error
}

// Error 实现 error interface，输出错误数量与每条错误详情。
func (p PartialHookErrors) Error() string {
	if len(p.Errors) == 0 {
		return "event: partial hook errors (0)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "event: partial hook errors (%d):", len(p.Errors))
	for i, e := range p.Errors {
		fmt.Fprintf(&b, "\n  [%d] %s", i+1, e.Error())
	}
	return b.String()
}

// Unwrap 返回所有聚合错误，支持 errors.Is/errors.As 透传（Go 1.20+ 多错误 Unwrap）。
func (p PartialHookErrors) Unwrap() []error {
	return p.Errors
}

// wrapErrors 将错误切片封装为 PartialHookErrors；无错误时返回 nil。
func wrapErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return PartialHookErrors{Errors: errs}
}
