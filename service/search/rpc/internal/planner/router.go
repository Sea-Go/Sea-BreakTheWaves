// ============================================================================
// router.go —— 判档器（工程方案 §3.1 / 图 3 的"判档 [D-3]"）。
//
// §3.1：判档器蓝本是 Adaptive-RAG（arXiv:2403.14403：小 LM 分类器 +
// 自动标签，与 Sea 三档同构），且"档间只升不降，判错成本低"。本文件
// 是该小分类器的 **dev 规则替身**：基于长度与触发词/实体计数的确定性
// 规则，不调任何模型。真实化路径：用小 LM 分类器（或轻量 LLM 判档）
// 替换 Route 的规则体，接口不变（见 README）。
//
// dev 规则口径（长度按 rune 计——中文语境，不是字节数）：
//   deep     ：len>100，或含触发词 比较/分析/为什么
//   balanced ：len>30，或含多个实体（≥2 个实体性标记）
//   fast     ：其余
// "多个实体"的 dev 近似：ASCII 字母/数字词元（产品名、英文术语、代号）
// 与书名号《》各计 1，合计 ≥2 即视为多实体查询。
// ============================================================================

package planner

import (
	"context"
	"strings"
	"unicode"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// deepTriggers 是 deep 档触发词（比较/分析/因果类综合任务，§3.1）。
var deepTriggers = []string{"比较", "分析", "为什么"}

// routerEntityThreshold 多实体阈值：实体性标记计数 ≥ 该值判 balanced。
const routerEntityThreshold = 2

// Router 判档器：按查询复杂度给出检索档位建议。零值可用、无状态、
// 并发安全。
type Router struct{}

// NewRouter 构造判档器（零值即可用，构造函数仅为与检索层风格一致）。
func NewRouter() *Router {
	return &Router{}
}

// Route 返回查询的建议档位（fast|balanced|deep）。纯规则、无副作用、
// 不返回错误；ctx 取消时兜底返回 fast（最低成本档，档间只升不降由
// 调用方经 MaxTier 合并保证）。空查询判 fast（下游规划器会对空查询
// 显式报错，判档层不重复拦截）。
func (r *Router) Route(ctx context.Context, query string) retrieval.Tier {
	if ctx.Err() != nil {
		return retrieval.TierFast
	}
	if containsAny(query, deepTriggers) || runeLen(query) > 100 {
		return retrieval.TierDeep
	}
	if runeLen(query) > 30 || countEntities(query) >= routerEntityThreshold {
		return retrieval.TierBalanced
	}
	return retrieval.TierFast
}

// tierRank 档位秩（fast<balanced<deep）；未知档位视为最低，由调用方
// 先行校验（retrieval 侧会对未知档位报 ErrInvalidTier）。
func tierRank(t retrieval.Tier) int {
	switch t {
	case retrieval.TierDeep:
		return 2
	case retrieval.TierBalanced:
		return 1
	default:
		return 0
	}
}

// MaxTier 档间只升不降（§3.1）：取两档中更高者。调用方把用户显式指定
// 档与 Route 建议档合并时必须用它，保证判错只多花预算、不丢质量。
func MaxTier(a, b retrieval.Tier) retrieval.Tier {
	if tierRank(b) > tierRank(a) {
		return b
	}
	return a
}

// runeLen 按 rune 计长（中文语境的"字符数"）。
func runeLen(s string) int {
	return len([]rune(s))
}

// containsAny 报告 s 是否含列表中任一子串。
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// countEntities 计实体性标记数（dev 近似）：ASCII 字母/数字词元
// （产品名、英文术语、代号；中文视作分隔符不计）+ 书名号《》个数。
func countEntities(query string) int {
	count := strings.Count(query, "《")
	count += len(strings.FieldsFunc(query, func(r rune) bool {
		return !((unicode.IsLetter(r) && r < 0x80) || unicode.IsDigit(r))
	}))
	return count
}
