// Package summary 实现 B6 摘要交付的纯域层：把 B5 产出的 EvidencePack
// （C-10 契约）交付为"答案 + 行内引用角标 + 引用列表"。
//
// 本包属于 BTW（BreakTheWaves）搜索服务 B 域流水线的末段（图 3：
// B5 ──C-10──► B6 ──答案+引用──► A1 ──► 用户），定位是 summary 交付的
// 域契约与编排骨架：
//   - 契约类型（本文件）：SummaryRequest / SummaryResult / Citation，
//     以及 Summarizer 接口（真实实现经 D1 调模型，dev 形态为 stub.go
//     的确定性摘要器）；
//   - 出口校验（validate.go）：answer 非空 ≤4000 rune、citations 1..8、
//     角标与正文一致、quote ≤200 rune；
//   - 前端格式化（format.go）：[n] 角标 → Markdown 锚点链接 + 脚注式
//     引用列表（CitationCard 挂 #cit-n 锚点）。
//
// 边界（详见本目录 README.md）：
//   - 不调模型——stub.go 是 dev 替身，真实化路径是经 D1 dc-gateway 的
//     模型调用实现同一 Summarizer 接口；
//   - tools 交付不经此——B5 的 EvidencePack 在 tools 档直返 B1，本包
//     只消费 summary 档的包；
//   - 不做持久化与网络 IO（纯函数域层，可独立测试）。
package summary

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
)

// 出口契约常量。数值为 C-10 链路 B6 出口的口径，改动需同步 README 与
// 前端 CitationCard 渲染。
const (
	// MaxAnswerRunes 答案长度上限（rune 计）。
	MaxAnswerRunes = 4000

	// MaxCitations 单次摘要允许的引用条数上限；下限为 1（无引用的
	// summary 交付没有证据支撑，应直接拒绝而非放行）。
	MaxCitations = 8

	// StubTopCandidates dev stub 引用的候选文档数（按 RRF 降序取前 N）。
	StubTopCandidates = 3
)

// SummaryRequest 是 Summarizer 的输入：一次 summary 交付对应的查询与
// B5 组装完成的证据包（C-10 载荷）。
type SummaryRequest struct {
	// QueryID 查询 ID（链路追踪键；须与 Pack.QueryID 一致或留空继承之）。
	QueryID string
	// Query 用户原始查询（供真实实现拼 prompt；dev stub 只透传不用）。
	Query string
	// Pack B5 产出的证据包（候选文档 + Locator 证据）。
	Pack evidence.EvidencePack
}

// Citation 是答案中的一条引用：角标序号 + 指回证据包的定位信息。
// 前端 CitationCard 按 Index 挂锚点 #cit-<Index>（见 format.go）。
type Citation struct {
	// Index 行内引用角标（1..MaxCitations），与答案内 [n] 角标对应。
	Index int
	// DocKey 被引用的候选文档键（非空）。
	DocKey string
	// Locator 该引用指回的证据地址（quote ≤ evidence.MaxQuoteRunes）。
	Locator evidence.Locator
}

// SummaryResult 是 Summarizer 的输出：带行内 [n] 角标的答案 + 引用列表。
type SummaryResult struct {
	// QueryID 查询 ID（回显请求侧，用于链路对账）。
	QueryID string
	// Answer 答案文本，内嵌 [n] 行内引用角标（每条 citation 的 Index
	// 至少出现一次）；非空且 ≤ MaxAnswerRunes 个 rune。
	Answer string
	// Citations 引用列表（1..MaxCitations 条，Index 唯一）。
	Citations []Citation
}

// Summarizer 是 B6 摘要器的域接口：EvidencePack → 答案 + 引用。
//
// 实现方约定：
//   - 返回前必须通过 SummaryResult.Validate 出口校验；
//   - ctx 取消时应及时返回 ctx.Err()；
//   - 无有效证据（空包/校验失败）时返回错误，绝不产出无证据答案。
//
// dev 形态为 stub.go 的 StubSummarizer（确定性、不调模型）；真实化
// 路径是包一层经 D1 dc-gateway 的模型调用（见 README"真实化路径"）。
type Summarizer interface {
	// Summarize 把一次查询的证据包交付为答案与引用。
	Summarize(ctx context.Context, req SummaryRequest) (SummaryResult, error)
}
