package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
)

// ============================================================================
// 镜像一致性黄金向量：以下期望值由 async 服务侧真实的
// service/async/rpc/internal/artifact 包产出（2026-10-06，integration
// 分支 eb2c9a9）。两侧实现必须逐字节一致；artifact 侧改动后请重新生成
// 并同步本处（详见 artifactmirror.go 的双边同步声明）。
// ============================================================================

func TestMirrorMatchesArtifactGoldens(t *testing.T) {
	v := []float32{1, -0.5, 0.25}
	if got := hex.EncodeToString(QuantizeF32(v)); got != "0000fe427fc020" {
		t.Fatalf("QuantizeF32 黄金向量不符: %s", got)
	}
	back := DequantizeI8(QuantizeF32(v), len(v))
	want := []float32{1, -0.503937, 0.2519685}
	for i := range want {
		if math.Abs(float64(back[i]-want[i])) > 1e-6 {
			t.Fatalf("DequantizeI8[%d]=%v want %v", i, back[i], want[i])
		}
	}

	terms := []Term{{TermID: 7, Weight: 255}, {TermID: 3, Weight: 128}}
	if got := hex.EncodeToString(EncodeImpact(terms)); got != "07000000ff0300000080" {
		t.Fatalf("EncodeImpact 黄金向量不符: %s", got)
	}
	decoded, err := DecodeImpact(EncodeImpact(terms))
	if err != nil || len(decoded) != 2 || decoded[0] != terms[0] || decoded[1] != terms[1] {
		t.Fatalf("DecodeImpact 往返不符: %v err=%v", decoded, err)
	}

	mat := []float32{1, -0.5, 0.25, -0.125}
	if got := hex.EncodeToString(QuantizeMulti(mat, 2)); got != "0000fe427fc020f0" {
		t.Fatalf("QuantizeMulti 黄金向量不符: %s", got)
	}
	mback := DequantizeMulti(QuantizeMulti(mat, 2), 2, 2)
	wantM := []float32{1, -0.503937, 0.2519685, -0.12598425}
	for i := range wantM {
		if math.Abs(float64(mback[i]-wantM[i])) > 1e-6 {
			t.Fatalf("DequantizeMulti[%d]=%v want %v", i, mback[i], wantM[i])
		}
	}

	m := WholeDocIndexManifest{
		ModuleID:  "sea-search-dev",
		ReleaseID: "retr-eval-v1",
		Docs: []DocEntry{{
			DocKey:       "doc-00",
			StructureRef: "structure/doc-00",
			DenseRef:     "dense.v1:aa",
			SparseRef:    "sparse.v1:bb",
			MultiRef:     "multi.v1:cc",
			MultiTokens:  2,
			EncoderID:    "fake-encoder.v1",
			SourceChars:  100,
			BudgetBytes:  42,
		}},
	}
	const canonicalGolden = `{"module_id":"sea-search-dev","release_id":"retr-eval-v1","docs":[{"doc_key":"doc-00","structure_ref":"structure/doc-00","dense_ref":"dense.v1:aa","sparse_ref":"sparse.v1:bb","multi_ref":"multi.v1:cc","multi_tokens":2,"encoder_id":"fake-encoder.v1","source_chars":100,"budget_bytes":42}]}`
	if string(CanonicalJSON(m)) != canonicalGolden {
		t.Fatalf("CanonicalJSON 黄金向量不符:\n%s", CanonicalJSON(m))
	}
	if ManifestID(m) != "5565980775f4a3520954756913496733" {
		t.Fatalf("ManifestID 黄金值不符: %s", ManifestID(m))
	}

	if ObjectKey("mid", "dense.v1:aa") != "mid/dense.v1:aa" {
		t.Fatal("ObjectKey 镜像不符")
	}
	if ManifestKey("mid") != "mid/manifest.v1.json" || TreeKey("mid") != "mid/tree.v1.json" {
		t.Fatal("固定名对象键镜像不符")
	}
}

