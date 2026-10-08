// ============================================================================
// fusion.go —— RRF 多路融合的薄适配层（唯一实现在 common/retrieval/rrf）。
//
// 2026-10-08 复用审计前本文件持有一份私有 RRF（与推荐侧 content_recaller
// 的私有实现、Milvus NewRRFReranker 三处平行）。现统一为共享实现，本文件
// 只做 Scored/Fused 与 rrf.Entry/rrf.Fused 之间的类型适配，保持本包导出
// 面稳定。
// ============================================================================

package retrieval

import (
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/rrf"
)

// KDefault 是 RRF 的默认平滑常数（转发共享实现；业界惯例值 60，与
// Milvus RRFReranker 默认一致）。
const KDefault = rrf.KDefault

// Fused 是融合后的一个候选：doc_key + RRF 总分。
type Fused struct {
	DocKey string
	Score  float32
}

// RRF 把多路候选列表融合为单一列表：每路贡献 1/(k+rank)，rank 为该文档
// 在此路的位次（1 起，未出现不贡献；未排序的路先按分数降序定名次）。
// k<=0 时取 KDefault。输出按总分降序、平局按 doc_key 字典序升序（确定性）。
// 空输入返回空。
func RRF(rankLists [][]Scored, k int) []Fused {
	lists := make([][]rrf.Entry, len(rankLists))
	for i, list := range rankLists {
		entries := make([]rrf.Entry, len(list))
		for j, s := range list {
			entries[j] = rrf.Entry{Key: s.DocKey, Score: float64(s.Score)}
		}
		lists[i] = entries
	}
	fused := rrf.RRF(lists, k)
	out := make([]Fused, 0, len(fused))
	for _, f := range fused {
		out = append(out, Fused{DocKey: f.Key, Score: float32(f.Score)})
	}
	return out
}
