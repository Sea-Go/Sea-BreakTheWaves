// ============================================================================
// 该文件测试 internal/evidence 包：MapHitToLocator 的段落定位 / 标题链 /
// quote 截断，BuildPack 的组装 / 排序确定性 / 全有或全无，以及
// EvidencePack 校验与 JSON 镜像 tag。
//
// fixture 树按 RTW structure.Derive 的节点语义手工声明（标题整行、段落
// 整行、全局段落序、字节区间 [start,end)），每段的 section_path 为
// 手工推算的期望值（见 wantSectionPath）。
// ============================================================================

package evidence

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// fixture：多级标题 + 段落（含中文章节名）。
//
// 手工推算的各段 section_path（层级栈：level<=top 弹栈后压入）：
//   - 段落 0（任何标题之前）        → []
//   - 段落 1                        → [海洋学导论, 第一章 潮汐]
//   - 段落 2                        → [海洋学导论, 第一章 潮汐, 潮汐的成因]
//   - 段落 3（H2 弹掉 H3）          → [海洋学导论, 第二章 洋流]
//   - 段落 4（H1 弹掉 H2+H1）       → [结语]
// ---------------------------------------------------------------------------

const fixtureRevision = "rev-fixture-001"

type fixtureLine struct {
	text  string
	level int // 0=普通行（空白或段落），1..6=标题层级
}

var fixtureLines = []fixtureLine{
	{"全书概览在前，正文在后。", 0}, // 段落 0
	{"", 0},
	{"# 海洋学导论", 1},
	{"", 0},
	{"## 第一章 潮汐", 2},
	{"", 0},
	{"潮汐 是 海水 周期性 涨落 的 现象。", 0}, // 段落 1
	{"", 0},
	{"### 潮汐的成因", 3},
	{"", 0},
	{"月球 与 太阳 的 引潮力 是 主因。", 0}, // 段落 2
	{"", 0},
	{"## 第二章 洋流", 2},
	{"", 0},
	{"洋流 是 海水 的 大规模 定向 流动。", 0}, // 段落 3
	{"", 0},
	{"# 结语", 1},
	{"", 0},
	{"全文 完。", 0}, // 段落 4
}

// buildFixture 按行拼接源文本并同步派生节点字节区间（标题整行、段落
// 整行、空白行不产生节点、段落序全局递增），与 RTW Derive 语义一致。
func buildFixture() (string, TreeJSON) {
	var b strings.Builder
	tree := TreeJSON{RevisionID: fixtureRevision}
	paraIndex := 0
	seq := 0
	for _, ln := range fixtureLines {
		start, end := b.Len(), b.Len()+len(ln.text)
		b.WriteString(ln.text)
		b.WriteByte('\n')
		node := NodeJSON{NodeID: "fx" + string(rune('0'+seq)), CharStart: start, CharEnd: end}
		seq++
		switch {
		case ln.level >= 1 && ln.level <= 6:
			node.Level = ln.level
			node.Title = strings.TrimSpace(ln.text[strings.LastIndex(ln.text, "#")+1:])
			node.ParaIndex = HeadingParaIndex
		case strings.TrimSpace(ln.text) == "":
			continue // 空白行不产生节点
		default:
			node.Level = LevelParagraph
			node.ParaIndex = paraIndex
			paraIndex++
		}
		tree.Nodes = append(tree.Nodes, node)
	}
	return b.String(), tree
}

// spanOf 返回 src 中 needle 首次出现的字节区间（测试用命中）。
func spanOf(t *testing.T, src, needle string) HitSpan {
	t.Helper()
	i := strings.Index(src, needle)
	if i < 0 {
		t.Fatalf("fixture 中找不到 %q", needle)
	}
	return HitSpan{CharStart: i, CharEnd: i + len(needle)}
}

// wantSectionPath 手工推算的期望标题链（按段落在 fixture 中的顺序）。
var wantSectionPath = [][]string{
	{},
	{"海洋学导论", "第一章 潮汐"},
	{"海洋学导论", "第一章 潮汐", "潮汐的成因"},
	{"海洋学导论", "第二章 洋流"},
	{"结语"},
}

