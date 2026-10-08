// Package retrieval 实现 M3 检索内核的整篇化切片（dev 形态）：把 M2
// indexer 产出的全文档索引工件（manifest + 三路量化载荷）装载成进程内
// 只读 Store，在其上执行三路整篇召回（dense 余弦 / sparse impact 内积 /
// multi exact MaxSim）、RRF 融合与档位化检索（fast|balanced|deep），最终
// 组装 evidence.EvidencePack。
//
// 边界（dev 形态声明，详见本目录 README.md）：
//   - 工件契约以 artifactmirror.go 镜像 async 服务侧的 artifact 域，
//     不做跨服务 internal 导入（Go 可见性规则 + 本仓架构红线）；
//   - 只消费工件，不生产、不切换（生产归 M2 indexer，装载生效归
//     artifact.Switcher 所在层）；
//   - 命中定位是 dev 近似：取文档第一个段落节点整段为 HitSpan。
package retrieval

import (
	"encoding/json"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/wholeindex"
	"sort"
	"sync"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
)

// LoadedDoc 是单个文档装载后的完整形态：三路去量表示 + 证据定位所需的
// 结构树与冻结源文本。
type LoadedDoc struct {
	// Dense 去量后的整篇 dense 向量（与查询同维才参与 dense 路）。
	Dense []float32
	// Sparse 稀疏 impact 表示：TermID → 权重（u8 线性值转 float32）。
	Sparse map[uint32]float32
	// Multi 多向量 token 矩阵：每行一个 token 向量（exact MaxSim 用）。
	Multi [][]float32
	// Tree 该文档冻结修订的结构树（证据定位用；可后置 Attach）。
	Tree evidence.TreeJSON
	// Source 该文档冻结修订的源文本字节（与 Tree 同一修订）。
	Source []byte
}

// Store 是装载后的文档集：构造完成后只读，可并发检索。Tree/Source 允许
// 经 AttachSource 后置补齐（工件集合不含源文本与逐文档结构树）。
type Store struct {
	mu   sync.RWMutex
	docs map[string]LoadedDoc
}

// NewStore 从已构造好的 LoadedDoc 集合建 Store。校验：docs 非空、docKey
// 非空、每文档 Dense 非空（manifest 契约要求 dense 路存在）、Multi各行
// 等宽（行内形状一致才可做 exact MaxSim）。Sparse/Tree/Source 允许为空
// （对应路不产生候选 / 证据组装阶段才需要）。
func NewStore(docs map[string]LoadedDoc) (*Store, error) {
	if len(docs) == 0 {
		return nil, fmt.Errorf("retrieval: docs 不能为空")
	}
	for key, d := range docs {
		if key == "" {
			return nil, fmt.Errorf("retrieval: 存在空 doc_key")
		}
		if len(d.Dense) == 0 {
			return nil, fmt.Errorf("retrieval: 文档 %s 缺少 dense 表示", key)
		}
		if w := multiRowWidth(d.Multi); w < 0 {
			return nil, fmt.Errorf("retrieval: 文档 %s 的 multi 各行宽度不一致", key)
		}
	}
	cp := make(map[string]LoadedDoc, len(docs))
	for k, v := range docs {
		cp[k] = v
	}
	return &Store{docs: cp}, nil
}

// multiRowWidth 返回 multi 矩阵的行宽（0 行返回 0）；各行不等宽返回 -1。
func multiRowWidth(rows [][]float32) int {
	w := 0
	for _, r := range rows {
		if w == 0 {
			w = len(r)
			continue
		}
		if len(r) != w {
			return -1
		}
	}
	return w
}

