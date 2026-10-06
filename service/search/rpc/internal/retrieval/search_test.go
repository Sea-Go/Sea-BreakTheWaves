package retrieval

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
)

// newSearchFixture 构造带结构树/源文本的检索器：每文档两段正文，首段
// "「docKey」第一段。"（用于断言 dev 命中定位取首段）。
func newSearchFixture(t *testing.T, docs map[string]LoadedDoc, topN int) *Searcher {
	t.Helper()
	for key, d := range docs {
		tree, src := testTreeAndSource(t, "rev-"+key, key+" 的标题", []string{
			"「" + key + "」第一段。",
			"「" + key + "」第二段。",
		})
		d.Tree = tree
		d.Source = src
		docs[key] = d
	}
	store, err := NewStore(docs)
	if err != nil {
		t.Fatal(err)
	}
	return &Searcher{store: store, TopN: topN}
}

func packKeys(pack evidence.EvidencePack) []string {
	keys := make([]string, 0, len(pack.Candidates))
	for _, c := range pack.Candidates {
		keys = append(keys, c.DocKey)
	}
	return keys
}

func contains(list []string, key string) bool {
	for _, k := range list {
		if k == key {
			return true
		}
	}
	return false
}

// 探针 fake：doc-c 只能经 multi 路浮出（dense 余弦 0、sparse 无交集、
// multi 与查询 token 平行）。fast 档跳 multi → doc-c 必不出现；balanced
// 档三路 → doc-c 出现且 Lanes.Multi>0。
func TestSearchFastSkipsMultiLane(t *testing.T) {
	sh := newSearchFixture(t, map[string]LoadedDoc{
		"doc-a": {Dense: []float32{1, 0}, Sparse: map[uint32]float32{7: 3}},
		"doc-b": {Dense: []float32{0, 1}},
		"doc-c": {Dense: []float32{0, 1}, Multi: [][]float32{{1, 0}}}, // 唯一浮出来路：multi
	}, 50)
	req := Request{
		QueryID: "q-1",
		Dense:   []float32{1, 0},
		Sparse:  map[uint32]float32{7: 2},
		Multi:   [][]float32{{1, 0}},
	}

	fastPack, err := sh.Search(context.Background(), Request{QueryID: "q-1", Tier: TierFast,
		Dense: req.Dense, Sparse: req.Sparse, Multi: req.Multi})
	if err != nil {
		t.Fatalf("fast: %v", err)
	}
	if contains(packKeys(fastPack), "doc-c") {
		t.Fatalf("fast 档不应含 multi-only 候选 doc-c: %v", packKeys(fastPack))
	}
	for _, c := range fastPack.Candidates {
		if c.Lanes.Multi != 0 {
			t.Fatalf("fast 档候选 %s 不应有 multi 路分数: %+v", c.DocKey, c.Lanes)
		}
	}

	balPack, err := sh.Search(context.Background(), Request{QueryID: "q-1", Tier: TierBalanced,
		Dense: req.Dense, Sparse: req.Sparse, Multi: req.Multi})
	if err != nil {
		t.Fatalf("balanced: %v", err)
	}
	if !contains(packKeys(balPack), "doc-c") {
		t.Fatalf("balanced 档应含 multi-only 候选 doc-c: %v", packKeys(balPack))
	}
	for _, c := range balPack.Candidates {
		if c.DocKey == "doc-c" && c.Lanes.Multi <= 0 {
			t.Fatalf("doc-c 应有正的 multi 分数: %+v", c.Lanes)
		}
	}

	// deep 档同样含 doc-c（三路）。
	deepPack, err := sh.Search(context.Background(), Request{QueryID: "q-1", Tier: TierDeep,
		Dense: req.Dense, Sparse: req.Sparse, Multi: req.Multi})
	if err != nil {
		t.Fatalf("deep: %v", err)
	}
	if !contains(packKeys(deepPack), "doc-c") {
		t.Fatalf("deep 档应含 doc-c: %v", packKeys(deepPack))
	}
}

