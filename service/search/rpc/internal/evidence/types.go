// Package evidence 实现 B5 证据域的纯领域层：把文档级检索命中（源文本的
// 字符区间）映射为 RTW structure 契约下的 Locator，并组装成 EvidencePack。
//
// 本包属于 BTW（BreakTheWaves）搜索服务，是纯函数域层：
//   - MapHitToLocator（locator.go）：单条命中区间 → Locator；
//   - BuildPack（pack.go）：DocHit 列表 → EvidencePack（排序 + 整包校验）。
//
// 与 RTW 的镜像关系：本包不 import Sea-RideTheWaves 仓的任何代码（跨仓
// internal 包不可导入，也是本仓架构红线），而是持有 RTW
// service/knowledge/internal/structure 契约的镜像类型（TreeJSON / Locator），
// 字段语义与 JSON tag 逐一对应。任何一侧改动字段或 tag，必须双边同步
// （详见本目录 README.md 的镜像关系声明）。
//
// 边界：不做检索打分（RRF/lanes 分数由召回与融合层产出，本包只透传）、
// 不做持久化与网络 IO（纯函数，可独立测试）、不做 Markdown 解析（结构树
// 由 RTW structure.Derive 在冻结修订时产出，本包只消费）。
package evidence

import (
	"fmt"
	"unicode/utf8"
)

// 契约常量。数值与 RTW structure 包保持一致，改动需双边同步。
const (
	// MaxQuoteRunes locator quote 的长度上限（rune 计），与 RTW
	// structure.MaxQuoteRunes 一致。
	MaxQuoteRunes = 200

	// LevelParagraph 段落节点层级：标题为 ATX 1..6 级，段落固定为 7，
	// 与 RTW structure.LevelParagraph 一致。只有段落节点可承载证据。
	LevelParagraph = 7

	// HeadingParaIndex 标题节点的 para_index 哨兵值（标题不占全局段落序），
	// 与 RTW structure.HeadingAbsent 一致。
	HeadingParaIndex = -1

	// MaxCandidates 单个 EvidencePack 允许的候选文档数上限。
	MaxCandidates = 50

	// MaxEvidencesPerCandidate 单个候选允许的 evidence（Locator）条数上限。
	MaxEvidencesPerCandidate = 8
)

// ============================================================================
// RTW structure 契约的镜像类型（JSON snake_case，字段级一致，双边同步）。
// ============================================================================

// TreeJSON 是 RTW structure.Tree 的镜像：一次冻结修订的结构树。节点按
// 文档序排列，CharStart/CharEnd 为源文本字节偏移（区间 [start, end)）。
type TreeJSON struct {
	// RevisionID 冻结修订 ID（结构树的派生键）。
	RevisionID string `json:"revision_id"`
	// Nodes 结构节点列表（标题 + 段落），按文档序。
	Nodes []NodeJSON `json:"nodes"`
}

// NodeJSON 是 RTW structure.Node 的镜像：一个标题或段落节点。
type NodeJSON struct {
	// NodeID 节点 ID（RTW 派生：hex(sha256(revisionID‖0‖seq))[:16]，本包视为不透明）。
	NodeID string `json:"node_id"`
	// Level 层级：标题 1..6，段落为 LevelParagraph(7)。
	Level int `json:"level"`
	// Title 标题文本（已 trim）；段落节点为空字符串。
	Title string `json:"title"`
	// ParaIndex 全局段落序号；标题节点为 HeadingParaIndex(-1)。
	ParaIndex int `json:"para_index"`
	// CharStart 节点覆盖源文本的起始字节偏移（含）。
	CharStart int `json:"char_start"`
	// CharEnd 节点覆盖源文本的结束字节偏移（不含）。
	CharEnd int `json:"char_end"`
}

