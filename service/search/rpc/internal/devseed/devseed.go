// Package devseed 把冻结种子集语料装载成检索侧可用的只读 Store（dev
// 形态）：corpus/*.md → 假编码（fakerepr，镜像 cmd/indexer 口径）→
// M2 工件量化落位（manifest + 三路内容寻址载荷）→ retrieval.Load 装载 →
// 逐文档 AttachSource（deriveTree 派生结构树，RTW structure.Derive 的
// dev 替身）。
//
// 消费方：cmd/retr_eval（评测入口）、cmd/search_demo（演示入口）与
// internal/pipeline 的种子集测试。抽出共享包是为了避免 seeds 装载口径
// 出现多份拷贝（先例：cmd/retr_eval 曾自带完整副本）。
//
// 边界：只消费冻结种子集，不做索引生产/切换（归 M2 indexer 与
// artifact.Switcher 层）；deriveTree 是 markdown 结构派生的 dev 替身，
// 段落计数口径与种子集构造口径一致（见 testdata/index/seeds/README）。
package devseed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/wholeindex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/fakerepr"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// Doc 是语料侧的单文档：源文本 + 派生的修订/结构引用。
type Doc struct {
	// DocKey 稳定文档键（语料文件名去扩展名）。
	DocKey string
	// StructureRef 结构引用（M2 事件惯例：structure/<doc_key>）。
	StructureRef string
	// RevisionID 冻结修订 ID（内容哈希前 16 hex，确定性替身）。
	RevisionID string
	// Source 冻结源文本字节。
	Source []byte
}

// Corpus 是一次种子集装载的结果：可直接检索的 Store + 语料清单与
// 工件元数据（encoder_id / manifest_id，供演示与对账输出）。
type Corpus struct {
	// Store 装载完成（含结构树/源文本）的只读检索库。
	Store *retrieval.Store
	// Docs 语料文档清单（按 doc_key 字典序）。
	Docs []Doc
	// ManifestID 本次装载的工件 manifest ID。
	ManifestID string
	// EncoderID 编码器标识（恒为 fakerepr.EncoderID）。
	EncoderID string
}

// LoadCorpus 跑通"语料 → 假编码 → 工件量化 → 装载 → 补源"全链路，
// 返回可直接检索的 Corpus。任一环节失败即整体失败（全有或全无）。
func LoadCorpus(seedsDir string) (*Corpus, error) {
	if seedsDir == "" {
		return nil, fmt.Errorf("devseed: seeds 目录为空")
	}
	docs, err := loadCorpusDocs(seedsDir)
	if err != nil {
		return nil, err
	}
	sink, manifest, err := buildSink(docs)
	if err != nil {
		return nil, err
	}
	store, err := retrieval.Load(sink, manifest.Docs)
	if err != nil {
		return nil, fmt.Errorf("devseed: 装载工件: %w", err)
	}
	for _, d := range docs {
		tree, err := DeriveTree(d.Source, d.RevisionID)
		if err != nil {
			return nil, err
		}
		if err := store.AttachSource(d.DocKey, tree, d.Source); err != nil {
			return nil, fmt.Errorf("devseed: 补齐文档 %s: %w", d.DocKey, err)
		}
	}
	return &Corpus{
		Store:      store,
		Docs:       docs,
		ManifestID: manifest.ManifestID,
		EncoderID:  fakerepr.EncoderID,
	}, nil
}

