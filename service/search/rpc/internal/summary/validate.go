// ============================================================================
// SummaryResult 出口校验：B6 交付前的最后一道闸。
//
// 约束（口径见 C-10 链路 B6 出口契约）：
//   - query_id 非空（链路对账键）；
//   - answer 非空且 ≤ MaxAnswerRunes(4000) rune；
//   - citations 数量在 [1, MaxCitations](1..8)，Index 唯一且落在
//     1..MaxCitations；
//   - 每条 citation 的 DocKey 非空、quote 非空且 ≤ 200 rune
//     （与 evidence.MaxQuoteRunes 同口径）；
//   - 每条 citation 的 Index 必须在 answer 内以 [n] 角标形式至少出现
//     一次——角标与引用列表对不上即为断链，宁可整单拒绝。
//
// 角标识别用完整 token（`\[\d+\]`）而非子串包含，避免 [10] 误判出
// [1]、[1]0 误判出 [0]（见 markerRe 与 format.go 的同一套识别规则）。
// ============================================================================

package summary

import (
	"fmt"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
)

// markerRe 匹配答案内的行内引用角标：一个完整的 [数字] token。
// 与 format.go 的 FormatAnswer 共用同一识别规则，改一处必须同步另一处。
var markerRe = regexp.MustCompile(`\[(\d+)\]`)

// Validate 校验 SummaryResult 是否满足 B6 出口契约。stub 与真实实现
// 都必须在返回前调用它；上游收到跨层结果后也可用它做入口校验。
func (r SummaryResult) Validate() error {
	if r.QueryID == "" {
		return fmt.Errorf("summary: query_id 不能为空")
	}
	n := utf8.RuneCountInString(r.Answer)
	if n == 0 {
		return fmt.Errorf("summary: answer 不能为空")
	}
	if n > MaxAnswerRunes {
		return fmt.Errorf("summary: answer 长度 %d rune 超出上限 %d", n, MaxAnswerRunes)
	}
	if len(r.Citations) < 1 {
		return fmt.Errorf("summary: citations 为空（需 1..%d 条）", MaxCitations)
	}
	if len(r.Citations) > MaxCitations {
		return fmt.Errorf("summary: citations %d 条超出上限 %d", len(r.Citations), MaxCitations)
	}

	counts := countMarkers(r.Answer)
	seen := make(map[int]bool, len(r.Citations))
	for i, c := range r.Citations {
		if c.DocKey == "" {
			return fmt.Errorf("summary: 引用[%d] doc_key 不能为空", i)
		}
		if c.Index < 1 || c.Index > MaxCitations {
			return fmt.Errorf("summary: 引用[%d] 角标 %d 超出 1..%d", i, c.Index, MaxCitations)
		}
		if seen[c.Index] {
			return fmt.Errorf("summary: 引用[%d] 角标 %d 重复", i, c.Index)
		}
		seen[c.Index] = true
		q := utf8.RuneCountInString(c.Locator.Quote)
		if q == 0 {
			return fmt.Errorf("summary: 引用[%d] %s 的 quote 为空", i, c.DocKey)
		}
		if q > evidence.MaxQuoteRunes {
			return fmt.Errorf("summary: 引用[%d] %s 的 quote 长度 %d rune 超出上限 %d", i, c.DocKey, q, evidence.MaxQuoteRunes)
		}
		if counts[c.Index] < 1 {
			return fmt.Errorf("summary: 引用[%d] 角标 [%d] 未在 answer 中出现（至少一次）", i, c.Index)
		}
	}
	return nil
}

// countMarkers 统计 answer 内每个行内角标 token 的出现次数：只认完整
// 的 [数字]，[10] 不会计入 [1]/[0]，[1]0 会计入 [1]。
func countMarkers(answer string) map[int]int {
	counts := make(map[int]int)
	for _, m := range markerRe.FindAllStringSubmatch(answer, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue // 正则保证是纯数字，理论不可达
		}
		counts[n]++
	}
	return counts
}