// 探针：TopN=1 时，dense 路排第 2 的文档在 fast/balanced 被截断，仅
// deep 档（路内候选 ×2 模拟放宽）能浮出。
func TestSearchDeepDoublesLaneCandidates(t *testing.T) {
	sh := newSearchFixture(t, map[string]LoadedDoc{
		"d1": {Dense: []float32{1, 0}},       // cos 1.0
		"d2": {Dense: []float32{0.8, 0.6}},   // cos 0.8 ← deep 档才能浮出
		"d3": {Dense: []float32{0.6, 0.8}},   // cos 0.6
		"d4": {Dense: []float32{0.28, 0.96}}, // cos 0.28
	}, 1)
	req := Request{QueryID: "q-2", Dense: []float32{1, 0}}

	for _, tier := range []Tier{TierFast, TierBalanced} {
		pack, err := sh.Search(context.Background(), Request{QueryID: req.QueryID, Tier: tier, Dense: req.Dense})
		if err != nil {
			t.Fatalf("%s: %v", tier, err)
		}
		if keys := packKeys(pack); len(keys) != 1 || keys[0] != "d1" {
			t.Fatalf("%s 档应只含 d1: %v", tier, keys)
		}
	}
	deepPack, err := sh.Search(context.Background(), Request{QueryID: req.QueryID, Tier: TierDeep, Dense: req.Dense})
	if err != nil {
		t.Fatalf("deep: %v", err)
	}
	if keys := packKeys(deepPack); len(keys) != 2 || keys[0] != "d1" || keys[1] != "d2" {
		t.Fatalf("deep 档应含 [d1 d2]: %v", keys)
	}
}

