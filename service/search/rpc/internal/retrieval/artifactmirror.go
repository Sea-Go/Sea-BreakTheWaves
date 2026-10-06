// ============================================================================
// artifactmirror.go —— 索引工件契约的检索侧镜像（dev 形态 M3）。
//
// 源头是 async 服务侧的索引工件域
// service/async/rpc/internal/artifact 与 internal/indexer 的对象键约定。
// Go 的 internal 包可见性规则禁止 search 服务直接导入 async 服务的
// internal 包（本仓既定架构红线，先例见 indexer/structure.go 对 evalseed
// TreeJSON 的镜像），因此这里按"逐字节一致"原则镜像检索侧消费所需的最小
// 面：
//
//   - WholeDocIndexManifest / DocEntry        ← artifact/manifest.go
//   - CanonicalJSON / ManifestID / AssignID   ← artifact/manifest.go
//   - QuantizeF32 / DequantizeI8              ← artifact/codec.go
//   - QuantizeMulti / DequantizeMulti         ← artifact/codec.go
//   - Term / ImpactWeight / EncodeImpact / DecodeImpact ← artifact/sparse.go
//   - ObjectKey / ManifestKey                 ← indexer/pipeline.go 键约定
//
// 任何一侧改动字段、tag、布局或键约定，必须双边同步（两侧各有独立测试
// 锁行为；本文件另有跨侧一致性的黄金向量断言）。
// ============================================================================

package retrieval

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
)

// mirrorScaleHeaderBytes 与 artifact 的量化布局头一致：int8 载荷前的
// 4 字节 little-endian float32 scale。
const mirrorScaleHeaderBytes = 4

// mirrorImpactRecordBytes 与 artifact 的稀疏 impact 定长记录一致：
// 4 字节 little-endian TermID + 1 字节权重。
const mirrorImpactRecordBytes = 5

// ============================================================================
// manifest 契约镜像（JSON snake_case，字段与 tag 逐一对齐 artifact）。
// ============================================================================

// WholeDocIndexManifest 镜像 artifact.WholeDocIndexManifest：一次全文档
// 索引发布的清单。检索侧只消费（解析/校验），不负责生产与切换。
type WholeDocIndexManifest struct {
	ManifestID string     `json:"manifest_id"`
	ModuleID   string     `json:"module_id"`
	ReleaseID  string     `json:"release_id"`
	Docs       []DocEntry `json:"docs"`
}

// DocEntry 镜像 artifact.DocEntry：单文档三路检索工件的引用。
type DocEntry struct {
	DocKey       string `json:"doc_key"`
	StructureRef string `json:"structure_ref"`
	DenseRef     string `json:"dense_ref"`
	SparseRef    string `json:"sparse_ref"`
	MultiRef     string `json:"multi_ref"`
	MultiTokens  int    `json:"multi_tokens"`
	EncoderID    string `json:"encoder_id"`
	SourceChars  int    `json:"source_chars"`
	BudgetBytes  int    `json:"budget_bytes"`
}

// CanonicalJSON 镜像 artifact.CanonicalJSON：固定字段序、docs 保持原序、
// 无缩进、最小转义；manifest_id 字段不参与规范字节。实现必须与源头
// 逐字节一致（两侧产出的 manifest_id 才可互认）。
func CanonicalJSON(m WholeDocIndexManifest) []byte {
	b := make([]byte, 0, 128+len(m.Docs)*160)
	b = append(b, '{')
	b = appendJSONKey(b, "module_id")
	b = appendJSONString(b, m.ModuleID)
	b = append(b, ',')
	b = appendJSONKey(b, "release_id")
	b = appendJSONString(b, m.ReleaseID)
	b = append(b, ',')
	b = appendJSONKey(b, "docs")
	b = append(b, '[')
	for i, d := range m.Docs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '{')
		b = appendJSONKey(b, "doc_key")
		b = appendJSONString(b, d.DocKey)
		b = append(b, ',')
		b = appendJSONKey(b, "structure_ref")
		b = appendJSONString(b, d.StructureRef)
		b = append(b, ',')
		b = appendJSONKey(b, "dense_ref")
		b = appendJSONString(b, d.DenseRef)
		b = append(b, ',')
		b = appendJSONKey(b, "sparse_ref")
		b = appendJSONString(b, d.SparseRef)
		b = append(b, ',')
		b = appendJSONKey(b, "multi_ref")
		b = appendJSONString(b, d.MultiRef)
		b = append(b, ',')
		b = appendJSONKey(b, "multi_tokens")
		b = strconv.AppendInt(b, int64(d.MultiTokens), 10)
		b = append(b, ',')
		b = appendJSONKey(b, "encoder_id")
		b = appendJSONString(b, d.EncoderID)
		b = append(b, ',')
		b = appendJSONKey(b, "source_chars")
		b = strconv.AppendInt(b, int64(d.SourceChars), 10)
		b = append(b, ',')
		b = appendJSONKey(b, "budget_bytes")
		b = strconv.AppendInt(b, int64(d.BudgetBytes), 10)
		b = append(b, '}')
	}
	b = append(b, ']')
	b = append(b, '}')
	return b
}

