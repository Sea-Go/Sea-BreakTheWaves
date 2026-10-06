package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strings"
	"unicode"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/artifact"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/indexer"
)

// 假编码器参数：dense 64 维、multi 每行 8 维、至多前 64 token。
const (
	fakeEncoderID = "fake-encoder.v1"
	fakeDenseDim  = 64
	fakeMultiDim  = 8
	fakeMultiRows = 64
)

// fakeEncoder 是 Encoder 接缝的确定性假实现（dev 形态）：
//
//	dense  = sha256 展开 64 维（事件稳定字段做种子，迭代哈希展开）；
//	sparse = 词频 hash（token 计数 → fnv32a TermID + ImpactWeight 权重）；
//	multi  = 前 64 token 复制（每 token 一个 8 维哈希行）。
//
// 同一文档字段必得同一 Repr；真实 DC representation 编码后续替换本实现。
// 事件只含内容引用（C-1 契约），故种子取 doc_key/structure_ref/revision_id
// 的拼接——真实实现按引用取正文编码，接缝形状相同。
type fakeEncoder struct{}

func (fakeEncoder) Encode(_ context.Context, doc indexer.ReleaseDoc) (indexer.Repr, error) {
	text := doc.DocKey + "\x1f" + doc.StructureRef + "\x1f" + doc.RevisionID
	seed := sha256.Sum256([]byte(text))
	toks := tokenize(text)
	return indexer.Repr{
		Dense:     expandHash(seed[:], fakeDenseDim),
		Sparse:    sparseTerms(toks),
		Multi:     multiMatrix(toks, seed[:]),
		MultiDim:  fakeMultiDim,
		EncoderID: fakeEncoderID,
	}, nil
}

// expandHash 把 seed 确定性展开成 dim 个 [-1,1) 的 float32：第 k 轮取
// sha256(seed ‖ le32(k)) 的 8 字节组成 2 个分量。
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
// artifact.ImpactWeight(count, maxCount)，按 TermID 升序输出（同输入同输出；
// TermID 碰撞时按 token 字典序后写覆盖，仍确定）。
func sparseTerms(toks []string) []artifact.Term {
	if len(toks) == 0 {
		return []artifact.Term{}
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
		byID[fnv32a(t)] = artifact.ImpactWeight(float32(counts[t]), float32(maxCount))
	}
	terms := make([]artifact.Term, 0, len(byID))
	for id, w := range byID {
		terms = append(terms, artifact.Term{TermID: id, Weight: w})
	}
	sort.Slice(terms, func(i, j int) bool { return terms[i].TermID < terms[j].TermID })
	return terms
}

// multiMatrix 前 64 token 复制：每 token 一个 fakeMultiDim 维哈希行；无
// token 时以全文种子补一行，保证 multi_tokens >= 1（manifest 契约）。
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

// tokenize 按非字母数字切分并小写化，产出假编码的 token 流。
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

// fnv32a 是 FNV-1a 32 位哈希（标准参数，无依赖）。
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
