// alias.go —— 索引工件类型与算法向 service/common/wholeindex 的兼容别名。
//
// 为什么在这里做别名而不是把代码留在本包：量化编解码、canonical JSON 与
// manifest 计算是**跨服务共享的纯算法**，async（生产侧）与 search（消费侧）
// 都必须用同一份实现。此前检索侧用"逐字节镜像"绕开 Go internal 可见性规则
// （见已删除的 search/retrieval/artifactmirror.go），导致同一算法两处实现、
// 靠人工维护同步义务——2026-10-08 复用审计判为重复实现。
//
// 现在唯一实现位于 service/common/wholeindex（根 module 下的共享包，两侧均
// 可导入）。本文件只保留类型别名，使既有调用点无需改动；新增代码应直接
// 导入 common/wholeindex。
package artifact

import "github.com/Sea-Go/Sea-BreakTheWaves/service/common/wholeindex"

// 工件契约类型（唯一实现见 common/wholeindex）。
type (
	// WholeDocIndexManifest 是一次发布的整篇索引清单。
	WholeDocIndexManifest = wholeindex.WholeDocIndexManifest
	// DocEntry 是清单中的单文档条目。
	DocEntry = wholeindex.DocEntry
	// Term 是稀疏表示的一个词项（TermID + 量化权重）。
	Term = wholeindex.Term
)

// 契约常量。
const (
	// MaxMultiTokens 多向量矩阵的 token 行数上限。
	MaxMultiTokens = wholeindex.MaxMultiTokens
	// ImpactRecordBytes 单条 impact 记录的线格式字节数。
	ImpactRecordBytes = wholeindex.ImpactRecordBytes
)

// ErrInvalidManifest 标记清单字段级契约违规。
var ErrInvalidManifest = wholeindex.ErrInvalidManifest

// 量化编解码（唯一实现见 common/wholeindex）。
var (
	// QuantizeF32 把 float32 向量量化为 int8 线格式。
	QuantizeF32 = wholeindex.QuantizeF32
	// DequantizeI8 把 int8 线格式还原为 float32 向量。
	DequantizeI8 = wholeindex.DequantizeI8
	// QuantizeMulti 量化多向量矩阵。
	QuantizeMulti = wholeindex.QuantizeMulti
	// DequantizeMulti 还原多向量矩阵。
	DequantizeMulti = wholeindex.DequantizeMulti
	// RoundTripMaxErr 计算量化往返的最大相对误差（测试与容量评估用）。
	RoundTripMaxErr = wholeindex.RoundTripMaxErr
)

// manifest 序列化与内容寻址（唯一实现见 common/wholeindex）。
var (
	// CanonicalJSON 生成清单的规范化 JSON（字段固定序、最小转义）。
	CanonicalJSON = wholeindex.CanonicalJSON
	// ManifestID 计算清单的内容寻址 ID。
	ManifestID = wholeindex.ManifestID
	// AssignID 就地回填清单的 manifest_id。
	AssignID = wholeindex.AssignID
)

// 稀疏 impact 编解码（唯一实现见 common/wholeindex）。
var (
	// ImpactWeight 把归一化权重映射为 u8 量化值。
	ImpactWeight = wholeindex.ImpactWeight
	// EncodeImpact 编码稀疏 impact 列表。
	EncodeImpact = wholeindex.EncodeImpact
	// DecodeImpact 解码稀疏 impact 列表。
	DecodeImpact = wholeindex.DecodeImpact
)
