// Package fakerepr 是 dev 形态的确定性假编码器核心：把一段文本编码成
// 三路检索表示（dense / sparse terms / multi）。
//
// 本包镜像 service/async/rpc/cmd/indexer/fake.go 的编码口径（package
// main 无法导入，先例见 cmd/retr_eval 的镜像声明）：dense 64 维、multi
// 每行 8 维、至多前 64 token。**两侧改动必须双边同步**，否则检索侧的
// 文档表示与索引侧不可对照。
//
// 消费方：
//   - internal/devseed：文档侧编码（种子 = doc_key‖structure_ref‖revision_id）
//     与 gold 查询表示（种子 = gold 文档清单）；
//   - internal/pipeline：查询侧编码（种子 = 查询文本，dev 查询编码器替身）。
//
// 边界：不调模型、无 IO、无状态；同文本必得同表示（有测试钉死）。
// 真实化路径：DC representation 编码器替换（同 encoder_id 契约）。
package fakerepr

import (
	"crypto/sha256"
	"encoding/binary"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/wholeindex"
	"sort"
	"strings"
	"unicode"
)

// 假编码器参数：与 service/async/rpc/cmd/indexer/fake.go 完全一致。
const (
	// EncoderID 假编码器标识（工件 manifest 的 encoder_id 契约值）。
	EncoderID = "fake-encoder.v1"
	// DenseDim dense 向量维度。
	DenseDim = 64
	// MultiDimMultiRow 见 MultiRows；multi 每行维度。
	MultiDim = 8
	// MultiRows multi 矩阵最多保留的 token（行）数。
	MultiRows = 64
)

// Repr 是一段文本的三路原始表示（对应 cmd/indexer.Repr 的镜像）。
type Repr struct {
	// Dense 稠密向量（DenseDim 维）。
	Dense []float32
	// Terms 稀疏词频 hash 项（按 TermID 升序；TermID 冲突时后键覆盖）。
	Terms []wholeindex.Term
	// Multi 多向量矩阵（平铺行优先；每行 MultiDim 维）。
	Multi []float32
	// MultiRows 实际行数（len(Multi)/MultiDim）。
	MultiRows int
	// EncoderID 编码器标识（恒为 EncoderID）。
	EncoderID string
}

// Encode 用假编码口径编码一段文本：
//
//	dense = sha256 展开 DenseDim 维（文本做种子，迭代哈希展开）；
//	sparse = 词频 hash（token 计数 → fnv32a TermID + ImpactWeight 权重）；
//	multi = 前 MultiRows 个 token 复制（每 token 一个 MultiDim 维哈希行，
//	        无 token 时以全文种子补一行，保证行数 ≥1）。
//
// 同文本必得同 Repr（纯哈希推导，无随机/时间依赖）。
func Encode(text string) Repr {
	seed := sha256.Sum256([]byte(text))
	toks := Tokenize(text)
	return Repr{
		Dense:     expandHash(seed[:], DenseDim),
		Terms:     sparseTerms(toks),
		Multi:     multiMatrix(toks, seed[:]),
		MultiRows: multiRowCount(toks),
		EncoderID: EncoderID,
	}
}

// multiRowCount 返回 multi 矩阵的实际行数（无 token 时补 1 行种子）。
func multiRowCount(toks []string) int {
	n := min(len(toks), MultiRows)
	if n == 0 {
		return 1
	}
	return n
}