// ============================================================================
// Load 解码往返。
// ============================================================================

// buildSink 按 indexer 的键约定组装一个单 manifest 工件集合：三路载荷
// 内容寻址 + manifest 对象（载荷键在 manifest_id 确定后统一落位）。
func buildSink(t *testing.T, docs []struct {
	key   string
	dense []float32
	terms []Term
	multi []float32
	dim   int
}) (map[string][]byte, WholeDocIndexManifest) {
	t.Helper()
	type laneBytes struct {
		dense, sparse, multi []byte
	}
	payloads := make([]laneBytes, len(docs))
	entries := make([]DocEntry, len(docs))
	for i, d := range docs {
		dense := QuantizeF32(d.dense)
		sparse := EncodeImpact(d.terms)
		multi := QuantizeMulti(d.multi, d.dim)
		if multi == nil {
			t.Fatalf("doc %s multi 量化失败", d.key)
		}
		payloads[i] = laneBytes{dense: dense, sparse: sparse, multi: multi}
		entries[i] = DocEntry{
			DocKey:       d.key,
			StructureRef: "structure/" + d.key,
			DenseRef:     payloadRef("dense", dense),
			SparseRef:    payloadRef("sparse", sparse),
			MultiRef:     payloadRef("multi", multi),
			MultiTokens:  len(d.multi) / d.dim,
			EncoderID:    "fake-encoder.v1",
			SourceChars:  100,
			BudgetBytes:  len(dense) + len(sparse) + len(multi),
		}
	}
	m := WholeDocIndexManifest{ModuleID: "sea-search-dev", ReleaseID: "retr-eval-v1", Docs: entries}
	AssignID(&m)
	sink := map[string][]byte{}
	for i := range entries {
		sink[ObjectKey(m.ManifestID, entries[i].DenseRef)] = payloads[i].dense
		sink[ObjectKey(m.ManifestID, entries[i].SparseRef)] = payloads[i].sparse
		sink[ObjectKey(m.ManifestID, entries[i].MultiRef)] = payloads[i].multi
	}
	mj, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	sink[ManifestKey(m.ManifestID)] = mj
	return sink, m
}

func TestLoadRoundTrip(t *testing.T) {
	docs := []struct {
		key   string
		dense []float32
		terms []Term
		multi []float32
		dim   int
	}{
		{
			key:   "doc-a",
			dense: []float32{0.5, -1.25, 3.0},
			terms: []Term{{TermID: 7, Weight: 255}, {TermID: 9, Weight: 128}},
			multi: []float32{1, -0.5, 0.25, -0.125, 0, 2},
			dim:   2,
		},
		{
			key:   "doc-b",
			dense: []float32{-0.1, 0.2, 0.3},
			terms: []Term{},
			multi: []float32{0.75, -0.75},
			dim:   2,
		},
	}
	sink, m := buildSink(t, docs)
	store, err := Load(sink, m.Docs)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sn := store.Snapshot()
	if sn.Len() != 2 {
		t.Fatalf("Len=%d want 2", sn.Len())
	}
	for i, d := range docs {
		got, ok := sn.Doc(d.key)
		if !ok {
			t.Fatalf("缺少文档 %s", d.key)
		}
		if len(got.Dense) != len(d.dense) {
			t.Fatalf("%s dense 维数 %d want %d", d.key, len(got.Dense), len(d.dense))
		}
		maxAbs := 0.0
		for _, x := range d.dense {
			maxAbs = math.Max(maxAbs, math.Abs(float64(x)))
		}
		for j, x := range d.dense {
			// 契约界：往返误差 ≤ max|v|/127 + 1e-6（artifact README）。
			if e := math.Abs(float64(got.Dense[j] - x)); e > maxAbs/127+1e-6 {
				t.Fatalf("%s dense[%d] 往返误差 %g 超界", d.key, j, e)
			}
		}
		wantSparse := map[uint32]float32{}
		for _, tm := range d.terms {
			wantSparse[tm.TermID] = float32(tm.Weight)
		}
		if len(got.Sparse) != len(wantSparse) {
			t.Fatalf("%s sparse 条数 %d want %d", d.key, len(got.Sparse), len(wantSparse))
		}
		for id, w := range wantSparse {
			if got.Sparse[id] != w {
				t.Fatalf("%s sparse[%d]=%v want %v", d.key, id, got.Sparse[id], w)
			}
		}
		rows := len(d.multi) / d.dim
		if len(got.Multi) != rows {
			t.Fatalf("%s multi 行数 %d want %d", d.key, len(got.Multi), rows)
		}
		maxAbsM := 0.0
		for _, x := range d.multi {
			maxAbsM = math.Max(maxAbsM, math.Abs(float64(x)))
		}
		for r := 0; r < rows; r++ {
			for c := 0; c < d.dim; c++ {
				orig := d.multi[r*d.dim+c]
				if e := math.Abs(float64(got.Multi[r][c] - orig)); e > maxAbsM/127+1e-6 {
					t.Fatalf("%s multi[%d][%d] 往返误差 %g 超界", d.key, r, c, e)
				}
			}
		}
		_ = i
	}
	// Tree/Source 装载后为空，等待 Attach。
	if g, _ := sn.Doc("doc-a"); len(g.Source) != 0 || g.Tree.RevisionID != "" {
		t.Fatal("Load 不应填充 Tree/Source")
	}
}

