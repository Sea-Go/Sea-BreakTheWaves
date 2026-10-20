// ============================================================================
// decompose.go —— 子查询分解器（deep 档的 dev 形态）。
//
// §3.1 deep 行：self-ask 式有界子查询队列逐轮检索-重规划，预算
// K 轮×1 次。本分解器是它的确定性规则替身：按分号/问号/连接词把
// 复合查询拆为有界子查询（≤5），每个子查询经 Delegate（dev：规则
// 规划器）递归产一个"1 次规划"口径的子 Plan——正对应 K 轮×1 次；
// 顶层计划只携带子查询列表与总预算 K=len(subqueries)。
//
// dev 口径声明：
//   - 切分不做分词：连接词按子串匹配，可能误切合成词（"和平"被"和"
//     切开），dev 接受；真实化路径是 LLM 分解（self-ask），见 README。
//   - 有界性：非空子查询超过 MaxSubqueries 时取前 5 段、丢弃尾部
//     （保延迟预算优先；真实化由 LLM 规划器合并/摘要尾部）。
//   - 子 Plan 的 Tier 记 balanced：一轮 deep 检索的规划口径与 balanced
//     相同（1 次规划调用），执行档位以顶层计划的 Tier=deep 为准。
// ============================================================================

package planner

import (
	"context"
	"fmt"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// MaxSubqueries 是子查询的有界上限（§3.1 subqueries[](有界)；dev 取 5）。
const MaxSubqueries = 5

// sentenceSeps 句界分隔：分号（中/英）与问号（中/英）。
const sentenceSeps = "；;？?"

// connectives 连接词切分表（dev 写死小表；无分词，子串匹配）。
var connectives = []string{"以及", "并且", "还有", "然后", "同时", "另外", "和", "与"}

// Decomposer 子查询分解器：只服务 deep 档。Delegate 为每个子查询递归
// 产子 Plan 的规划器；nil 时默认用 Rule（dev 形态）。零值可用。
type Decomposer struct {
	Delegate Planner
}

// Plan 产出 deep 档顶层计划：RewrittenQuery=TrimSpace 后的原查询（伞
// 查询，执行器可作全局一路）、Subqueries=各子查询（经 Delegate 规划后
// 的 rewritten_query）、LLMBudget=K=len(Subqueries)、Variants/Stepback
// 为空（变体在各子 Plan 内，经 SubPlans 取用）。
func (d *Decomposer) Plan(ctx context.Context, query string, tier retrieval.Tier) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if !validTier(tier) {
		return Plan{}, fmt.Errorf("%w: %q（合法值 fast|balanced|deep）", ErrInvalidTier, tier)
	}
	if tier != retrieval.TierDeep {
		return Plan{}, fmt.Errorf("%w: 分解器只服务 deep 档，收到 %q", ErrTierMismatch, tier)
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return Plan{}, ErrEmptyQuery
	}
	subPlans, err := d.SubPlans(ctx, query)
	if err != nil {
		return Plan{}, err
	}
	subs := make([]string, len(subPlans))
	for i, sp := range subPlans {
		subs[i] = sp.RewrittenQuery
	}
	return Plan{
		RewrittenQuery: trimmed,
		Subqueries:     subs,
		Tier:           retrieval.TierDeep,
		LLMBudget:      len(subs),
	}, nil
}

// SubPlans 为每个子查询递归调 Delegate 产子 Plan（每轮 1 次规划口径，
// §3.1 "K 轮×1 次"）。deep 执行器（B3 装配）逐轮取用：第 i 轮用
// subPlans[i] 的 rewritten_query + variants 走三路检索。
func (d *Decomposer) SubPlans(ctx context.Context, query string) ([]Plan, error) {
	delegate := d.Delegate
	if delegate == nil {
		delegate = Rule{}
	}
	subs := SplitSubqueries(query)
	if len(subs) == 0 {
		return nil, ErrNoSubqueries
	}
	plans := make([]Plan, 0, len(subs))
	for _, s := range subs {
		sp, err := delegate.Plan(ctx, s, retrieval.TierBalanced)
		if err != nil {
			return nil, fmt.Errorf("planner: 子查询 %q 规划失败: %w", s, err)
		}
		plans = append(plans, sp)
	}
	return plans, nil
}

// SplitSubqueries 把查询切分为有界子查询：先按句界（分号/问号）切，
// 再按连接词表逐词细分；每段 TrimSpace、剔除空段；超过
// MaxSubqueries 时取前 MaxSubqueries 段（dev 口径，见文件头）。
// 无有效子查询时返回 nil。
func SplitSubqueries(query string) []string {
	pieces := strings.FieldsFunc(query, func(r rune) bool {
		return strings.ContainsRune(sentenceSeps, r)
	})
	for _, conn := range connectives {
		next := make([]string, 0, len(pieces))
		for _, p := range pieces {
			next = append(next, strings.Split(p, conn)...)
		}
		pieces = next
	}
	var subs []string
	for _, p := range pieces {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		if len(subs) == MaxSubqueries {
			break
		}
		subs = append(subs, t)
	}
	return subs
}
