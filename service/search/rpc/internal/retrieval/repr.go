// repr.go —— 本包表示形态与既有 common/retrieval 参考实现之间的转换层。
//
// 2026-10-08 复用审计前，sparse 内积与 multi MaxSim 在本包各有一份手写
// 实现，与 common/retrieval/{sparse,multivector} 的参考实现平行。现改为：
// 装载时把文档表示预转换为 representation 包的类型（SparseValues /
// TokenValues），召回时直接调用既有参考实现（Dot / MaxSim）。
//
// 语义映射（与旧手写实现的差异，均为对齐既有参考实现的契约）：
//   - sparse：impact 权重按 wire 契约恒为正（u8/255）；非正的查询权重项
//     被丢弃（旧实现会以负乘积参与累加，属契约外语料）；
//   - multi：零范数行以掩码表达（不参与该 query token 的 max），与
//     MaxSim 的掩码语义一致（"Masked rows never participate in score
//     math"）；宽度与查询不一致的文档/行不参与该路（同旧语义）；
//   - 精度：算术在 float64（既有参考实现口径），排序按 float64 权威值
//     后再窄化为 Scored.Score（float32）。
package retrieval

import (
	"math"
	"sort"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/representation"
)

// maxTokensCap 返回该宽度下 Contract.MaxTokens 的上限（Contract.Validate
// 约束：[1,8192] 且 MaxTokens×Dimensions ≤ 8MiB）。
func maxTokensCap(dim int) int {
	cap := 8192
	if dim > 0 {
		if byteCap := 8 * 1024 * 1024 / dim; byteCap < cap {
			cap = byteCap
		}
	}
	if cap < 1 {
		return 1
	}
	return cap
}

// multiContractForWidth 为指定行宽构造确定性的 TokenMatrix 契约（dev 形态
// 固定标识符；真实编码器接入后由 encoder_id 派生，见 README 真实化路径）。
func multiContractForWidth(dim int) representation.Contract {
	return representation.Contract{
		ID:            "sea-wholeindex-multivector-dev-v1",
		TokenizerID:   "sea-wholeindex-tokenizer-dev-v1",
		Kind:          representation.TokenMatrix,
		Dimensions:    dim,
		Normalization: "none",
		Metric:        "maxsim",
		Aggregation:   "sum_maxsim",
		MaxTokens:     maxTokensCap(dim),
	}
}

// docSparseValues 把文档稀疏表示转为参考实现的 SortedSparse 形态：
// TermID 升序、权重为正（impact wire 契约恒正；非正项丢弃）。
func docSparseValues(sparse map[uint32]float32) representation.SparseValues {
	ids := make([]int, 0, len(sparse))
	for id, w := range sparse {
		if w > 0 {
			ids = append(ids, int(id))
		}
	}
	sort.Ints(ids)
	out := representation.SparseValues{Indices: ids, Weights: make([]float64, 0, len(ids))}
	for _, id := range ids {
		out.Weights = append(out.Weights, float64(sparse[uint32(id)]))
	}
	return out
}

// docTokenValues 把文档多向量矩阵转为参考实现的 TokenValues：零范数行
// 掩码（值为零、不参与 max）；全零矩阵保留（查询时校验失败即跳过该文档）。
func docTokenValues(rows [][]float32) representation.TokenValues {
	tv := representation.TokenValues{
		Shape:  []int{len(rows), len(rows[0])},
		Values: make([][]float64, len(rows)),
		Mask:   make([]bool, len(rows)),
	}
	for i, row := range rows {
		values := make([]float64, len(row))
		norm := 0.0
		for j, x := range row {
			values[j] = float64(x)
			norm += float64(x) * float64(x)
		}
		tv.Values[i] = values
		tv.Mask[i] = norm > 0 && !math.IsNaN(norm) && !math.IsInf(norm, 0)
	}
	return tv
}

// querySparseValues 把查询稀疏表示转为参考实现形态（正权重、TermID 升序）。
func querySparseValues(query map[uint32]float32) representation.SparseValues {
	return docSparseValues(query)
}

// queryTokenValues 把查询矩阵转为参考实现形态。返回 ok=false 的情形与旧
// 手写实现一致：空查询、行宽不一致（含全空行）、无任何非零范数行。
// 行宽以查询自身首个非空行为准（multiRowWidth 语义）；与该宽不同的行
// 被丢弃（同旧实现的逐行跳过）。
func queryTokenValues(query [][]float32) (representation.TokenValues, int, bool) {
	width := multiRowWidth(query)
	if width <= 0 {
		return representation.TokenValues{}, 0, false
	}
	rows := make([][]float32, 0, len(query))
	for _, q := range query {
		if len(q) == width {
			rows = append(rows, q)
		}
	}
	if len(rows) == 0 {
		return representation.TokenValues{}, 0, false
	}
	tv := representation.TokenValues{
		Shape:  []int{len(rows), width},
		Values: make([][]float64, len(rows)),
		Mask:   make([]bool, len(rows)),
	}
	active := 0
	for i, row := range rows {
		values := make([]float64, len(row))
		norm := 0.0
		for j, x := range row {
			values[j] = float64(x)
			norm += float64(x) * float64(x)
		}
		tv.Values[i] = values
		tv.Mask[i] = norm > 0 && !math.IsNaN(norm) && !math.IsInf(norm, 0)
		if tv.Mask[i] {
			active++
		}
	}
	if active == 0 {
		return representation.TokenValues{}, 0, false
	}
	return tv, width, true
}
