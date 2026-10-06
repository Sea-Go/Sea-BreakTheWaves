// ============================================================================
// identity.go —— 恒等规划器（fast 档；B1 fast 直通的实现）。
//
// §3.1 fast 行：0 次 LLM、原查询直发 dense+sparse 两路（跳 multi）。
// 因此 fast 档的计划是恒等计划——rewritten_query 原样透传，不带任何
// 变体/子查询，LLM 预算为 0。B1 网关在 fast 档下可整体跳过 B2，等价
// 于执行本规划器的输出。
// ============================================================================

package planner

import (
	"context"
	"fmt"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// Identity 恒等规划器：只服务 fast 档，产出恒等计划。
// 零值可用，无状态、并发安全。
type Identity struct{}

// Plan 产出 fast 档恒等计划：RewrittenQuery=query 原样（不 TrimSpace，
// 仅拒绝全空白查询）、Variants/Subqueries/Stepback 全空、LLMBudget=0。
func (p Identity) Plan(ctx context.Context, query string, tier retrieval.Tier) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if !validTier(tier) {
		return Plan{}, fmt.Errorf("%w: %q（合法值 fast|balanced|deep）", ErrInvalidTier, tier)
	}
	if tier != retrieval.TierFast {
		return Plan{}, fmt.Errorf("%w: 恒等规划器只服务 fast 档，收到 %q", ErrTierMismatch, tier)
	}
	if strings.TrimSpace(query) == "" {
		return Plan{}, ErrEmptyQuery
	}
	return Plan{
		RewrittenQuery: query,
		Tier:           retrieval.TierFast,
		LLMBudget:      BudgetFast,
	}, nil
}
