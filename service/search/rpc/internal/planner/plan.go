// ============================================================================
// plan.go —— B2 查询计划的领域契约（工程方案 §3.1 planner 输出）。
//
// §3.1：planner 输出结构化计划
// `{rewritten_query, variants[K], stepback?, subqueries[](有界)}`，且
// "三档共用执行器，差异只在计划内容与 LLM 预算"。本文件把该契约落成
// Go 类型：Plan 是跨层传输的纯数据（JSON snake_case），Planner 是三档
// 规划器的统一接口（fast 恒等 / balanced 规则 / deep 分解，见
// identity.go / rule.go / decompose.go）。
// ============================================================================

package planner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// 档位预算契约（§3.1 查询策略分配表）：fast=0 次、balanced=1 次、
// deep=K 轮×1 次。Plan.LLMBudget 携带该预算，Validate 钉死。
const (
	// BudgetFast fast 档 LLM 预算：0 次（原查询直发，B1 fast 直通）。
	BudgetFast = 0
	// BudgetBalanced balanced 档 LLM 预算：1 次规划调用。
	BudgetBalanced = 1
)

// 计划构造与校验的错误契约（errors.Is 可判别）。
var (
	// ErrInvalidTier 标记未知检索档位（镜像 retrieval.ErrInvalidTier 口径）。
	ErrInvalidTier = errors.New("planner: 未知检索档位")
	// ErrTierMismatch 标记规划器与请求档位不匹配（如对恒等规划器请求 deep）。
	ErrTierMismatch = errors.New("planner: 规划器不服务该档位")
	// ErrEmptyQuery 标记查询为空（TrimSpace 后无内容）。
	ErrEmptyQuery = errors.New("planner: 查询为空")
	// ErrNoSubqueries 标记分解后无有效子查询（如查询仅由分隔符构成）。
	ErrNoSubqueries = errors.New("planner: 分解后无有效子查询")
)

// Plan 是一次查询规划的结构化计划。字段名对齐 §3.1 的 planner 输出
// （rewritten_query / variants / stepback / subqueries），另携带 Tier 与
// LLMBudget 两项执行元数据——三档共用执行器，靠它们区分执行口径。
type Plan struct {
	// RewrittenQuery 规范化后的原查询。§3.1 护栏"原查询永远保留为一路
	// 防漂移"：它是执行器必走的一路，变体只是附加路。
	RewrittenQuery string `json:"rewritten_query"`
	// Variants 改写/扩展变体（balanced 档多视角路；fast 恒等计划为空）。
	Variants []string `json:"variants,omitempty"`
	// Stepback step-back 泛化问句（§3.1 balanced ③"过细查询加 step-back
	// 变体"；可空——dev 规划器不产，真实化后由 LLM 规划器填）。
	Stepback *string `json:"stepback,omitempty"`
	// Subqueries 有界子查询列表（deep 档 K 轮的轮次来源；其余档为空）。
	Subqueries []string `json:"subqueries,omitempty"`
	// Tier 本计划的目标检索档位（retrieval.Tier：fast|balanced|deep）。
	Tier retrieval.Tier `json:"tier"`
	// LLMBudget 本计划声明的 LLM 调用预算：fast=0、balanced=1、deep=K
	// （K=len(Subqueries)，§3.1 deep 行"K 轮×1 次"）。
	LLMBudget int `json:"llm_budget"`
}

// Planner 查询规划器接口。三档共用同一签名，差异只在实现产出的计划
// 内容与 LLM 预算（§3.1）；实现必须纯域（不调模型、无 IO），真实化
// 路径见 README。
type Planner interface {
	// Plan 对 query 在指定档位下产出结构化计划。档位不匹配返回
	// ErrTierMismatch，未知档位返回 ErrInvalidTier，空查询返回
	// ErrEmptyQuery；ctx 取消时尽快返回 ctx.Err()。
	Plan(ctx context.Context, query string, tier retrieval.Tier) (Plan, error)
}

// Validate 出口契约校验：rewritten_query 非空、档位合法、LLM 预算与
// 档位契约一致（fast=0 / balanced=1 / deep=len(subqueries)≥1）、
// variants 与 subqueries 每项非空。跨层 JSON 反序列化后亦可作入口校验
// （先例：evidence.EvidencePack.Validate）。
func (p Plan) Validate() error {
	if strings.TrimSpace(p.RewrittenQuery) == "" {
		return fmt.Errorf("planner: 计划 rewritten_query 为空")
	}
	switch p.Tier {
	case retrieval.TierFast:
		if p.LLMBudget != BudgetFast {
			return fmt.Errorf("planner: fast 档预算必须为 %d，得到 %d", BudgetFast, p.LLMBudget)
		}
	case retrieval.TierBalanced:
		if p.LLMBudget != BudgetBalanced {
			return fmt.Errorf("planner: balanced 档预算必须为 %d，得到 %d", BudgetBalanced, p.LLMBudget)
		}
	case retrieval.TierDeep:
		if p.LLMBudget != len(p.Subqueries) || len(p.Subqueries) < 1 {
			return fmt.Errorf("planner: deep 档预算必须为 K=len(subqueries)≥1，得到 budget=%d、len=%d",
				p.LLMBudget, len(p.Subqueries))
		}
	default:
		return fmt.Errorf("%w: %q（合法值 fast|balanced|deep）", ErrInvalidTier, p.Tier)
	}
	for i, v := range p.Variants {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("planner: variants[%d] 为空", i)
		}
	}
	for i, s := range p.Subqueries {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("planner: subqueries[%d] 为空", i)
		}
	}
	return nil
}

// validTier 判断档位是否合法（供各规划器入口统一校验）。
func validTier(tier retrieval.Tier) bool {
	return tier == retrieval.TierFast || tier == retrieval.TierBalanced || tier == retrieval.TierDeep
}
