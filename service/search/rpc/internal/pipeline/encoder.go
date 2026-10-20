// ============================================================================
// encoder.go —— 查询编码器接缝（查询文本 → 三路检索表示）。
//
// 工程方案中查询向量化属 DC representation 职责（planner README 边界：
// "不调模型、不做编码"）；管线经本接缝消费它。dev 形态为 FakeEncoder：
// fakerepr 假编码的查询侧（种子 = 查询文本），与文档侧（种子 =
// doc_key‖structure_ref‖revision_id）同一哈希口径但不同种子空间——
// 查询与文档的 sparse term 基本不相交，命中主要由 dense/multi 的哈希
// 相似度驱动。这是"真实查询编码器"的确定性替身（数字不代表真实检索
// 质量），真实化后同 encoder_id 编码、同接口替换（见 README）。
// ============================================================================
package pipeline

import (
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/fakerepr"
)

// queryMultiRows 是查询 multi 表示保留的 token 行数（dev 口径，镜像
// cmd/retr_eval 的 queryMultiTokens=8）。
const queryMultiRows = 8

// QueryRepr 是编码后的三路查询表示（retrieval.Request 的表示三件套）。
type QueryRepr struct {
	// Dense 查询稠密向量（与文档侧同维才参与 dense 路）。
	Dense []float32
	// Sparse 查询稀疏 impact（TermID → 权重）。
	Sparse map[uint32]float32
	// Multi 查询多向量 token 矩阵（各行等宽；fast 档跳过）。
	Multi [][]float32
}

// QueryEncoder 是查询编码器接缝：查询文本 → 三路查询表示。
//
// 实现方约定：
//   - 纯函数化：同文本必得同表示（管线与检索层的确定性依赖于此）；
//   - 无 IO、无状态、并发安全（与文档侧 encoder_id 同构的契约）；
//   - 与文档侧表示同维/同 term 空间才有召回意义（dev 替身见文件头声明）。
type QueryEncoder interface {
	// EncodeQuery 编码查询文本为三路表示。
	EncodeQuery(query string) QueryRepr
}

// FakeEncoder 是 dev 形态的确定性查询编码器：fakerepr 假编码的查询侧
// （dense = 哈希展开 64 维 / sparse = 词频 hash / multi = 前 8 个 token
// 各一行 8 维）。零值可用、无状态、并发安全。
type FakeEncoder struct{}

// EncodeQuery 按 fakerepr 假编码口径编码查询文本。
func (FakeEncoder) EncodeQuery(query string) QueryRepr {
	r := fakerepr.Encode(query)
	return QueryRepr{
		Dense:  r.Dense,
		Sparse: r.SparseMap(),
		Multi:  r.Rows(queryMultiRows),
	}
}