// ManifestID 镜像 artifact.ManifestID：hex(sha256(CanonicalJSON(m)))[0:32]。
func ManifestID(m WholeDocIndexManifest) string {
	sum := sha256.Sum256(CanonicalJSON(m))
	return hex.EncodeToString(sum[:16])
}

// AssignID 镜像 artifact.AssignID：把内容寻址 id 写回 manifest 字段。
func AssignID(m *WholeDocIndexManifest) {
	m.ManifestID = ManifestID(*m)
}

func appendJSONKey(b []byte, key string) []byte {
	b = append(b, '"')
	b = append(b, key...)
	b = append(b, '"', ':')
	return b
}

func appendJSONString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b = append(b, '\\', '"')
		case c == '\\':
			b = append(b, '\\', '\\')
		case c == '\b':
			b = append(b, '\\', 'b')
		case c == '\f':
			b = append(b, '\\', 'f')
		case c == '\n':
			b = append(b, '\\', 'n')
		case c == '\r':
			b = append(b, '\\', 'r')
		case c == '\t':
			b = append(b, '\\', 't')
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hexDigit(c>>4), hexDigit(c&0xf))
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}

func hexDigit(v byte) byte {
	if v < 10 {
		return '0' + v
	}
	return 'a' + v - 10
}

// ============================================================================
// 量化编解码镜像（布局：[4 字节 LE float32 scale][N 字节 int8]，
// scale = 127/max|v|，整块单一 scale；全零/非有限输入 scale=0）。
// ============================================================================

// QuantizeF32 镜像 artifact.QuantizeF32：dense 向量对称 int8 量化。
func QuantizeF32(v []float32) []byte {
	scale := quantScale(v)
	out := make([]byte, mirrorScaleHeaderBytes+len(v))
	binary.LittleEndian.PutUint32(out[:mirrorScaleHeaderBytes], math.Float32bits(scale))
	fs := float64(scale)
	for i, x := range v {
		if !finite32(x) {
			continue // 非有限输入量化为 0
		}
		q := int32(math.Round(float64(x) * fs))
		if q > 127 {
			q = 127
		} else if q < -127 {
			q = -127
		}
		out[mirrorScaleHeaderBytes+i] = byte(int8(q))
	}
	return out
}

// DequantizeI8 镜像 artifact.DequantizeI8：按 dim 反量化 dense 载荷；
// 长度不符（4+dim）返回 nil。
func DequantizeI8(q []byte, dim int) []float32 {
	if dim < 0 || len(q) != mirrorScaleHeaderBytes+dim {
		return nil
	}
	scale := math.Float32frombits(binary.LittleEndian.Uint32(q[:mirrorScaleHeaderBytes]))
	out := make([]float32, dim)
	if scale == 0 {
		return out
	}
	for i := range out {
		out[i] = float32(int8(q[mirrorScaleHeaderBytes+i])) / scale
	}
	return out
}

