// ============================================================================
// recall.go —— 三路整篇召回（dev 形态为全量精确扫描，无 ANN 索引）。
//
// sparse 与 multi 两路的打分委托 common/retrieval 的参考实现
// （sparse.Dot / multivector.MaxSim，2026-10-08 复用整改）；dense 路的
// 余弦为本包实现（common/retrieval/dense 无独立打分函数）。
//
// 每一路都是独立的候选来源：输入查询表示，输出按分数降序（平局按
// doc_key 字典序升序，保证同输入同输出）的候选列表。只保留 score > 0
// 的文档（零/负分数不构成候选，避免给 RRF 注入尾部噪声）。
// ============================================================================

package retrieval

import (
	"math"
	"slices"
	"sort"
	"strings"

	cmulti "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/multivector"
	csparse "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/sparse"
)

// Scored 是单路召回的一个候选：doc_key + 该路分数。
type Scored struct {
	DocKey string
	Score  float32
}

// Dense 执行 dense 路整篇召回：查询向量与每文档去量 dense 向量的余弦
// 相似度。维度与查询不一致的文档跳过（视为不同编码器的表示）；零范数
// 查询返回空（无候选）。
func (sn Snapshot) Dense(query []float32) []Scored {
	if len(query) == 0 || !hasNorm32(query) {
		return nil
	}
	var out []Scored
	for _, key := range sn.DocKeys() {
		d, _ := sn.Doc(key)
		if len(d.Dense) != len(query) {
			continue
		}
		score := cosine32(query, d.Dense)
		if score > 0 {
			out = append(out, Scored{DocKey: key, Score: score})
		}
	}
	sortScored(out)
	return out
}

// Sparse 执行 sparse 路整篇召回：查询 impact 向量与每文档稀疏表示的
// 内积。打分委托 common/retrieval/sparse.Dot（参考实现；装载时已把文档
// 表示预转换为 SortedSparse 形态）。空查询返回空。
func (sn Snapshot) Sparse(query map[uint32]float32) []Scored {
	if len(query) == 0 {
		return nil
	}
	qsv := querySparseValues(query)
	if len(qsv.Indices) == 0 {
		return nil
	}
	type cand struct {
		key   string
		score float64
	}
	var cands []cand
	for _, key := range sn.DocKeys() {
		dsv, ok := sn.sparseSV[key]
		if !ok || len(dsv.Indices) == 0 {
			continue
		}
		v, err := csparse.Dot(qsv, dsv)
		if err != nil {
			continue // 契约不匹配（如非正权重）→ 该文档不参与此路
		}
		if v > 0 {
			cands = append(cands, cand{key: key, score: v})
		}
	}
	// 按参考实现的 float64 权威值排序后再窄化，保持排序确定性。
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].key < cands[j].key
	})
	out := make([]Scored, 0, len(cands))
	for _, c := range cands {
		out = append(out, Scored{DocKey: c.key, Score: float32(c.score)})
	}
	return out
}

// Multi 执行 multi 路整篇召回：exact MaxSim——每文档分数 =
// Σ_{i∈query tokens} max_{j∈doc tokens} dot(q_i, d_j)。打分委托
// common/retrieval/multivector.MaxSim（参考实现；零范数行经掩码不参与
// max，行宽与查询不一致的文档不参与此路）。空查询、行宽不一致或查询
// 各行零范数返回空。
func (sn Snapshot) Multi(query [][]float32) []Scored {
	qtv, qWidth, ok := queryTokenValues(query)
	if !ok {
		return nil
	}
	contract := multiContractForWidth(qWidth)
	if qtv.Shape[0] > contract.MaxTokens {
		return nil // 超出契约容量（见 repr.go maxTokensCap）
	}
	type cand struct {
		key   string
		score float64
	}
	var cands []cand
	for _, key := range sn.DocKeys() {
		if sn.multiW[key] != qWidth {
			continue // 不同编码器/宽度的表示不互相参与（旧语义）
		}
		dtv, ok := sn.multiTV[key]
		if !ok {
			continue
		}
		v, err := cmulti.MaxSim(qtv, dtv, contract)
		if err != nil {
			continue // 矩阵校验失败（如全零矩阵）→ 该文档不参与此路
		}
		if v > 0 {
			cands = append(cands, cand{key: key, score: v})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].key < cands[j].key
	})
	out := make([]Scored, 0, len(cands))
	for _, c := range cands {
		out = append(out, Scored{DocKey: c.key, Score: float32(c.score)})
	}
	return out
}

// sortScored 就地排序：分数降序，平局按 doc_key 字典序升序（确定性）。
func sortScored(s []Scored) {
	slices.SortFunc(s, func(a, b Scored) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		return strings.Compare(a.DocKey, b.DocKey)
	})
}

// cosine32 返回两个等宽向量的余弦相似度；零范数返回 0。
func cosine32(a, b []float32) float32 {
	dot, aa, bb := dot3232(a, b)
	if aa <= 0 || bb <= 0 {
		return 0
	}
	return dot / sqrt32(aa*bb)
}

// dot32 返回两个等宽向量的内积。
func dot32(a, b []float32) float32 {
	dot, _, _ := dot3232(a, b)
	return dot
}

// dot3232 一次遍历算出内积与两边的自点积。
func dot3232(a, b []float32) (dot, aa, bb float32) {
	for i, x := range a {
		y := b[i]
		dot += x * y
		aa += x * x
		bb += y * y
	}
	return dot, aa, bb
}

// hasNorm32 报告向量是否含非零分量（即范数 > 0）。
func hasNorm32(v []float32) bool {
	for _, x := range v {
		if x != 0 {
			return true
		}
	}
	return false
}

// sqrt32 是 math.Sqrt 的 float32 便捷包装。
func sqrt32(x float32) float32 { return float32(math.Sqrt(float64(x))) }