// TestMapHitToLocator_Paragraphs 正例：各段命中 → revision/para_index/
// section_path（含中文章节名）/quote 均正确。
func TestMapHitToLocator_Paragraphs(t *testing.T) {
	src, tree := buildFixture()
	cases := []struct {
		name   string
		needle string // 命中文本（fixture 内唯一）
		para   int
	}{
		{"段落0 命中（任何标题之前，路径为空）", "全书概览在前", 0},
		{"段落1 命中（二级章节）", "海水 周期性", 1},
		{"段落2 命中（三级章节）", "引潮力", 2},
		{"段落3 命中（H2 弹掉 H3 的回退路径）", "大规模", 3},
		{"段落4 命中（H1 弹掉 H2+H1 的回退路径）", "全文", 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sp := spanOf(t, src, c.needle)
			loc, err := MapHitToLocator(tree, []byte(src), sp.CharStart, sp.CharEnd)
			if err != nil {
				t.Fatalf("MapHitToLocator 报错: %v", err)
			}
			if loc.RevisionID != fixtureRevision {
				t.Fatalf("RevisionID = %q, want %q", loc.RevisionID, fixtureRevision)
			}
			if loc.ParaIndex != c.para {
				t.Fatalf("ParaIndex = %d, want %d", loc.ParaIndex, c.para)
			}
			if !reflect.DeepEqual(loc.SectionPath, wantSectionPath[c.para]) {
				t.Fatalf("SectionPath = %q, want %q", loc.SectionPath, wantSectionPath[c.para])
			}
			if loc.Quote != c.needle {
				t.Fatalf("Quote = %q, want %q", loc.Quote, c.needle)
			}
		})
	}
}

// TestMapHitToLocator_QuoteTrimSpace quote 取区间文本 TrimSpace 后的结果
// （区间两端混入的空白被去除）。
func TestMapHitToLocator_QuoteTrimSpace(t *testing.T) {
	src, tree := buildFixture()
	sp := spanOf(t, src, " 是 海水 周期性 涨落 ")
	loc, err := MapHitToLocator(tree, []byte(src), sp.CharStart, sp.CharEnd)
	if err != nil {
		t.Fatalf("MapHitToLocator 报错: %v", err)
	}
	if want := "是 海水 周期性 涨落"; loc.Quote != want {
		t.Fatalf("Quote = %q, want %q", loc.Quote, want)
	}
}

// TestMapHitToLocator_QuoteTruncate quote 超 200 rune 截断到前 200 rune；
// 恰好 200 rune 不截断。多字节文本截断必须 rune 安全。
func TestMapHitToLocator_QuoteTruncate(t *testing.T) {
	longPara := strings.Repeat("潮", 201) + "。" // 202 rune / 606 字节
	head := "# 长文\n\n"
	src := head + longPara + "\n"
	paraStart, paraEnd := len(head), len(head)+len(longPara)
	tree := TreeJSON{
		RevisionID: fixtureRevision,
		Nodes: []NodeJSON{
			{NodeID: "lx0", Level: 1, Title: "长文", ParaIndex: HeadingParaIndex, CharStart: 0, CharEnd: len("# 长文")},
			{NodeID: "lx1", Level: LevelParagraph, ParaIndex: 0, CharStart: paraStart, CharEnd: paraEnd},
		},
	}
	cases := []struct {
		name  string
		start int
		end   int
	}{
		{"整段 202 rune 截断到 200", paraStart, paraEnd},
		{"恰好 200 rune 边界不截断", paraStart, paraStart + 200*3},
		{"201 rune 截断到 200", paraStart, paraStart + 201*3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loc, err := MapHitToLocator(tree, []byte(src), c.start, c.end)
			if err != nil {
				t.Fatalf("MapHitToLocator 报错: %v", err)
			}
			if n := utf8.RuneCountInString(loc.Quote); n != MaxQuoteRunes {
				t.Fatalf("quote 长度 = %d rune, want %d", n, MaxQuoteRunes)
			}
			if want := strings.Repeat("潮", MaxQuoteRunes); loc.Quote != want {
				t.Fatalf("quote 截断结果与期望不符（多字节截断必须 rune 安全）")
			}
			if want := []string{"长文"}; !reflect.DeepEqual(loc.SectionPath, want) {
				t.Fatalf("SectionPath = %q, want %q", loc.SectionPath, want)
			}
		})
	}
}

