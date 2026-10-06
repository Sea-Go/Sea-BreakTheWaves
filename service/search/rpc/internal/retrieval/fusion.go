// ============================================================================
// fusion.go —— RRF（Reciprocal Rank Fusion）多路融合。
//
// 公式：score(d) = Σ_{lists} 1/(k + rank_i(d))，rank 从 1 起。
// 输入的每路列表必须已按分数降序（本包三路召回的输出即满足）。
// ============================================================================

package retrieval

import (
	"slices"
	"strings"
)

// KDefault 是 RRF 的默认平滑常数（业界惯例值 60）。
const KDefault = 60

// Fused 是融合后的一个候选：doc_key + RRF 总分。
type Fused struct {
	DocKey string
	Score  float32
}

// RRF 把多路降序候选列表融合为单一列表：每路贡献 1/(k+rank)，rank 为
// 该文档在此路的位次（1 起，未出现不贡献）。k<=0 时取 KDefault。输出按
// 总分降序、平局按 doc_key 字典序升序（确定性）。空输入返回空。
func RRF(rankLists [][]Scored, k int) []Fused {
	if k <= 0 {
		k = KDefault
	}
	scores := map[string]float32{}
	for _, list := range rankLists {
		for rank, s := range list {
			scores[s.DocKey] += 1 / float32(k+rank+1)
		}
	}
	out := make([]Fused, 0, len(scores))
	for key, score := range scores {
		out = append(out, Fused{DocKey: key, Score: score})
	}
	slices.SortFunc(out, func(a, b Fused) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		return strings.Compare(a.DocKey, b.DocKey)
	})
	return out
}