// payloadRef 镜像 indexer 的内容寻址 ref：lane.v1:hex(sha256(b))[:32]。
func payloadRef(lane string, b []byte) string {
	sum := sha256.Sum256(b)
	return lane + ".v1:" + hex.EncodeToString(sum[:16])
}

func TestLoadErrors(t *testing.T) {
	docs := []struct {
		key   string
		dense []float32
		terms []Term
		multi []float32
		dim   int
	}{
		{key: "doc-a", dense: []float32{1, 2}, terms: []Term{{TermID: 1, Weight: 10}}, multi: []float32{1, 1}, dim: 2},
	}
	sink, m := buildSink(t, docs)

	if _, err := Load(map[string][]byte{}, m.Docs); err == nil {
		t.Fatal("空 sink 应报错")
	}
	if _, err := Load(sink, nil); err == nil {
		t.Fatal("空 entries 应报错")
	}

	// 删除 dense 对象 → 报错并指明缺失键。
	drop := ObjectKey(m.ManifestID, m.Docs[0].DenseRef)
	deleted := sink[drop]
	delete(sink, drop)
	if _, err := Load(sink, m.Docs); err == nil {
		t.Fatal("缺 dense 对象应报错")
	}
	sink[drop] = deleted

	// 两个 manifest 对象 → 报错（dev 形态一次只装一个 manifest）。
	sink["another-manifest/"+manifestObject] = []byte(`{"manifest_id":"x"}`)
	if _, err := Load(sink, m.Docs); err == nil {
		t.Fatal("多 manifest 对象应报错")
	}
	delete(sink, "another-manifest/"+manifestObject)

	// manifest_id 与键前缀不一致 → 报错。
	bad := `{"manifest_id":"deadbeef"}`
	good := sink[ManifestKey(m.ManifestID)]
	sink["other-id/"+manifestObject] = []byte(bad)
	delete(sink, ManifestKey(m.ManifestID))
	if _, err := Load(sink, m.Docs); err == nil {
		t.Fatal("manifest_id 与前缀不一致应报错")
	}
	sink[ManifestKey(m.ManifestID)] = good

	// 重复 docKey → 报错。
	if _, err := Load(sink, []DocEntry{m.Docs[0], m.Docs[0]}); err == nil {
		t.Fatal("重复 docKey 应报错")
	}

	// MultiTokens=0 → 报错（manifest 契约 [1,2048]）。
	badEntries := []DocEntry{m.Docs[0]}
	badEntries[0].MultiTokens = 0
	if _, err := Load(sink, badEntries); err == nil {
		t.Fatal("multi_tokens=0 应报错")
	}
}

