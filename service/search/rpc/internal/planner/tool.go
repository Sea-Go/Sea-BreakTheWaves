// graph.go —— B2 查询规划的 tRPC-Agent-Go 装配层（C17）。
//
// 职责边界（沿 service/search/rpc/internal/trpcagent 的既有惯例）：
//   - 本文件是**框架装配层**：把 planner 包的纯域规划器（Identity/Rule/
//     Decomposer/Router）暴露为框架 **Tool**，供框架 Agent 在推理中调用；
//   - planner 包的其余文件是**算法内核**：纯函数、无 IO、无框架依赖；
//   - 本层不选模型、不建会话——模型与会话由调用方经框架注入（C31：模型
//     调用统一走 DC）。
//
// 为什么需要这层：C17 要求 BTW 的 Agent、工作流、工具、会话及检索装配以
// 框架公开 API 为基础。规划算法本身是纯函数（不依赖框架），但"把规划作为
// 能力暴露给 Agent"必须经框架 Tool 接口。
package planner

import (
	"context"
	"fmt"

	trpctool "trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// PlanToolRequest 是规划 Tool 的入参（wire 形态，snake_case）。
type PlanToolRequest struct {
	// Query 用户原始查询。
	Query string `json:"query"`
	// Tier 目标档位（fast|balanced|deep）；留空时由 Route 建议档决定。
	Tier string `json:"tier"`
}

// PlanToolResponse 是规划 Tool 的出参：结构化计划 + 实际生效档位 + 路由建议。
// 三档语义只在 Plan 与 effective_tier 表达，调用方不重复解释。
type PlanToolResponse struct {
	RewrittenQuery string   `json:"rewritten_query"`
	Variants       []string `json:"variants,omitempty"`
	Stepback       string   `json:"stepback,omitempty"`
	Subqueries     []string `json:"subqueries,omitempty"`
	Tier           string   `json:"tier"`
	LLMBudget      int      `json:"llm_budget"`
	// SuggestedTier 是判档器给出的建议档；EffectiveTier 是 MaxTier 合并后的
	// 实际执行档（§3.1「档间只升不降」）。
	SuggestedTier string `json:"suggested_tier"`
	EffectiveTier string `json:"effective_tier"`
}

// PlannerToolDeps 是规划 Tool 的注入依赖：三档规划器各一个（可缺省）、
// 一个判档器。缺省的档位在请求该档时返回明确错误，不静默降级。
type PlannerToolDeps struct {
	Fast     Planner
	Balanced Planner
	Deep     Planner
	Router   *Router
}

// NewPlanTool 把纯域规划器适配成框架 Tool，Agent 可在推理中调用它获得
// 结构化计划。工具名 sea_query_plan 与 §3.1 的判档/规划职责对应。
func NewPlanTool(deps PlannerToolDeps) (trpctool.Tool, error) {
	if deps.Fast == nil && deps.Balanced == nil && deps.Deep == nil {
		return nil, fmt.Errorf("planner tool: at least one tier planner is required")
	}
	router := deps.Router
	if router == nil {
		router = NewRouter()
	}
	return function.NewFunctionTool(
		func(ctx context.Context, in PlanToolRequest) (PlanToolResponse, error) {
			if in.Query == "" {
				return PlanToolResponse{}, fmt.Errorf("query is required")
			}
			suggested := router.Route(ctx, in.Query)
			effective := suggested
			if in.Tier != "" {
				requested := retrieval.Tier(in.Tier)
				switch requested {
				case retrieval.TierFast, retrieval.TierBalanced, retrieval.TierDeep:
					// §3.1：档间只升不降——调用方请求与建议档取高者。
					effective = MaxTier(requested, suggested)
				default:
					return PlanToolResponse{}, fmt.Errorf("unknown tier %q", in.Tier)
				}
			}
			planner, err := deps.forTier(effective)
			if err != nil {
				return PlanToolResponse{}, err
			}
			plan, err := planner.Plan(ctx, in.Query, effective)
			if err != nil {
				return PlanToolResponse{}, fmt.Errorf("plan %s: %w", effective, err)
			}
			out := PlanToolResponse{
				RewrittenQuery: plan.RewrittenQuery, Variants: plan.Variants,
				Subqueries: plan.Subqueries, Tier: string(plan.Tier),
				LLMBudget: plan.LLMBudget, SuggestedTier: string(suggested),
				EffectiveTier: string(effective),
			}
			if plan.Stepback != nil {
				out.Stepback = *plan.Stepback
			}
			return out, nil
		},
		function.WithName("sea_query_plan"),
		function.WithDescription(
			"Plan a whole-document retrieval query: returns the rewritten query, "+
				"optional variants/subqueries and the LLM budget for the effective tier."),
	), nil
}

// forTier 取该档的规划器；缺省时明确报错而非降级。
func (d PlannerToolDeps) forTier(tier retrieval.Tier) (Planner, error) {
	switch tier {
	case retrieval.TierFast:
		if d.Fast != nil {
			return d.Fast, nil
		}
	case retrieval.TierBalanced:
		if d.Balanced != nil {
			return d.Balanced, nil
		}
	case retrieval.TierDeep:
		if d.Deep != nil {
			return d.Deep, nil
		}
	default:
		return nil, fmt.Errorf("planner tool: unknown tier %q", tier)
	}
	return nil, fmt.Errorf("planner tool: no planner configured for tier %q", tier)
}

// NewDefaultPlanTool 装配三档默认规划器（恒等/规则/分解）的规划 Tool。
func NewDefaultPlanTool() (trpctool.Tool, error) {
	return NewPlanTool(PlannerToolDeps{
		Fast:     Identity{},
		Balanced: Rule{},
		Deep:     &Decomposer{Delegate: Rule{}},
		Router:   NewRouter(),
	})
}