// TestMapHitToLocator_CrossParagraph 反例：区间跨两个段落（穿过中间
// 标题）→ ErrHitCrossParagraph。
func TestMapHitToLocator_CrossParagraph(t *testing.T) {
	src, tree := buildFixture()
	start := spanOf(t, src, "海水").CharStart // 段落 1 内
	end := spanOf(t, src, "引潮力").CharEnd    // 段落 2 内
	_, err := MapHitToLocator(tree, []byte(src), start, end)
	if !errors.Is(err, ErrHitCrossParagraph) {
		t.Fatalf("err = %v, want ErrHitCrossParagraph", err)
	}
	if !strings.Contains(err.Error(), "命中跨段，请按段细分") {
		t.Fatalf("错误文案缺关键提示: %v", err)
	}
}

// TestMapHitToLocator_OnHeading 反例：区间落在标题行上（整行或局部）
// → ErrHitCrossParagraph（标题永不承载证据）。
func TestMapHitToLocator_OnHeading(t *testing.T) {
	src, tree := buildFixture()
	for _, needle := range []string{"## 第二章 洋流", "第二章"} {
		sp := spanOf(t, src, needle)
		if _, err := MapHitToLocator(tree, []byte(src), sp.CharStart, sp.CharEnd); !errors.Is(err, ErrHitCrossParagraph) {
			t.Fatalf("needle %q: err = %v, want ErrHitCrossParagraph", needle, err)
		}
	}
}

// TestMapHitToLocator_InBlank 反例：区间落在节点之间的空白带上 →
// ErrHitCrossParagraph。
func TestMapHitToLocator_InBlank(t *testing.T) {
	src, tree := buildFixture()
	sp := spanOf(t, src, "\n\n") // 段落 0 行尾与 H1 行之间的空行
	if _, err := MapHitToLocator(tree, []byte(src), sp.CharStart, sp.CharEnd); !errors.Is(err, ErrHitCrossParagraph) {
		t.Fatalf("err = %v, want ErrHitCrossParagraph", err)
	}
}

// TestMapHitToLocator_Bounds 反例：区间越界 / 为空 / 树缺 revision_id /
// 空白文本。
func TestMapHitToLocator_Bounds(t *testing.T) {
	src, tree := buildFixture()
	source := []byte(src)
	inside := spanOf(t, src, "海水")
	if _, err := MapHitToLocator(tree, source, 0, len(src)+1); err == nil || !strings.Contains(err.Error(), "越界") {
		t.Fatalf("越界区间应报错, got %v", err)
	}
	if _, err := MapHitToLocator(tree, source, -1, inside.CharEnd); err == nil || !strings.Contains(err.Error(), "越界") {
		t.Fatalf("负偏移应报错, got %v", err)
	}
	if _, err := MapHitToLocator(tree, source, inside.CharStart, inside.CharStart); err == nil || !strings.Contains(err.Error(), "为空") {
		t.Fatalf("空区间应报错, got %v", err)
	}
	noRev := tree
	noRev.RevisionID = ""
	if _, err := MapHitToLocator(noRev, source, inside.CharStart, inside.CharEnd); err == nil || !strings.Contains(err.Error(), "revision_id") {
		t.Fatalf("缺 revision_id 应报错, got %v", err)
	}
	// 区间合法落在段落内：两端空白被 TrimSpace 去掉。
	spacy := "  潮汐。  "
	spacyTree := TreeJSON{RevisionID: fixtureRevision, Nodes: []NodeJSON{
		{NodeID: "s0", Level: LevelParagraph, ParaIndex: 0, CharStart: 0, CharEnd: len(spacy)},
	}}
	loc, err := MapHitToLocator(spacyTree, []byte(spacy), 0, len(spacy))
	if err != nil || loc.Quote != "潮汐。" {
		t.Fatalf("两端空白应被 TrimSpace, got (%+v, %v)", loc, err)
	}
	// 纯空白文本无法构造有效 quote。
	blank := "  \t "
	if _, err := MapHitToLocator(spacyTree, []byte(blank), 0, len(blank)); err == nil || !strings.Contains(err.Error(), "空白") {
		t.Fatalf("纯空白命中应报错, got %v", err)
	}
}