// QuantizeMulti 镜像 artifact.QuantizeMulti：行主序 multi-token 矩阵整块
// 量化（单一 scale 覆盖全部元素）。dim<=0 或长度非 dim 整倍数返回 nil。
func QuantizeMulti(mat []float32, dim int) []byte {
	if dim <= 0 || len(mat)%dim != 0 {
		return nil
	}
	return QuantizeF32(mat)
}

// DequantizeMulti 镜像 artifact.DequantizeMulti：反量化 rows*dim 行主序
// 矩阵；形状不符返回 nil。
func DequantizeMulti(q []byte, rows, dim int) []float32 {
	if rows < 0 || dim <= 0 || len(q) < mirrorScaleHeaderBytes {
		return nil
	}
	payload := len(q) - mirrorScaleHeaderBytes
	if payload%dim != 0 || payload/dim != rows {
		return nil
	}
	return DequantizeI8(q, payload)
}

func quantScale(v []float32) float32 {
	var maxAbs float64
	for _, x := range v {
		if !finite32(x) {
			continue
		}
		if a := math.Abs(float64(x)); a > maxAbs {
			maxAbs = a
		}
	}
	if !(maxAbs > 0) {
		return 0
	}
	s := 127.0 / maxAbs
	if s > math.MaxFloat32 {
		return math.MaxFloat32
	}
	return float32(s)
}

func finite32(x float32) bool {
	return !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0)
}

// ============================================================================
// 稀疏 impact 编解码镜像（TermID LE u32 + u8 权重，定长 5 字节）。
// ============================================================================

// Term 镜像 artifact.Term：TermID + 线性 0-255 权重。
type Term struct {
	TermID uint32
	Weight uint8
}

// ImpactWeight 镜像 artifact.ImpactWeight：round(255·w/maxW)，w 夹紧
// [0,maxW]；maxW<=0 或非有限输入返回 0。
func ImpactWeight(w, maxW float32) uint8 {
	if !(maxW > 0) || !finite32(w) || !finite32(maxW) {
		return 0
	}
	if w <= 0 {
		return 0
	}
	if w >= maxW {
		return 255
	}
	return uint8(math.Round(float64(w) * 255.0 / float64(maxW)))
}

// EncodeImpact 镜像 artifact.EncodeImpact：定长 5 字节记录、保持原序。
func EncodeImpact(terms []Term) []byte {
	out := make([]byte, 0, mirrorImpactRecordBytes*len(terms))
	var rec [mirrorImpactRecordBytes]byte
	for _, t := range terms {
		binary.LittleEndian.PutUint32(rec[:4], t.TermID)
		rec[4] = t.Weight
		out = append(out, rec[:]...)
	}
	return out
}

// DecodeImpact 镜像 artifact.DecodeImpact：解码 impact 载荷；长度非 5 的
// 倍数报错。
func DecodeImpact(b []byte) ([]Term, error) {
	if len(b)%mirrorImpactRecordBytes != 0 {
		return nil, fmt.Errorf("impact payload length %d is not a multiple of %d", len(b), mirrorImpactRecordBytes)
	}
	terms := make([]Term, 0, len(b)/mirrorImpactRecordBytes)
	for i := 0; i < len(b); i += mirrorImpactRecordBytes {
		terms = append(terms, Term{
			TermID: binary.LittleEndian.Uint32(b[i : i+4]),
			Weight: b[i+4],
		})
	}
	return terms, nil
}

// ============================================================================
// 对象键约定镜像（indexer 的键 = manifestID 前缀 + 内容寻址 ref）。
// ============================================================================

// 固定名对象：与 indexer 的 manifestObject / treeObject 一致。
const (
	manifestObject = "manifest.v1.json"
	treeObject     = "tree.v1.json"
)

// ObjectKey 镜像 indexer.ObjectKey：单文档载荷对象键 = manifestID/ref。
func ObjectKey(manifestID, ref string) string {
	return manifestID + "/" + ref
}

// ManifestKey 镜像 indexer.ManifestKey：manifest 对象键。
func ManifestKey(manifestID string) string {
	return manifestID + "/" + manifestObject
}

// TreeKey 镜像 indexer.TreeKey：检索树对象键。
func TreeKey(manifestID string) string {
	return manifestID + "/" + treeObject
}