// AttachSource 为已存在的文档补齐结构树与源文本（装载工件时不包含这两
// 者，由调用方从语料侧取得）。要求 docKey 已在 Store 中、树有非空
// revision_id、源文本非空；重复 Attach 以最后一次为准。
func (s *Store) AttachSource(docKey string, tree evidence.TreeJSON, source []byte) error {
	if docKey == "" {
		return fmt.Errorf("retrieval: doc_key 不能为空")
	}
	if tree.RevisionID == "" {
		return fmt.Errorf("retrieval: 文档 %s 的结构树缺少 revision_id", docKey)
	}
	if len(source) == 0 {
		return fmt.Errorf("retrieval: 文档 %s 的源文本为空", docKey)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[docKey]
	if !ok {
		return fmt.Errorf("retrieval: 文档 %s 不在 Store 中", docKey)
	}
	d.Tree = tree
	d.Source = append([]byte(nil), source...)
	s.docs[docKey] = d
	return nil
}

// Snapshot 返回 Store 的只读视图：复制 map 头，切片/树共享底层内存，
// 调用方不得修改（Store 本身在无 Attach 并发时全域只读）。并发检索
// 的入口统一走 Snapshot，避免直接触碰 Store 的锁。
type Snapshot struct {
	docs map[string]LoadedDoc
}

// Snapshot 生成当前文档集的只读视图。
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make(map[string]LoadedDoc, len(s.docs))
	for k, v := range s.docs {
		cp[k] = v
	}
	return Snapshot{docs: cp}
}

// Len 返回视图内文档数。
func (sn Snapshot) Len() int { return len(sn.docs) }