// TestBuildPack_HappyPath 组装 + 排序：RRFScore 降序、平局按 doc_key、
// lanes/分数透传（rerank=0 合法）、evidence 逐 hit 映射。
func TestBuildPack_HappyPath(t *testing.T) {
	src, tree := buildFixture()
	source := []byte(src)
	doc := func(key string, score float32, lanes LaneScores, needles ...string) DocHit {
		h := DocHit{DocKey: key, RevisionID: fixtureRevision, RRFScore: score, Lanes: lanes, Tree: tree, Source: source}
		for _, n := range needles {
			h.Hits = append(h.Hits, spanOf(t, src, n))
		}
		return h
	}
	hits := []DocHit{
		doc("x/低分", 0.01, LaneScores{Dense: 0.1, Sparse: 0.1, Multi: 0.1}, "全文"),
		doc("b/平局", 0.02, LaneScores{Dense: 0.2, Sparse: 0.2, Multi: 0.2}, "全书概览在前"),
		doc("a/平局", 0.02, LaneScores{Dense: 0.2, Sparse: 0.2, Multi: 0.2}, "全书概览在前"),
		doc("wiki/洋流", 0.032, LaneScores{Dense: 0.6, Sparse: 0.5, Multi: 0.4}, "大规模"),
		doc("wiki/潮汐", 0.045, LaneScores{Dense: 0.9, Sparse: 0.8, Multi: 0.7, Rerank: 0.6}, "海水 周期性", "引潮力"),
	}
	pack, err := BuildPack("q-001", hits)
	if err != nil {
		t.Fatalf("BuildPack 报错: %v", err)
	}
	wantOrder := []string{"wiki/潮汐", "wiki/洋流", "a/平局", "b/平局", "x/低分"}
	gotOrder := make([]string, 0, len(pack.Candidates))
	for _, c := range pack.Candidates {
		gotOrder = append(gotOrder, c.DocKey)
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("候选顺序 = %q, want %q", gotOrder, wantOrder)
	}
	top := pack.Candidates[0]
	if top.RRFScore != 0.045 || top.Lanes.Rerank != 0.6 {
		t.Fatalf("分数/lanes 未透传: %+v", top)
	}
	if len(top.Evidence) != 2 {
		t.Fatalf("top 候选 evidence = %d 条, want 2", len(top.Evidence))
	}
	for i, wantPara := range []int{1, 2} {
		loc := top.Evidence[i]
		if loc.ParaIndex != wantPara {
			t.Fatalf("evidence[%d].ParaIndex = %d, want %d", i, loc.ParaIndex, wantPara)
		}
		if !reflect.DeepEqual(loc.SectionPath, wantSectionPath[wantPara]) {
			t.Fatalf("evidence[%d].SectionPath = %q, want %q", i, loc.SectionPath, wantSectionPath[wantPara])
		}
	}
	if rerankZero := pack.Candidates[2]; rerankZero.DocKey != "a/平局" || rerankZero.Lanes.Rerank != 0 {
		t.Fatalf("rerank=0（无 rerank）应合法且保留: %+v", rerankZero)
	}
	if err := pack.Validate(); err != nil {
		t.Fatalf("合法包不应校验失败: %v", err)
	}
}

// TestBuildPack_Deterministic 排序确定性：同一输入集合以不同顺序传入，
// 产出完全相同的包（含平局候选的稳定次序）。
func TestBuildPack_Deterministic(t *testing.T) {
	src, tree := buildFixture()
	source := []byte(src)
	doc := func(key string, score float32, needles ...string) DocHit {
		h := DocHit{DocKey: key, RevisionID: fixtureRevision, RRFScore: score, Tree: tree, Source: source}
		for _, n := range needles {
			h.Hits = append(h.Hits, spanOf(t, src, n))
		}
		return h
	}
	hits := []DocHit{
		doc("a", 0.01, "全文"),
		doc("d", 0.03, "大规模"),
		doc("b", 0.01, "全书概览在前"),
		doc("e", 0.05, "海水 周期性", "引潮力"),
		doc("c", 0.01, "引潮力"),
	}
	pack1, err := BuildPack("q-det", hits)
	if err != nil {
		t.Fatalf("BuildPack 报错: %v", err)
	}
	// 逆序重排输入再组装一次。
	for i, j := 0, len(hits)-1; i < j; i, j = i+1, j-1 {
		hits[i], hits[j] = hits[j], hits[i]
	}
	pack2, err := BuildPack("q-det", hits)
	if err != nil {
		t.Fatalf("BuildPack 报错: %v", err)
	}
	if !reflect.DeepEqual(pack1, pack2) {
		t.Fatalf("相同输入集合产出不同包:\n%+v\n%+v", pack1, pack2)
	}
	j1, _ := json.Marshal(pack1)
	j2, _ := json.Marshal(pack2)
	if string(j1) != string(j2) {
		t.Fatalf("JSON 不一致:\n%s\n%s", j1, j2)
	}
}