// Locator 是 RTW structure.Locator 的镜像（增补 revision_id 字段以支持
// 跨文档的 EvidencePack）：文档内一处可验证的证据地址。
type Locator struct {
	// RevisionID 该 Locator 所属的冻结修订 ID（取自结构树）。
	RevisionID string `json:"revision_id"`
	// SectionPath 从文档根到此段落之前最近的标题链（按文档序、层级严格
	// 递增）；段落位于任何标题之前时为空切片。
	SectionPath []string `json:"section_path"`
	// ParaIndex 全局段落序号（RTW 派生序，非文档内局部序）。
	ParaIndex int `json:"para_index"`
	// Quote 证据引文：命中区间文本 TrimSpace 后，超 MaxQuoteRunes 则取
	// 前 MaxQuoteRunes 个 rune。
	Quote string `json:"quote"`
}

// LaneScores 是多路召回/排序的分数快照（均 float）。Rerank 为 0 表示
// 该候选未经过 rerank 阶段（语义上的"无"，不是分数为 0）。
type LaneScores struct {
	// Dense 稠密向量路分数。
	Dense float32 `json:"dense"`
	// Sparse 稀疏（词法）路分数。
	Sparse float32 `json:"sparse"`
	// Multi 多向量路分数。
	Multi float32 `json:"multi"`
	// Rerank 重排分数；0 表示未经过 rerank（无此路）。
	Rerank float32 `json:"rerank"`
}

// EvidenceCandidate 是 EvidencePack 中的一个候选文档：携带来路分数与
// 1..MaxEvidencesPerCandidate 条证据 Locator。
type EvidenceCandidate struct {
	// DocKey 候选文档键（检索层的稳定文档标识）。
	DocKey string `json:"doc_key"`
	// RRFScore 融合后的 RRF 分数（本包只透传与排序，不重算）。
	RRFScore float32 `json:"rrf_score"`
	// Lanes 各路分数快照。
	Lanes LaneScores `json:"lanes"`
	// Evidence 证据 Locator 列表（1..8 条）。
	Evidence []Locator `json:"evidence"`
}

// EvidencePack 是一次搜索查询的证据载荷：查询 ID + 候选列表。作为跨服务
// 契约对象，序列化为 snake_case JSON。
type EvidencePack struct {
	// QueryID 查询 ID（非空）。
	QueryID string `json:"query_id"`
	// Candidates 候选列表（至多 MaxCandidates 条，建议按 RRFScore 降序）。
	Candidates []EvidenceCandidate `json:"candidates"`
}

// Validate 校验 EvidencePack 是否满足出口契约：
//   - query_id 非空；
//   - candidates 数量 ≤ MaxCandidates；
//   - 每个候选 doc_key 非空、evidence 数量在 [1, MaxEvidencesPerCandidate]；
//   - 每条 Locator 的 quote 非空且 ≤ MaxQuoteRunes 个 rune。
//
// BuildPack 在组装完成后调用本方法做最终把关；上游也可以在收到跨服务
// JSON 反序列化后调用它做入口校验。
func (p EvidencePack) Validate() error {
	if p.QueryID == "" {
		return fmt.Errorf("evidence: query_id 不能为空")
	}
	if len(p.Candidates) > MaxCandidates {
		return fmt.Errorf("evidence: 候选数 %d 超出上限 %d", len(p.Candidates), MaxCandidates)
	}
	for i, c := range p.Candidates {
		if c.DocKey == "" {
			return fmt.Errorf("evidence: 候选[%d] doc_key 不能为空", i)
		}
		if len(c.Evidence) < 1 {
			return fmt.Errorf("evidence: 候选[%d] %s 的 evidence 为空（需 1..%d 条）", i, c.DocKey, MaxEvidencesPerCandidate)
		}
		if len(c.Evidence) > MaxEvidencesPerCandidate {
			return fmt.Errorf("evidence: 候选[%d] %s 的 evidence %d 条超出上限 %d", i, c.DocKey, len(c.Evidence), MaxEvidencesPerCandidate)
		}
		for j, loc := range c.Evidence {
			n := utf8.RuneCountInString(loc.Quote)
			if n == 0 {
				return fmt.Errorf("evidence: 候选[%d] %s 的 evidence[%d] quote 为空", i, c.DocKey, j)
			}
			if n > MaxQuoteRunes {
				return fmt.Errorf("evidence: 候选[%d] %s 的 evidence[%d] quote 长度 %d rune 超出上限 %d", i, c.DocKey, j, n, MaxQuoteRunes)
			}
		}
	}
	return nil
}