// loadCorpusDocs 读 corpus/doc-*.md（按文件名排序），修订 ID = 内容哈希
// 前 16 hex（冻结修订的确定性替身），structure_ref 按 M2 事件惯例。
func loadCorpusDocs(seedsDir string) ([]Doc, error) {
	dir := filepath.Join(seedsDir, "corpus")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("devseed: 读语料目录 %s: %w", dir, err)
	}
	var docs []Doc
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("devseed: 读语料 %s: %w", e.Name(), err)
		}
		if len(source) == 0 {
			return nil, fmt.Errorf("devseed: 语料 %s 为空", e.Name())
		}
		sum := sha256.Sum256(source)
		key := strings.TrimSuffix(e.Name(), ".md")
		docs = append(docs, Doc{
			DocKey:       key,
			StructureRef: "structure/" + key,
			RevisionID:   "rev-" + hex.EncodeToString(sum[:8]),
			Source:       source,
		})
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("devseed: 语料目录 %s 无 .md 文档", dir)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].DocKey < docs[j].DocKey })
	return docs, nil
}

// DeriveTree 从 markdown 源文本派生结构树（RTW structure.Derive 的 dev
// 替身）。按空行分块：标题块（# 开头）成标题节点，正文块成段落节点
// （level=7、全局段落序），导读（>）、分隔线（---）与尾注（<!--）块
// 跳过——段落计数口径与种子集构造口径一致。节点覆盖源文本字节区间
// [CharStart, CharEnd)（不含块尾换行）。
func DeriveTree(source []byte, revisionID string) (evidence.TreeJSON, error) {
	tree := evidence.TreeJSON{RevisionID: revisionID}
	paraIdx := 0
	nodeSeq := 0
	lines := splitLinesWithOffsets(source)
	i := 0
	for i < len(lines) {
		if lines[i].isBlank() {
			i++
			continue
		}
		// 块 = 连续非空行。
		start := lines[i].start
		end := lines[i].end
		first := lines[i]
		j := i + 1
		for j < len(lines) && !lines[j].isBlank() {
			end = lines[j].end
			j++
		}
		kind, level, title := classifyBlock(first.content)
		switch kind {
		case blockHeading:
			tree.Nodes = append(tree.Nodes, evidence.NodeJSON{
				NodeID:    nodeID(revisionID, nodeSeq),
				Level:     level,
				Title:     title,
				ParaIndex: evidence.HeadingParaIndex,
				CharStart: start,
				CharEnd:   end,
			})
			nodeSeq++
		case blockParagraph:
			tree.Nodes = append(tree.Nodes, evidence.NodeJSON{
				NodeID:    nodeID(revisionID, nodeSeq),
				Level:     evidence.LevelParagraph,
				ParaIndex: paraIdx,
				CharStart: start,
				CharEnd:   end,
			})
			paraIdx++
			nodeSeq++
		}
		i = j
	}
	if paraIdx == 0 {
		return evidence.TreeJSON{}, fmt.Errorf("devseed: 修订 %s 的语料无段落节点", revisionID)
	}
	return tree, nil
}

// nodeID 派生节点 ID（hex(sha256(revisionID‖0‖seq))[:16] 的简化替身，
// 检索侧视为不透明）。
func nodeID(revisionID string, seq int) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d", revisionID, seq)
	return hex.EncodeToString(h.Sum(nil)[:8])
}

type lineSpan struct {
	content string
	start   int // 行首字节偏移
	end     int // 行尾字节偏移（不含换行）
}