// Rows 把 multi 平铺矩阵按行切分为 [][]float32（至多保留前 maxRows 行；
// maxRows<=0 时保留全部）。返回行与底层共享内存，调用方不得修改。
func (r Repr) Rows(maxRows int) [][]float32 {
	rows := make([][]float32, 0, r.MultiRows)
	for i := 0; i < r.MultiRows; i++ {
		rows = append(rows, r.Multi[i*MultiDim:(i+1)*MultiDim])
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return rows
}

// SparseMap 把 Terms 转为 map[TermID]权重（检索侧 sparse 路的查询形态）。
func (r Repr) SparseMap() map[uint32]float32 {
	m := make(map[uint32]float32, len(r.Terms))
	for _, t := range r.Terms {
		m[t.TermID] = float32(t.Weight)
	}
	return m
}

// SparseMapOfText 直接把文本编码为 sparse 查询 map（词频 hash 口径与
// Encode 相同；供 gold 查询表示等只需要 sparse 一路的调用方复用）。
func SparseMapOfText(text string) map[uint32]float32 {
	m := map[uint32]float32{}
	toks := Tokenize(text)
	if len(toks) == 0 {
		return m
	}
	counts := map[string]int{}
	for _, t := range toks {
		counts[t]++
	}
	maxCount := 0
	for _, c := range counts {
		if c > maxCount {
			maxCount = c
		}
	}
	keys := make([]string, 0, len(counts))
	for t := range counts {
		keys = append(keys, t)
	}
	sort.Strings(keys)
	for _, t := range keys {
		m[fnv32a(t)] = float32(wholeindex.ImpactWeight(float32(counts[t]), float32(maxCount)))
	}
	return m
}

// Tokenize 按非字母数字切分并小写化（与 indexer 侧同口径）。
func Tokenize(s string) []string {
	var toks []string
	var cur []rune
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, r)
			continue
		}
		if len(cur) > 0 {
			toks = append(toks, string(cur))
			cur = nil
		}
	}
	if len(cur) > 0 {
		toks = append(toks, string(cur))
	}
	return toks
}

// expandHash 把 seed 确定性展开成 dim 个 [-1,1) 的 float32。
func expandHash(seed []byte, dim int) []float32 {
	out := make([]float32, dim)
	var ctr [4]byte
	for i := 0; i < dim; i += 2 {
		binary.LittleEndian.PutUint32(ctr[:], uint32(i/2))
		h := sha256.New()
		h.Write(seed)
		h.Write(ctr[:])
		sum := h.Sum(nil)
		for j := 0; j < 2 && i+j < dim; j++ {
			bits := binary.LittleEndian.Uint32(sum[j*4 : j*4+4])
			out[i+j] = float32(int32(bits)) / (1 << 31)
		}
	}
	return out
}

// sparseTerms 词频 hash：token 计数 → TermID=fnv32a(token)、权重 =
// ImpactWeight(count, maxCount)，按 TermID 升序输出（TermID 冲突时
// 后键覆盖——与 indexer 侧 byID 映射同口径）。
func sparseTerms(toks []string) []wholeindex.Term {
	if len(toks) == 0 {
		return []wholeindex.Term{}
	}
	counts := make(map[string]int, len(toks))
	for _, t := range toks {
		counts[t]++
	}
	maxCount := 0
	for _, c := range counts {
		if c > maxCount {
			maxCount = c
		}
	}
	keys := make([]string, 0, len(counts))
	for t := range counts {
		keys = append(keys, t)
	}
	sort.Strings(keys)
	byID := make(map[uint32]uint8, len(keys))
	for _, t := range keys {
		byID[fnv32a(t)] = wholeindex.ImpactWeight(float32(counts[t]), float32(maxCount))
	}
	terms := make([]wholeindex.Term, 0, len(byID))
	for id, w := range byID {
		terms = append(terms, wholeindex.Term{TermID: id, Weight: w})
	}
	sort.Slice(terms, func(i, j int) bool { return terms[i].TermID < terms[j].TermID })
	return terms
}

// multiMatrix 前 MultiRows 个 token 复制：每 token 一个 MultiDim 维哈希
// 行；无 token 时以全文种子补一行，保证行数 ≥1。
func multiMatrix(toks []string, seed []byte) []float32 {
	rows := toks
	if len(rows) > MultiRows {
		rows = rows[:MultiRows]
	}
	if len(rows) == 0 {
		return expandHash(seed, MultiDim)
	}
	out := make([]float32, 0, len(rows)*MultiDim)
	for _, t := range rows {
		sum := sha256.Sum256([]byte(t))
		out = append(out, expandHash(sum[:], MultiDim)...)
	}
	return out
}

// fnv32a 是 FNV-1a 32 位哈希。
func fnv32a(s string) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return h
}
