// ============================================================================
// recall.go —— 三路整篇召回（dev 形态为全量精确扫描，无 ANN 索引）。
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
// 内积（只累加共同 TermID 的权重乘积）。空查询返回空。
func (sn Snapshot) Sparse(query map[uint32]float32) []Scored {
	if len(query) == 0 {
		return nil
	}
	var out []Scored
	// Sort query term IDs for deterministic float32 summation (Go map
	// iteration order is randomized; FP addition is not associative).
	terms := make([]uint32, 0, len(query))
	for term := range query {
		terms = append(terms, term)
	}
	sort.Slice(terms, func(i, j int) bool { return terms[i] < terms[j] })
	for _, key := range sn.DocKeys() {
		d, _ := sn.Doc(key)
		var score float32
		for _, term := range terms {
			if dw, ok := d.Sparse[term]; ok {
				score += query[term] * dw
			}
		}
		if score > 0 {
			out = append(out, Scored{DocKey: key, Score: score})
		}
	}
	sortScored(out)
	return out
}

// Multi 执行 multi 路整篇召回：exact MaxSim——每文档分数 =
// Σ_{i∈query tokens} max_{j∈doc tokens} dot(q_i, d_j)。查询各行需等宽；
// 文档行宽与查询不一致的行跳过（不参与该 query token 的 max）。空查询
// 或查询各行零范数返回空。
func (sn Snapshot) Multi(query [][]float32) []Scored {
	if len(query) == 0 {
		return nil
	}
	qWidth := multiRowWidth(query)
	if qWidth <= 0 {
		return nil
	}
	usable := false
	for _, q := range query {
		if hasNorm32(q) {
			usable = true
			break
		}
	}
	if !usable {
		return nil
	}
	var out []Scored
	for _, key := range sn.DocKeys() {
		d, _ := sn.Doc(key)
		var score float32
		for _, q := range query {
			if len(q) != qWidth || !hasNorm32(q) {
				continue
			}
			best := float32(math.Inf(-1))
			for _, row := range d.Multi {
				if len(row) != qWidth {
					continue
				}
				if s := dot32(q, row); s > best {
					best = s
				}
			}
			if best == float32(math.Inf(-1)) {
				continue // 该 query token 无可比对行，不计入
			}
			score += best
		}
		if score > 0 {
			out = append(out, Scored{DocKey: key, Score: score})
		}
	}
	sortScored(out)
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