func splitLinesWithOffsets(source []byte) []lineSpan {
	var lines []lineSpan
	off := 0
	for len(source) > 0 {
		nl := indexByte(source, '\n')
		var seg []byte
		if nl < 0 {
			seg = source
			source = nil
		} else {
			seg = source[:nl]
			source = source[nl+1:]
		}
		lines = append(lines, lineSpan{content: string(seg), start: off, end: off + len(seg)})
		off += len(seg) + 1
	}
	return lines
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func (l lineSpan) isBlank() bool { return strings.TrimSpace(l.content) == "" }

type blockKind int

const (
	blockSkip blockKind = iota
	blockHeading
	blockParagraph
)

// classifyBlock 判定块类型：标题（1..6 级）、导读/分隔线/尾注（跳过）、
// 其余为正文段落。
func classifyBlock(first string) (kind blockKind, level int, title string) {
	trimmed := strings.TrimSpace(first)
	switch {
	case strings.HasPrefix(trimmed, "#"):
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level >= 1 && level <= 6 && len(trimmed) > level && trimmed[level] == ' ' {
			return blockHeading, level, strings.TrimSpace(trimmed[level+1:])
		}
		return blockSkip, 0, ""
	case strings.HasPrefix(trimmed, ">"), strings.HasPrefix(trimmed, "<!--"), strings.HasPrefix(trimmed, "---"):
		return blockSkip, 0, ""
	default:
		return blockParagraph, 0, ""
	}
}

// buildSink 把语料按 M2 工件约定编码落位：三路量化载荷（内容寻址 ref）
// + manifest 对象。返回 sink 与 manifest（docs 顺序即语料字典序）。
func buildSink(docs []Doc) (map[string][]byte, wholeindex.WholeDocIndexManifest, error) {
	type payloads struct{ dense, sparse, multi []byte }
	lanes := make([]payloads, len(docs))
	entries := make([]wholeindex.DocEntry, len(docs))
	for i, d := range docs {
		// 文档侧编码种子：doc_key‖structure_ref‖revision_id（C-1 事件只含
		// 内容引用，正文编码归真实实现；与 cmd/indexer 同口径）。
		text := d.DocKey + "\x1f" + d.StructureRef + "\x1f" + d.RevisionID
		repr := fakerepr.Encode(text)
		dense := wholeindex.QuantizeF32(repr.Dense)
		sparse := wholeindex.EncodeImpact(repr.Terms)
		multi := wholeindex.QuantizeMulti(repr.Multi, repr.MultiRows)
		if multi == nil {
			return nil, wholeindex.WholeDocIndexManifest{}, fmt.Errorf("devseed: 文档 %s multi 量化失败", d.DocKey)
		}
		lanes[i] = payloads{dense: dense, sparse: sparse, multi: multi}
		entries[i] = wholeindex.DocEntry{
			DocKey:       d.DocKey,
			StructureRef: d.StructureRef,
			DenseRef:     payloadRef("dense", dense),
			SparseRef:    payloadRef("sparse", sparse),
			MultiRef:     payloadRef("multi", multi),
			MultiTokens:  repr.MultiRows,
			EncoderID:    repr.EncoderID,
			SourceChars:  utf8.RuneCount(d.Source),
			BudgetBytes:  len(dense) + len(sparse) + len(multi),
		}
	}
	m := wholeindex.WholeDocIndexManifest{
		ModuleID: "sea-search-dev",
		// ReleaseID 冻结为 retr_eval 历史口径：manifest_id 是内容寻址
		// （含 release_id），改动会改变 manifest_id 并使已记录的评测
		// 输出不可对照（见 cmd/retr_eval/README.md 样例）。
		ReleaseID: "retr-eval-v1",
		Docs:      entries,
	}
	wholeindex.AssignID(&m)
	sink := map[string][]byte{}
	for i := range entries {
		sink[wholeindex.ObjectKey(m.ManifestID, entries[i].DenseRef)] = lanes[i].dense
		sink[wholeindex.ObjectKey(m.ManifestID, entries[i].SparseRef)] = lanes[i].sparse
		sink[wholeindex.ObjectKey(m.ManifestID, entries[i].MultiRef)] = lanes[i].multi
	}
	mj, err := json.Marshal(m)
	if err != nil {
		return nil, wholeindex.WholeDocIndexManifest{}, fmt.Errorf("devseed: 序列化 manifest: %w", err)
	}
	sink[wholeindex.ManifestKey(m.ManifestID)] = mj
	return sink, m, nil
}

// payloadRef 镜像 indexer 的内容寻址 ref：lane.v1:hex(sha256(b))[:32]。
func payloadRef(lane string, b []byte) string {
	sum := sha256.Sum256(b)
	return lane + ".v1:" + hex.EncodeToString(sum[:16])
}