// ============================================================================
// NewStore / AttachSource / Snapshot。
// ============================================================================

func testTreeAndSource(t *testing.T, rev, title string, paras []string) (evidence.TreeJSON, []byte) {
	t.Helper()
	src := "# " + title + "\n\n" + joinParas(paras)
	tree := evidence.TreeJSON{RevisionID: rev, Nodes: []evidence.NodeJSON{{
		NodeID: "h0", Level: 1, Title: title, ParaIndex: evidence.HeadingParaIndex,
		CharStart: 0, CharEnd: len("# " + title),
	}}}
	off := len("# " + title)
	for i, p := range paras {
		off += 2 // "\n\n"
		tree.Nodes = append(tree.Nodes, evidence.NodeJSON{
			NodeID: paraNodeID(rev, i), Level: evidence.LevelParagraph, ParaIndex: i,
			CharStart: off, CharEnd: off + len(p),
		})
		off += len(p)
	}
	if off != len(src)-1 { // joinParas 追加一个尾换行
		t.Fatalf("fixture 偏移漂移：off=%d len(src)=%d", off, len(src))
	}
	return tree, []byte(src)
}

func joinParas(paras []string) string {
	out := ""
	for i, p := range paras {
		if i > 0 {
			out += "\n\n"
		}
		out += p
	}
	return out + "\n"
}

func paraNodeID(rev string, i int) string {
	return rev + "-p" + string(rune('0'+i))
}

func TestNewStoreValidation(t *testing.T) {
	if _, err := NewStore(nil); err == nil {
		t.Fatal("空 docs 应报错")
	}
	if _, err := NewStore(map[string]LoadedDoc{"": {Dense: []float32{1}}}); err == nil {
		t.Fatal("空 docKey 应报错")
	}
	if _, err := NewStore(map[string]LoadedDoc{"a": {}}); err == nil {
		t.Fatal("缺 dense 应报错")
	}
	if _, err := NewStore(map[string]LoadedDoc{"a": {Dense: []float32{1}, Multi: [][]float32{{1, 0}, {1}}}}); err == nil {
		t.Fatal("multi 各行不等宽应报错")
	}
}

func TestAttachSourceAndSnapshot(t *testing.T) {
	tree, src := testTreeAndSource(t, "rev-a", "标题", []string{"第一段。", "第二段。"})
	store, err := NewStore(map[string]LoadedDoc{"doc-a": {Dense: []float32{1, 0}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSource("doc-b", tree, src); err == nil {
		t.Fatal("未知 docKey 应报错")
	}
	if err := store.AttachSource("doc-a", evidence.TreeJSON{}, src); err == nil {
		t.Fatal("缺 revision_id 的树应报错")
	}
	if err := store.AttachSource("doc-a", tree, nil); err == nil {
		t.Fatal("空源文本应报错")
	}
	if err := store.AttachSource("doc-a", tree, src); err != nil {
		t.Fatalf("AttachSource: %v", err)
	}
	sn := store.Snapshot()
	d, ok := sn.Doc("doc-a")
	if !ok || d.Tree.RevisionID != "rev-a" || string(d.Source) != string(src) {
		t.Fatalf("Snapshot 内容不符: %+v", d.Tree)
	}
	keys := sn.DocKeys()
	if len(keys) != 1 || keys[0] != "doc-a" {
		t.Fatalf("DocKeys=%v", keys)
	}
	// Attach 拷贝源文本：外层修改不影响 Store。
	src[0] = 'X'
	d2, _ := store.Snapshot().Doc("doc-a")
	if d2.Source[0] == 'X' {
		t.Fatal("AttachSource 应拷贝源文本")
	}
}