// TestBuildPack_AllOrNothing 全有或全无：任一 hit 失败整包失败，
// 不返回部分证据。
func TestBuildPack_AllOrNothing(t *testing.T) {
	src, tree := buildFixture()
	source := []byte(src)
	good := spanOf(t, src, "海水 周期性")
	bad := HitSpan{CharStart: spanOf(t, src, "海水").CharStart, CharEnd: spanOf(t, src, "大规模").CharEnd} // 跨段
	pack, err := BuildPack("q-fail", []DocHit{
		{DocKey: "good/1", RevisionID: fixtureRevision, RRFScore: 0.9, Hits: []HitSpan{good}, Tree: tree, Source: source},
		{DocKey: "bad/cross", RevisionID: fixtureRevision, RRFScore: 0.8, Hits: []HitSpan{bad}, Tree: tree, Source: source},
	})
	if err == nil {
		t.Fatalf("跨段命中应导致整包失败")
	}
	if !errors.Is(err, ErrHitCrossParagraph) {
		t.Fatalf("err = %v, want ErrHitCrossParagraph", err)
	}
	if !strings.Contains(err.Error(), "bad/cross") {
		t.Fatalf("错误应定位到失败候选: %v", err)
	}
	if len(pack.Candidates) != 0 || pack.QueryID != "" {
		t.Fatalf("失败时应返回零值包, got %+v", pack)
	}
}

// TestBuildPack_InputBounds 输入约束：候选超 50、命中数 0/9、
// doc_key 空、RevisionID 不一致、query_id 空。
func TestBuildPack_InputBounds(t *testing.T) {
	src, tree := buildFixture()
	source := []byte(src)
	sp := spanOf(t, src, "海水 周期性")
	doc := func(key string, spans []HitSpan) DocHit {
		return DocHit{DocKey: key, RevisionID: fixtureRevision, RRFScore: 0.1, Hits: spans, Tree: tree, Source: source}
	}
	tooMany := make([]DocHit, 0, MaxCandidates+1)
	for i := 0; i <= MaxCandidates; i++ {
		tooMany = append(tooMany, doc("doc", []HitSpan{sp})) // 51 条
	}
	cases := []struct {
		name string
		qid  string
		hits []DocHit
		want string
	}{
		{"query_id 为空", "", []DocHit{doc("k", []HitSpan{sp})}, "query_id"},
		{"候选数 51 超上限", "q", tooMany, "超出上限"},
		{"单候选命中 0 条", "q", []DocHit{doc("k", nil)}, "命中数"},
		{"单候选命中 9 条", "q", []DocHit{doc("k", repeatSpans(sp, 9))}, "命中数"},
		{"doc_key 为空", "q", []DocHit{doc("", []HitSpan{sp})}, "doc_key"},
		{"RevisionID 与树不一致", "q", []DocHit{{
			DocKey: "k", RevisionID: "rev-别的", RRFScore: 0.1,
			Hits: []HitSpan{sp}, Tree: tree, Source: source,
		}}, "不一致"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pack, err := BuildPack(c.qid, c.hits)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want 包含 %q", err, c.want)
			}
			if len(pack.Candidates) != 0 {
				t.Fatalf("失败时应返回零值包")
			}
		})
	}
}

func repeatSpans(sp HitSpan, n int) []HitSpan {
	out := make([]HitSpan, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, sp)
	}
	return out
}

// validPack 构造一个最小合法包（Validate 测试的基准）。
func validPack() EvidencePack {
	return EvidencePack{
		QueryID: "q1",
		Candidates: []EvidenceCandidate{{
			DocKey:   "k1",
			RRFScore: 1.5,
			Lanes:    LaneScores{Dense: 0.5, Sparse: 0.25, Multi: 0.75, Rerank: 0},
			Evidence: []Locator{{RevisionID: "r1", SectionPath: []string{}, ParaIndex: 0, Quote: "潮"}},
		}},
	}
}