// dev 近似命中定位：候选证据 = 文档第一个段落节点整段（quote 即首段文本），
// section_path 为文档标题链；RRF 排序降序。
func TestSearchEvidenceFromFirstParagraph(t *testing.T) {
	sh := newSearchFixture(t, map[string]LoadedDoc{
		"doc-a": {Dense: []float32{1, 0}},
		"doc-b": {Dense: []float32{0.5, 0.5}},
	}, 50)
	pack, err := sh.Search(context.Background(), Request{QueryID: "q-3", Tier: TierBalanced, Dense: []float32{1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Candidates) != 2 {
		t.Fatalf("候选数 %d want 2", len(pack.Candidates))
	}
	if pack.Candidates[0].DocKey != "doc-a" || pack.Candidates[1].DocKey != "doc-b" {
		t.Fatalf("RRF 排序不符: %v", packKeys(pack))
	}
	for i, wantKey := range []string{"doc-a", "doc-b"} {
		c := pack.Candidates[i]
		if c.RRFScore <= 0 || (i == 0 && c.RRFScore <= pack.Candidates[1].RRFScore) {
			t.Fatalf("RRF 分数排序不符: %v", pack.Candidates)
		}
		if len(c.Evidence) != 1 {
			t.Fatalf("候选 %s 证据数 %d want 1（dev 取首段）", wantKey, len(c.Evidence))
		}
		loc := c.Evidence[0]
		if loc.Quote != "「"+wantKey+"」第一段。" {
			t.Fatalf("quote 应为首段整段: %q", loc.Quote)
		}
		if loc.ParaIndex != 0 {
			t.Fatalf("para_index=%d want 0", loc.ParaIndex)
		}
		if len(loc.SectionPath) != 1 || loc.SectionPath[0] != wantKey+" 的标题" {
			t.Fatalf("section_path 不符: %v", loc.SectionPath)
		}
		if loc.RevisionID != "rev-"+wantKey {
			t.Fatalf("revision 不符: %q", loc.RevisionID)
		}
		if c.Lanes.Dense <= 0 || c.Lanes.Sparse != 0 || c.Lanes.Multi != 0 || c.Lanes.Rerank != 0 {
			t.Fatalf("路分数快照不符: %+v", c.Lanes)
		}
	}
	if err := pack.Validate(); err != nil {
		t.Fatalf("EvidencePack 校验: %v", err)
	}
}

// 请求契约：未知档位 / 三件套全空 / query_id 空。
func TestSearchRequestValidation(t *testing.T) {
	sh := newSearchFixture(t, map[string]LoadedDoc{
		"doc-a": {Dense: []float32{1, 0}},
	}, 50)
	ctx := context.Background()

	if _, err := sh.Search(ctx, Request{QueryID: "q", Tier: Tier("turbo"), Dense: []float32{1, 0}}); !errors.Is(err, ErrInvalidTier) {
		t.Fatalf("未知档位应报 ErrInvalidTier: %v", err)
	}
	if _, err := sh.Search(ctx, Request{QueryID: "q", Tier: TierFast}); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("空查询应报 ErrEmptyQuery: %v", err)
	}
	if _, err := sh.Search(ctx, Request{Tier: TierFast, Dense: []float32{1, 0}}); err == nil {
		t.Fatal("空 query_id 应报错")
	}

	// 候选缺结构树（未 AttachSource）：dev 命中定位无法构造，报错。
	bare, err := NewStore(map[string]LoadedDoc{"doc-a": {Dense: []float32{1, 0}}})
	if err != nil {
		t.Fatal(err)
	}
	bareSh := NewSearcher(bare)
	if _, err := bareSh.Search(ctx, Request{QueryID: "q", Tier: TierFast, Dense: []float32{1, 0}}); err == nil {
		t.Fatal("缺结构树应报错")
	}

	// context 取消立即失败。
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := sh.Search(cancelled, Request{QueryID: "q", Tier: TierFast, Dense: []float32{1, 0}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 ctx 应返回 Canceled: %v", err)
	}
}

// 同输入两次 Search 产出深相等的 pack（确定性）。
func TestSearchDeterministic(t *testing.T) {
	sh := newSearchFixture(t, map[string]LoadedDoc{
		"doc-a": {Dense: []float32{1, 0}, Sparse: map[uint32]float32{7: 3}, Multi: [][]float32{{1, 0}}},
		"doc-b": {Dense: []float32{0.9, 0.1}, Sparse: map[uint32]float32{7: 1}, Multi: [][]float32{{0.9, 0.1}}},
		"doc-c": {Dense: []float32{0, 1}, Multi: [][]float32{{0, 1}}},
	}, 50)
	req := Request{QueryID: "q-det", Tier: TierBalanced,
		Dense: []float32{1, 0}, Sparse: map[uint32]float32{7: 2}, Multi: [][]float32{{1, 0}}}
	first, err := sh.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := sh.Search(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Candidates) != len(again.Candidates) {
			t.Fatalf("第 %d 次候选数漂移", i)
		}
		for j := range first.Candidates {
			a, b := first.Candidates[j], again.Candidates[j]
			if a.DocKey != b.DocKey || a.RRFScore != b.RRFScore || a.Lanes != b.Lanes {
				t.Fatalf("第 %d 次候选漂移: %+v vs %+v", i, a, b)
			}
			if len(a.Evidence) != len(b.Evidence) || !locatorEqual(a.Evidence[0], b.Evidence[0]) {
				t.Fatalf("第 %d 次证据漂移", i)
			}
		}
	}
}

// locatorEqual 逐字段比较两个 Locator（SectionPath 是切片，不能直接 ==）。
func locatorEqual(a, b evidence.Locator) bool {
	if a.RevisionID != b.RevisionID || a.ParaIndex != b.ParaIndex || a.Quote != b.Quote ||
		len(a.SectionPath) != len(b.SectionPath) {
		return false
	}
	for i := range a.SectionPath {
		if a.SectionPath[i] != b.SectionPath[i] {
			return false
		}
	}
	return true
}

// 融合口径抽查：单文档双路命中时 RRF = 2/(KDefault+1)。
func TestSearchRRFScoreValue(t *testing.T) {
	sh := newSearchFixture(t, map[string]LoadedDoc{
		"doc-a": {Dense: []float32{1, 0}, Sparse: map[uint32]float32{7: 3}},
	}, 50)
	pack, err := sh.Search(context.Background(), Request{QueryID: "q-rrf", Tier: TierFast,
		Dense: []float32{1, 0}, Sparse: map[uint32]float32{7: 2}})
	if err != nil {
		t.Fatal(err)
	}
	want := float32(2.0 / (KDefault + 1))
	if math.Abs(float64(pack.Candidates[0].RRFScore-want)) > 1e-9 {
		t.Fatalf("RRF=%v want %v", pack.Candidates[0].RRFScore, want)
	}
}
