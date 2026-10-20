// ============================================================================
// BuildPack：DocHit 列表 → EvidencePack。
//
// 组装策略为"全有或全无"：任一命中无法映射为 Locator，整包失败并返回
// 错误，绝不产出部分证据的包（下游 LLM 引用必须可信，宁可无证据也
// 不能有假证据）。候选按 RRFScore 降序、平局按 doc_key 字典序，保证
// 相同输入永远产出相同的包（排序确定性）。
// ============================================================================

package evidence

import (
	"fmt"
	"slices"
	"strings"
)

// HitSpan 是检索层报告的一个命中区间：源文本字节偏移 [CharStart, CharEnd)。
type HitSpan struct {
	// CharStart 命中区间起始字节偏移（含）。
	CharStart int
	// CharEnd 命中区间结束字节偏移（不含）。
	CharEnd int
}

// DocHit 是 BuildPack 的输入：一个候选文档的全部命中及其定位所需的
// 结构树与源文本（两者必须来自同一次冻结修订）。
type DocHit struct {
	// DocKey 候选文档键（非空）。
	DocKey string
	// RevisionID 命中所属冻结修订 ID，须与 Tree.RevisionID 一致。
	RevisionID string
	// RRFScore 融合 RRF 分数（透传，用于排序）。
	RRFScore float32
	// Lanes 各路分数快照（透传）。
	Lanes LaneScores
	// Hits 命中区间列表（1..MaxEvidencesPerCandidate 条）。
	Hits []HitSpan
	// Tree 该修订的结构树（RTW structure 契约镜像）。
	Tree TreeJSON
	// Source 该修订的冻结源文本字节（与 Tree 同一修订）。
	Source []byte
}

// BuildPack 把 hits 组装为 EvidencePack：
//
//   - queryID 非空；hits 至多 MaxCandidates 条；每个 DocHit 的命中数在
//     1..MaxEvidencesPerCandidate 之间；
//   - 逐 hit 调 MapHitToLocator，任一失败则整包失败（返回零值包与错误，
//     不返回部分证据）；
//   - 候选按 RRFScore 降序、平局按 doc_key 字典序升序；
//   - 返回前再跑一次 EvidencePack.Validate 作为出口把关。
func BuildPack(queryID string, hits []DocHit) (EvidencePack, error) {
	if queryID == "" {
		return EvidencePack{}, fmt.Errorf("evidence: query_id 不能为空")
	}
	if len(hits) > MaxCandidates {
		return EvidencePack{}, fmt.Errorf("evidence: 候选数 %d 超出上限 %d", len(hits), MaxCandidates)
	}

	candidates := make([]EvidenceCandidate, 0, len(hits))
	for _, h := range hits {
		if h.DocKey == "" {
			return EvidencePack{}, fmt.Errorf("evidence: 候选 doc_key 不能为空")
		}
		if len(h.Hits) < 1 || len(h.Hits) > MaxEvidencesPerCandidate {
			return EvidencePack{}, fmt.Errorf("evidence: 候选 %s 的命中数 %d 超出 1..%d", h.DocKey, len(h.Hits), MaxEvidencesPerCandidate)
		}
		if h.RevisionID != h.Tree.RevisionID {
			return EvidencePack{}, fmt.Errorf("evidence: 候选 %s 的 RevisionID %q 与结构树 RevisionID %q 不一致", h.DocKey, h.RevisionID, h.Tree.RevisionID)
		}
		evidence := make([]Locator, 0, len(h.Hits))
		for i, sp := range h.Hits {
			loc, err := MapHitToLocator(h.Tree, h.Source, sp.CharStart, sp.CharEnd)
			if err != nil {
				return EvidencePack{}, fmt.Errorf("evidence: 候选 %s 第 %d 个命中: %w", h.DocKey, i+1, err)
			}
			evidence = append(evidence, loc)
		}
		candidates = append(candidates, EvidenceCandidate{
			DocKey:   h.DocKey,
			RRFScore: h.RRFScore,
			Lanes:    h.Lanes,
			Evidence: evidence,
		})
	}

	// 排序确定性：RRFScore 降序，平局按 doc_key 字典序；稳定排序保证
	// 同输入同输出。
	slices.SortStableFunc(candidates, func(a, b EvidenceCandidate) int {
		if a.RRFScore != b.RRFScore {
			if a.RRFScore > b.RRFScore {
				return -1
			}
			return 1
		}
		return strings.Compare(a.DocKey, b.DocKey)
	})

	pack := EvidencePack{QueryID: queryID, Candidates: candidates}
	if err := pack.Validate(); err != nil {
		return EvidencePack{}, err
	}
	return pack, nil
}