// DocKeys 返回全部 doc_key（字典序，确定性遍历入口）。
func (sn Snapshot) DocKeys() []string {
	keys := make([]string, 0, len(sn.docs))
	for k := range sn.docs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Doc 取单个文档的只读表示。
func (sn Snapshot) Doc(docKey string) (LoadedDoc, bool) {
	d, ok := sn.docs[docKey]
	return d, ok
}

// Load 从 M2 indexer 产出的工件集合（内存 sink；键约定 = manifestID 前缀
// + 内容寻址 ref，与 indexer.ObjectKey 一致）解码三路表示并建 Store：
//
//   - manifestID 从 sink 中唯一的 "<manifestID>/manifest.v1.json" 对象
//     发现（与 indexer.ManifestKey 一致），并与其 JSON 内容的
//     manifest_id 交叉校验；
//   - dense：wholeindex.DequantizeI8（dim = 载荷长度-4）；
//   - sparse：wholeindex.DecodeImpact → map[TermID]权重；
//   - multi：wholeindex.DequantizeMulti（rows = entry.MultiTokens，dim 由载荷长度
//     整除得出）→ 按行切分；
//   - Tree/Source 不在工件集合内，装载后为空，调用方按需 AttachSource。
//
// entries 是权威文档清单（通常来自 manifest.docs）；任一对象缺失或形状
// 不符即整体失败。
func Load(sink map[string][]byte, entries []wholeindex.DocEntry) (*Store, error) {
	if len(sink) == 0 {
		return nil, fmt.Errorf("retrieval: sink 为空")
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("retrieval: entries 为空")
	}
	manifestID, err := discoverManifestID(sink)
	if err != nil {
		return nil, err
	}
	docs := make(map[string]LoadedDoc, len(entries))
	for i, e := range entries {
		if e.DocKey == "" {
			return nil, fmt.Errorf("retrieval: entries[%d].doc_key 为空", i)
		}
		if _, dup := docs[e.DocKey]; dup {
			return nil, fmt.Errorf("retrieval: 文档 %s 在 entries 中重复", e.DocKey)
		}
		d, err := loadDoc(sink, manifestID, e)
		if err != nil {
			return nil, fmt.Errorf("retrieval: 文档 %s: %w", e.DocKey, err)
		}
		docs[e.DocKey] = d
	}
	return NewStore(docs)
}

// discoverManifestID 按 indexer.ManifestKey 约定发现唯一的 manifest 对象，
// 解析并校验其自报 manifest_id 与键前缀一致，返回该前缀。
func discoverManifestID(sink map[string][]byte) (string, error) {
	suffix := "/" + wholeindex.ManifestObject
	var found []string
	for key := range sink {
		if len(key) > len(suffix) && key[len(key)-len(suffix):] == suffix {
			found = append(found, key)
		}
	}
	sort.Strings(found)
	if len(found) != 1 {
		return "", fmt.Errorf("retrieval: sink 中 manifest 对象（%s）数量为 %d，期望恰好 1", suffix, len(found))
	}
	key := found[0]
	manifestID := key[:len(key)-len(suffix)]
	var m wholeindex.WholeDocIndexManifest
	if err := json.Unmarshal(sink[key], &m); err != nil {
		return "", fmt.Errorf("retrieval: 解析 manifest 对象 %s: %w", key, err)
	}
	if m.ManifestID != manifestID {
		return "", fmt.Errorf("retrieval: manifest 对象 %s 的 manifest_id %q 与键前缀不一致", key, m.ManifestID)
	}
	if m.ManifestID != wholeindex.ManifestID(m) {
		return "", fmt.Errorf("retrieval: manifest 对象 %s 的 manifest_id 与内容重算不一致", key)
	}
	return manifestID, nil
}

// loadDoc 解码单文档的三路载荷。
func loadDoc(sink map[string][]byte, manifestID string, e wholeindex.DocEntry) (LoadedDoc, error) {
	denseBytes, ok := sink[wholeindex.ObjectKey(manifestID, e.DenseRef)]
	if !ok {
		return LoadedDoc{}, fmt.Errorf("缺少 dense 对象 %s", wholeindex.ObjectKey(manifestID, e.DenseRef))
	}
	dim := len(denseBytes) - wholeindex.ScaleHeaderBytes
	dense := wholeindex.DequantizeI8(denseBytes, dim)
	if dense == nil || dim <= 0 {
		return LoadedDoc{}, fmt.Errorf("dense 对象 %s 形状非法（dim=%d）", wholeindex.ObjectKey(manifestID, e.DenseRef), dim)
	}

	sparse := map[uint32]float32{}
	if sparseBytes, ok := sink[wholeindex.ObjectKey(manifestID, e.SparseRef)]; ok {
		terms, err := wholeindex.DecodeImpact(sparseBytes)
		if err != nil {
			return LoadedDoc{}, fmt.Errorf("sparse 对象 %s: %w", wholeindex.ObjectKey(manifestID, e.SparseRef), err)
		}
		for _, t := range terms {
			sparse[t.TermID] = float32(t.Weight)
		}
	} else {
		return LoadedDoc{}, fmt.Errorf("缺少 sparse 对象 %s", wholeindex.ObjectKey(manifestID, e.SparseRef))
	}

	var multi [][]float32
	if e.MultiTokens < 1 {
		return LoadedDoc{}, fmt.Errorf("multi_tokens=%d 非法（manifest 契约要求 [1,2048]）", e.MultiTokens)
	}
	multiBytes, ok := sink[wholeindex.ObjectKey(manifestID, e.MultiRef)]
	if !ok {
		return LoadedDoc{}, fmt.Errorf("缺少 multi 对象 %s", wholeindex.ObjectKey(manifestID, e.MultiRef))
	}
	payload := len(multiBytes) - wholeindex.ScaleHeaderBytes
	if payload <= 0 || payload%e.MultiTokens != 0 {
		return LoadedDoc{}, fmt.Errorf("multi 对象 %s 形状非法（payload=%d tokens=%d）",
			wholeindex.ObjectKey(manifestID, e.MultiRef), payload, e.MultiTokens)
	}
	mDim := payload / e.MultiTokens
	mat := wholeindex.DequantizeMulti(multiBytes, e.MultiTokens, mDim)
	if mat == nil {
		return LoadedDoc{}, fmt.Errorf("multi 对象 %s 反量化失败", wholeindex.ObjectKey(manifestID, e.MultiRef))
	}
	multi = make([][]float32, e.MultiTokens)
	for r := range multi {
		multi[r] = mat[r*mDim : (r+1)*mDim]
	}
	return LoadedDoc{Dense: dense, Sparse: sparse, Multi: multi}, nil
}
