// seeds.go —— 冻结种子集的装载与结构树派生（dev 形态）。
//
// 语料/查询/qrels 来自 testdata/index/seeds（冻结声明见该目录 README）。
// 结构树派生（deriveTree）是 RTW structure.Derive 的 dev 替身：按空行
// 分块，标题块（# 开头）成标题节点，正文块成段落节点（level=7、全局
// 段落序），导读（>）、分隔线（---）与尾注（<!--）块跳过——段落计数
// 口径与种子集构造口径一致。
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evalseed"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// seedDoc 是语料侧的单文档：源文本 + 派生的修订/结构引用。
type seedDoc struct {
	DocKey       string
	StructureRef string
	RevisionID   string
	Source       []byte
}

// loadCorpus 读 corpus/doc-*.md（按文件名排序），修订 ID = 内容哈希前 16
// hex（冻结修订的确定性替身），structure_ref 按 M2 事件惯例。
func loadCorpus(seedsDir string) ([]seedDoc, error) {
	dir := filepath.Join(seedsDir, "corpus")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("retr_eval: 读语料目录 %s: %w", dir, err)
	}
	var docs []seedDoc
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("retr_eval: 读语料 %s: %w", e.Name(), err)
		}
		if len(source) == 0 {
			return nil, fmt.Errorf("retr_eval: 语料 %s 为空", e.Name())
		}
		sum := sha256.Sum256(source)
		key := strings.TrimSuffix(e.Name(), ".md")
		docs = append(docs, seedDoc{
			DocKey:       key,
			StructureRef: "structure/" + key,
			RevisionID:   "rev-" + hex.EncodeToString(sum[:8]),
			Source:       source,
		})
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("retr_eval: 语料目录 %s 无 .md 文档", dir)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].DocKey < docs[j].DocKey })
	return docs, nil
}

// seedQuery 是 queries.jsonl 的一行（gold 以 qrels.txt 为准，此处只取
// qid 顺序与查询文本）。
type seedQuery struct {
	Qid  string
	Text string
}

// loadQueries 解析 queries.jsonl。
func loadQueries(seedsDir string) ([]seedQuery, error) {
	f, err := os.Open(filepath.Join(seedsDir, "queries.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("retr_eval: 读 queries.jsonl: %w", err)
	}
	defer f.Close()
	var queries []seedQuery
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" {
			continue
		}
		var q struct {
			Qid  string `json:"qid"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(s), &q); err != nil {
			return nil, fmt.Errorf("retr_eval: queries.jsonl 第 %d 行: %w", line, err)
		}
		if q.Qid == "" {
			return nil, fmt.Errorf("retr_eval: queries.jsonl 第 %d 行缺 qid", line)
		}
		queries = append(queries, seedQuery{Qid: q.Qid, Text: q.Text})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("retr_eval: 读 queries.jsonl: %w", err)
	}
	if len(queries) == 0 {
		return nil, fmt.Errorf("retr_eval: queries.jsonl 无查询")
	}
	return queries, nil
}

// loadQrels 解析 qrels.txt。
func loadQrels(seedsDir string) (evalseed.Qrels, error) {
	f, err := os.Open(filepath.Join(seedsDir, "qrels.txt"))
	if err != nil {
		return nil, fmt.Errorf("retr_eval: 读 qrels.txt: %w", err)
	}
	defer f.Close()
	return evalseed.ParseQrels(f)
}

// deriveTree 从 markdown 源文本派生结构树（RTW structure.Derive 的 dev
// 替身）。节点覆盖源文本字节区间 [CharStart, CharEnd)（不含块尾换行）。
func deriveTree(source []byte, revisionID string) (evidence.TreeJSON, error) {
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
		return evidence.TreeJSON{}, fmt.Errorf("retr_eval: 修订 %s 的语料无段落节点", revisionID)
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
func buildSink(docs []seedDoc) (map[string][]byte, retrieval.WholeDocIndexManifest, error) {
	type payloads struct{ dense, sparse, multi []byte }
	lanes := make([]payloads, len(docs))
	entries := make([]retrieval.DocEntry, len(docs))
	for i, d := range docs {
		repr := encodeDoc(d.DocKey, d.StructureRef, d.RevisionID)
		dense := retrieval.QuantizeF32(repr.Dense)
		sparse := retrieval.EncodeImpact(repr.Sparse)
		multi := retrieval.QuantizeMulti(repr.Multi, repr.MultiDim)
		if multi == nil {
			return nil, retrieval.WholeDocIndexManifest{}, fmt.Errorf("retr_eval: 文档 %s multi 量化失败", d.DocKey)
		}
		lanes[i] = payloads{dense: dense, sparse: sparse, multi: multi}
		entries[i] = retrieval.DocEntry{
			DocKey:       d.DocKey,
			StructureRef: d.StructureRef,
			DenseRef:     payloadRef("dense", dense),
			SparseRef:    payloadRef("sparse", sparse),
			MultiRef:     payloadRef("multi", multi),
			MultiTokens:  len(repr.Multi) / repr.MultiDim,
			EncoderID:    repr.EncoderID,
			SourceChars:  utf8.RuneCount(d.Source),
			BudgetBytes:  len(dense) + len(sparse) + len(multi),
		}
	}
	m := retrieval.WholeDocIndexManifest{
		ModuleID:  "sea-search-dev",
		ReleaseID: "retr-eval-v1",
		Docs:      entries,
	}
	retrieval.AssignID(&m)
	sink := map[string][]byte{}
	for i := range entries {
		sink[retrieval.ObjectKey(m.ManifestID, entries[i].DenseRef)] = lanes[i].dense
		sink[retrieval.ObjectKey(m.ManifestID, entries[i].SparseRef)] = lanes[i].sparse
		sink[retrieval.ObjectKey(m.ManifestID, entries[i].MultiRef)] = lanes[i].multi
	}
	mj, err := json.Marshal(m)
	if err != nil {
		return nil, retrieval.WholeDocIndexManifest{}, fmt.Errorf("retr_eval: 序列化 manifest: %w", err)
	}
	sink[retrieval.ManifestKey(m.ManifestID)] = mj
	return sink, m, nil
}

// payloadRef 镜像 indexer 的内容寻址 ref：lane.v1:hex(sha256(b))[:32]。
func payloadRef(lane string, b []byte) string {
	sum := sha256.Sum256(b)
	return lane + ".v1:" + hex.EncodeToString(sum[:16])
}
