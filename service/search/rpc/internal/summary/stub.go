// ============================================================================
// StubSummarizer：确定性摘要器（dev 形态，不调模型）。
//
// 交付形态完全由证据包推导，保证同输入同输出（可缓存、可重放、测试
// 可断言）：
//
//	答案 = "根据 N 篇文档……" + 按 RRF 降序前 3 候选的首条 quote 拼接
//	       （每条截 200 rune，行内引用角标 [1][2][3] 与候选序对应）
//	citations = 同样前 3 候选，各取其 evidence 首条 Locator
//
// 它是 Summarizer 接口的 dev 替身：真实实现（经 D1 dc-gateway 调模型）
// 替换本实现时，接口、出口校验与格式化层不变（见 README"真实化路径"）。
// ============================================================================

package summary

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
)

// StubSummarizer 是 dev 形态的确定性摘要器。零值可用、无状态、并发安全。
type StubSummarizer struct{}

// NewStub 返回一个 dev 形态的确定性摘要器。
func NewStub() StubSummarizer { return StubSummarizer{} }

// Summarize 按 dev 口径产出答案与引用：
//
//   - 入口校验：ctx 未取消；query_id 可解析且与包一致；pack 通过
//     EvidencePack.Validate；空包（0 候选）直接拒绝——绝不产出
//     无证据答案；
//   - 候选按 RRFScore 降序、平局按 doc_key 字典序（与 BuildPack 同
//     规则），取前 StubTopCandidates(3) 个；
//   - 每个入选候选取首条 evidence 的 quote（截 200 rune）拼进答案，
//     行内角标 [i] 与候选序一致；citations 同序取该条 Locator；
//   - 出口前跑 SummaryResult.Validate 把关。
//
// 返回错误时结果为零值（全有或全无）。
func (StubSummarizer) Summarize(ctx context.Context, req SummaryRequest) (SummaryResult, error) {
	if err := ctx.Err(); err != nil {
		return SummaryResult{}, fmt.Errorf("summary: 已取消: %w", err)
	}
	qid, err := resolveQueryID(req)
	if err != nil {
		return SummaryResult{}, err
	}
	if err := req.Pack.Validate(); err != nil {
		return SummaryResult{}, fmt.Errorf("summary: 入口校验证据包失败: %w", err)
	}
	if len(req.Pack.Candidates) == 0 {
		return SummaryResult{}, fmt.Errorf("summary: 证据包无候选，拒绝产出无证据摘要")
	}

	top := topCandidates(req.Pack.Candidates, StubTopCandidates)
	parts := make([]string, 0, len(top)+1)
	parts = append(parts, fmt.Sprintf("根据 %d 篇文档……", len(req.Pack.Candidates)))
	citations := make([]Citation, 0, len(top))
	for i, c := range top {
		// pack 已过 Validate（evidence ≥1 条、quote ≤200 rune），
		// 截断只是双保险。
		loc := c.Evidence[0]
		loc.Quote = truncateRunes(loc.Quote, evidence.MaxQuoteRunes)
		parts = append(parts, fmt.Sprintf("「%s」[%d]", loc.Quote, i+1))
		citations = append(citations, Citation{Index: i + 1, DocKey: c.DocKey, Locator: loc})
	}

	result := SummaryResult{
		QueryID:   qid,
		Answer:    strings.Join(parts, "\n"),
		Citations: citations,
	}
	if err := result.Validate(); err != nil {
		return SummaryResult{}, err
	}
	return result, nil
}

// resolveQueryID 解析请求与证据包的 query_id：两者都非空且不相等视为
// 链路错位（拒绝）；否则取非空者（请求侧优先）。
func resolveQueryID(req SummaryRequest) (string, error) {
	if req.QueryID != "" && req.Pack.QueryID != "" && req.QueryID != req.Pack.QueryID {
		return "", fmt.Errorf("summary: 请求 query_id %q 与证据包 query_id %q 不一致", req.QueryID, req.Pack.QueryID)
	}
	if req.QueryID != "" {
		return req.QueryID, nil
	}
	return req.Pack.QueryID, nil
}

// topCandidates 把候选按 RRFScore 降序、平局按 doc_key 字典序稳定排序
// 后取前 k 个。不改动传入切片（拷贝后排序，保持纯函数）。
func topCandidates(cands []evidence.EvidenceCandidate, k int) []evidence.EvidenceCandidate {
	sorted := slices.Clone(cands)
	slices.SortStableFunc(sorted, func(a, b evidence.EvidenceCandidate) int {
		if a.RRFScore != b.RRFScore {
			if a.RRFScore > b.RRFScore {
				return -1
			}
			return 1
		}
		return strings.Compare(a.DocKey, b.DocKey)
	})
	if len(sorted) > k {
		sorted = sorted[:k]
	}
	return sorted
}

// truncateRunes 把 s 截到至多 max 个 rune（合法 UTF-8 边界内截断）。
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
