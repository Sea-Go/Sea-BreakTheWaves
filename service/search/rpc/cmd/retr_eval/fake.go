// fake.go —— dev 形态的确定性假编码器（镜像 service/async/rpc/cmd/indexer/fake.go）。
//
// cmd/indexer 是 package main，无法被导入；此处按同一口径逐函数镜像，
// 保证两侧对同一文档字段产出逐字节相同的 Repr（dense 64 维、multi 每行
// 8 维、至多前 64 token）。真实 DC representation 编码后续替换；两侧
// 同步义务见本目录 README.md。
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strings"
	"unicode"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// 假编码器参数：与 cmd/indexer/fake.go 完全一致。
const (
	fakeEncoderID = "fake-encoder.v1"
	fakeDenseDim  = 64
	fakeMultiDim  = 8
	fakeMultiRows = 64
)

// fakeRepr 是单文档三路原始表示（对应 indexer.Repr 的镜像）。
type fakeRepr struct {
	Dense     []float32
	Sparse    []retrieval.Term
	Multi     []float32
	MultiDim  int
	EncoderID string
}

// encodeDoc 用与 cmd/indexer 相同的假编码口径编码单文档：
//
//	dense  = sha256 展开 64 维（文档稳定字段做种子，迭代哈希展开）；
//	sparse = 词频 hash（token 计数 → fnv32a TermID + ImpactWeight 权重）；
//	multi  = 前 64 token 复制（每 token 一个 8 维哈希行）。
//
// 种子取 doc_key/structure_ref/revision_id 的拼接（C-1 事件只含内容引用，
// 正文编码归真实实现）。同一字段必得同一 Repr。
func encodeDoc(docKey, structureRef, revisionID string) fakeRepr {
	text := docKey + "\x1f" + structureRef + "\x1f" + revisionID
	seed := sha256.Sum256([]byte(text))
	toks := tokenize(text)
	return fakeRepr{
		Dense:     expandHash(seed[:], fakeDenseDim),
		Sparse:    sparseTerms(toks),
		Multi:     multiMatrix(toks, seed[:]),
		MultiDim:  fakeMultiDim,
		EncoderID: fakeEncoderID,
	}
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
// ImpactWeight(count, maxCount)，按 TermID 升序输出。
func sparseTerms(toks []string) []retrieval.Term {
	if len(toks) == 0 {
		return []retrieval.Term{}
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
		byID[fnv32a(t)] = retrieval.ImpactWeight(float32(counts[t]), float32(maxCount))
	}
	terms := make([]retrieval.Term, 0, len(byID))
	for id, w := range byID {
		terms = append(terms, retrieval.Term{TermID: id, Weight: w})
	}
	sort.Slice(terms, func(i, j int) bool { return terms[i].TermID < terms[j].TermID })
	return terms
}

// multiMatrix 前 64 token 复制：每 token 一个 8 维哈希行；无 token 时以
// 全文种子补一行，保证 multi_tokens >= 1。
func multiMatrix(toks []string, seed []byte) []float32 {
	rows := toks
	if len(rows) > fakeMultiRows {
		rows = rows[:fakeMultiRows]
	}
	if len(rows) == 0 {
		return expandHash(seed, fakeMultiDim)
	}
	out := make([]float32, 0, len(rows)*fakeMultiDim)
	for _, t := range rows {
		sum := sha256.Sum256([]byte(t))
		out = append(out, expandHash(sum[:], fakeMultiDim)...)
	}
	return out
}

// tokenize 按非字母数字切分并小写化。
func tokenize(s string) []string {
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