// TestEvidencePack_Validate 出口契约校验：query_id / 候选数 / evidence
// 条数 1..8 / quote 长度 1..200 rune / doc_key。
func TestEvidencePack_Validate(t *testing.T) {
	t.Run("基准合法", func(t *testing.T) {
		if err := validPack().Validate(); err != nil {
			t.Fatalf("合法包不应报错: %v", err)
		}
	})
	mutations := []struct {
		name string
		mut  func(*EvidencePack)
		want string
	}{
		{"query_id 为空", func(p *EvidencePack) { p.QueryID = "" }, "query_id"},
		{"候选数 51", func(p *EvidencePack) {
			for i := 0; i < MaxCandidates; i++ {
				p.Candidates = append(p.Candidates, p.Candidates[0])
			}
		}, "超出上限"},
		{"evidence 为空", func(p *EvidencePack) { p.Candidates[0].Evidence = nil }, "evidence 为空"},
		{"evidence 9 条", func(p *EvidencePack) {
			for i := 0; i < MaxEvidencesPerCandidate; i++ {
				p.Candidates[0].Evidence = append(p.Candidates[0].Evidence, p.Candidates[0].Evidence[0])
			}
		}, "超出上限"},
		{"quote 为空", func(p *EvidencePack) { p.Candidates[0].Evidence[0].Quote = "" }, "quote 为空"},
		{"quote 201 rune", func(p *EvidencePack) {
			p.Candidates[0].Evidence[0].Quote = strings.Repeat("潮", MaxQuoteRunes+1)
		}, "超出上限"},
		{"doc_key 为空", func(p *EvidencePack) { p.Candidates[0].DocKey = "" }, "doc_key"},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			p := validPack()
			m.mut(&p)
			if err := p.Validate(); err == nil || !strings.Contains(err.Error(), m.want) {
				t.Fatalf("err = %v, want 包含 %q", err, m.want)
			}
		})
	}
}

// TestJSONTags_Mirror 钉死与 RTW structure 契约一致的 snake_case JSON
// 线格式（字段名、嵌套结构、空 section_path 序列化为 []）。
func TestJSONTags_Mirror(t *testing.T) {
	loc := Locator{
		RevisionID:  "r1",
		SectionPath: []string{"海洋学导论", "第一章 潮汐"},
		ParaIndex:   1,
		Quote:       "潮汐",
	}
	locJSON, err := json.Marshal(loc)
	if err != nil {
		t.Fatalf("marshal Locator: %v", err)
	}
	wantLoc := `{"revision_id":"r1","section_path":["海洋学导论","第一章 潮汐"],"para_index":1,"quote":"潮汐"}`
	if string(locJSON) != wantLoc {
		t.Fatalf("Locator JSON = %s, want %s", locJSON, wantLoc)
	}

	tree := TreeJSON{
		RevisionID: "rev1",
		Nodes: []NodeJSON{{
			NodeID: "n0", Level: 2, Title: "第一章 潮汐",
			ParaIndex: HeadingParaIndex, CharStart: 5, CharEnd: 20,
		}},
	}
	treeJSON, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("marshal TreeJSON: %v", err)
	}
	wantTree := `{"revision_id":"rev1","nodes":[{"node_id":"n0","level":2,"title":"第一章 潮汐","para_index":-1,"char_start":5,"char_end":20}]}`
	if string(treeJSON) != wantTree {
		t.Fatalf("TreeJSON = %s, want %s", treeJSON, wantTree)
	}

	pack := EvidencePack{
		QueryID: "q1",
		Candidates: []EvidenceCandidate{{
			DocKey:   "k1",
			RRFScore: 1.5,
			Lanes:    LaneScores{Dense: 0.5, Sparse: 0.25, Multi: 0.75, Rerank: 0},
			Evidence: []Locator{{RevisionID: "r1", SectionPath: []string{}, ParaIndex: 0, Quote: "潮"}},
		}},
	}
	packJSON, err := json.Marshal(pack)
	if err != nil {
		t.Fatalf("marshal EvidencePack: %v", err)
	}
	wantPack := `{"query_id":"q1","candidates":[{"doc_key":"k1","rrf_score":1.5,"lanes":{"dense":0.5,"sparse":0.25,"multi":0.75,"rerank":0},"evidence":[{"revision_id":"r1","section_path":[],"para_index":0,"quote":"潮"}]}]}`
	if string(packJSON) != wantPack {
		t.Fatalf("EvidencePack JSON = %s, want %s", packJSON, wantPack)
	}

	// 反序列化回读一致（跨服务 JSON 入口可用）。
	var back EvidencePack
	if err := json.Unmarshal(packJSON, &back); err != nil {
		t.Fatalf("unmarshal EvidencePack: %v", err)
	}
	if !reflect.DeepEqual(back, pack) {
		t.Fatalf("JSON 往返不一致:\n%+v\n%+v", back, pack)
	}
}
