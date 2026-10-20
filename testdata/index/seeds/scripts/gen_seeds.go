//go:generate go run ./gen_seeds.go

// ============================================================================
// gen_seeds.go 是 Sea 检索评测种子集（M0）的确定性生成脚本。
//
// 手动运行（go:generate 风格但手动执行，见 testdata/index/seeds/README.md）：
//
//	# 在仓库根目录
//	go run ./testdata/index/seeds/scripts/gen_seeds.go
//	go run ./testdata/index/seeds/scripts/gen_seeds.go -out /tmp/seeds-check   # 输出到任意目录
//
// 产出（写入 -out 指定的种子集目录，默认为脚本所在目录的上一级）：
//   - corpus/doc-00.md .. doc-19.md：20 篇合成中文 markdown（多级标题、每篇 8-15 段）
//   - queries.jsonl：50 条查询（qid/text/gold{doc_rels, locator_spans}）
//   - qrels.txt：trec qrels 风格标注（"qid 0 docid rel"，仅 rel>0 行）
//
// 确定性保证：脚本不读取时间、随机数、环境变量或网络，全部内容由
// 文档下标/段落下标/句子下标经固定模板与取模运算推导。同一版本脚本
// 重复运行逐字节一致；该性质由 gen_seeds_test.go 强制校验。
// ============================================================================

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ----------------------------------------------------------------------------
// 规模常量。
// ----------------------------------------------------------------------------

const (
	numDomains       = 5  // 主题域数量
	docsPerDomain    = 4  // 每个主题域的文档数
	aspectsPerDomain = 5  // 每个主题域的查询侧面数
	anglesPerAspect  = 2  // 每个侧面的问法角度数
	minParas         = 8  // 每篇文档正文段落下限
	maxParas         = 15 // 每篇文档正文段落上限
)

const (
	numDocs    = numDomains * docsPerDomain                      // 20
	numQueries = numDomains * aspectsPerDomain * anglesPerAspect // 50
)

// domainNames 5 个主题域，便于构造跨域干扰项与相关性梯度。
var domainNames = [numDomains]string{
	"海洋观测",
	"气候能源",
	"城市交通",
	"精准农业",
	"深空探测",
}

// domainAspects 每个主题域 5 个查询侧面（与查询文本、gold 构造一一对应）。
var domainAspects = [numDomains][aspectsPerDomain]string{
	{"观测网络建设", "多源数据融合", "浮标运维策略", "数据质量控制", "极区海冰监测"},
	{"风光出力预测", "电网调峰能力", "碳排放核算", "储能配置规划", "极端天气应对"},
	{"轨道客流预测", "信号配时优化", "慢行系统规划", "公交线网调整", "拥堵收费评估"},
	{"土壤墒情监测", "变量施肥决策", "病虫害预警", "无人机遥感", "收获调度优化"},
	{"轨道器设计", "火星车导航", "深空通信链路", "科学载荷选择", "样品返回策略"},
}

// predicates 句式谓语池（确定性取模选择，无随机）。
var predicates = [12]string{
	"建立长期稳定的观测基线",
	"推动多源数据的深度融合",
	"控制全链路的误差累积",
	"保障关键节点的冗余容量",
	"对齐跨团队的口径定义",
	"沉淀可复用的方法模板",
	"量化不确定性并公开披露",
	"优先复用成熟的基础组件",
	"在仿真环境中先行验证",
	"为失败模式预留回退路径",
	"把人工经验转化为规则库",
	"按季度复盘并更新假设",
}

// consequences 句式后果池。
var consequences = [12]string{
	"否则后续的结论难以外推",
	"这将显著降低迭代成本",
	"团队对此已形成共识",
	"该结论在多轮复盘中保持稳定",
	"实践中需要权衡时间与精度",
	"历史数据支持这一判断",
	"这是当前阶段的最优解",
	"相关指标已纳入例行监控",
	"任何偏离都需要显式记录",
	"初期投入会相对集中",
	"效果通常在下个季度显现",
	"该路径的副作用可控",
}

// sectionTitles 每篇文档固定三个二级章节。
var sectionTitles = [3]string{"背景与约束", "方法与路线", "实践与展望"}

// subTitles 每个章节内的三级子标题（章节数 ≥3 段时插入在首段之后）。
var subTitles = [3]string{"关键约束清单", "关键技术与数据", "落地与复盘"}

// ----------------------------------------------------------------------------
// 数据模型。
// ----------------------------------------------------------------------------

// seedPara 正文段落：Text 为完整段落文本（单行），First 为首句（locator 引文用）。
type seedPara struct {
	Text  string
	First string
}

// seedSection 二级章节：标题、可选三级子标题及其插入位置、正文段落索引。
type seedSection struct {
	Title    string
	Sub      string // 可为空（表示该章节无三级子标题）
	SubAfter int    // 子标题插在本章节第几个段落之后（0 基）
	Paras    []int  // 本章节包含的文档级段落索引（按出现顺序）
}

// seedDoc 一篇合成文档。
type seedDoc struct {
	ID         string
	DomainIdx  int
	Vol        int // 域内卷号（1..docsPerDomain）
	Paragraphs []seedPara
	Sections   []seedSection
}

// locatorSpan gold 中的定位证据：文档 + 段落索引 + 逐字引文。
type locatorSpan struct {
	Doc       string `json:"doc"`
	ParaIndex int    `json:"para_index"`
	Quote     string `json:"quote"`
}

// goldJSON queries.jsonl 中 gold 字段的结构。
type goldJSON struct {
	DocRels  map[string]int `json:"doc_rels"`
	Locators []locatorSpan  `json:"locator_spans"`
}

// queryJSON queries.jsonl 单行结构。
type queryJSON struct {
	Qid  string   `json:"qid"`
	Text string   `json:"text"`
	Gold goldJSON `json:"gold"`
}

// ----------------------------------------------------------------------------
// 确定性推导函数。
// ----------------------------------------------------------------------------

// docID 由文档全局下标得到文件名（不含 .md）。
func docID(idx int) string { return fmt.Sprintf("doc-%02d", idx) }

// docIdxFor 由主题域下标与域内卷号（0 基）得到文档全局下标。
func docIdxFor(domainIdx, vol int) int { return vol*numDomains + domainIdx }

// paraCount 第 idx 篇文档的正文段落数（8..15，确定性分布）。
func paraCount(idx int) int { return minParas + (idx*5)%(maxParas-minParas+1) }

// sectionSizes 把 p 个段落切分为三个章节，余数前倾。
func sectionSizes(p int) [3]int {
	base, r := p/3, p%3
	sizes := [3]int{base, base, base}
	if r > 0 {
		sizes[0]++
	}
	if r > 1 {
		sizes[1]++
	}
	return sizes
}

// sentenceCount 第 idx 篇文档第 p 段的句子数（3..5）。
func sentenceCount(idx, p int) int { return 3 + (idx*3+p)%3 }

// buildSentence 生成一个句子。n 为文档内全局要点编号（1 起，doc 内唯一）。
func buildSentence(domain string, n, idx, p, k int) string {
	pred := predicates[(idx*31+p*13+k*5)%len(predicates)]
	cons := consequences[(idx*17+p*11+k*7)%len(consequences)]
	if k == 0 {
		// 首句固定使用基础句式，便于 locator 引文稳定可读。
		return fmt.Sprintf("%s的第 %d 个要点是%s，%s。", domain, n, pred, cons)
	}
	switch (idx*7 + p*5 + k) % 3 {
	case 0:
		return fmt.Sprintf("%s的第 %d 个要点是%s，%s。", domain, n, pred, cons)
	case 1:
		return fmt.Sprintf("围绕%s，第 %d 个要点指出应当%s，因为%s。", domain, n, pred, cons)
	default:
		return fmt.Sprintf("在%s的第 %d 个要点中，我们强调%s——%s。", domain, n, pred, cons)
	}
}

// buildDocs 构造全部 20 篇文档（内存模型，渲染与查询构造共用）。
func buildDocs() []seedDoc {
	docs := make([]seedDoc, 0, numDocs)
	for idx := 0; idx < numDocs; idx++ {
		domain := domainNames[idx%numDomains]
		vol := idx/numDomains + 1
		p := paraCount(idx)
		sizes := sectionSizes(p)

		doc := seedDoc{
			ID:        docID(idx),
			DomainIdx: idx % numDomains,
			Vol:       vol,
		}
		n := 0 // 文档内全局要点编号
		for s := 0; s < 3; s++ {
			sec := seedSection{Title: fmt.Sprintf("%s的%s", domain, sectionTitles[s])}
			for j := 0; j < sizes[s]; j++ {
				var b strings.Builder
				kMax := sentenceCount(idx, len(doc.Paragraphs))
				first := ""
				for k := 0; k < kMax; k++ {
					n++
					sent := buildSentence(domain, n, idx, len(doc.Paragraphs), k)
					b.WriteString(sent)
					if k == 0 {
						first = sent
					}
				}
				doc.Paragraphs = append(doc.Paragraphs, seedPara{Text: b.String(), First: first})
				sec.Paras = append(sec.Paras, len(doc.Paragraphs)-1)
			}
			if sizes[s] >= 3 {
				sec.Sub = fmt.Sprintf("%s的%s", domain, subTitles[s])
				sec.SubAfter = 0 // 插在本章节首段之后
			}
			doc.Sections = append(doc.Sections, sec)
		}
		docs = append(docs, doc)
	}
	return docs
}

// renderDoc 把内存模型渲染为 markdown 文本。
func renderDoc(doc seedDoc) string {
	domain := domainNames[doc.DomainIdx]
	var b strings.Builder
	fmt.Fprintf(&b, "# %s研究纪要（第 %d 卷）\n\n", domain, doc.Vol)
	fmt.Fprintf(&b, "> 导读：%s 是主题域「%s」的确定性合成文档，属于 Sea 检索评测种子集；"+
		"全文由固定模板生成，可逐字节再生。\n\n", doc.ID, domain)

	for _, sec := range doc.Sections {
		fmt.Fprintf(&b, "## %s\n\n", sec.Title)
		for j, pi := range sec.Paras {
			fmt.Fprintf(&b, "%s\n\n", doc.Paragraphs[pi].Text)
			if sec.Sub != "" && j == sec.SubAfter {
				fmt.Fprintf(&b, "### %s\n\n", sec.Sub)
			}
		}
	}

	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "<!-- %s · 主题域「%s」· 共 %d 段正文 · "+
		"由 scripts/gen_seeds.go 确定性生成，无随机成分与时间戳。 -->\n", doc.ID, domain, len(doc.Paragraphs))
	return b.String()
}

// buildQueries 构造全部 50 条查询及其 gold 标注。
//
// 相关性口径（0-3 分级，仅 >0 写入 qrels）：
//   - 域内主文档（侧面+角度决定）rel=3；
//   - 域内次文档 rel=2；
//   - (a*2+g)%3==0 时补一篇域内干扰文档 rel=1；
//   - (a+g)%3==1 时补一篇跨域（(d+2)%5）干扰文档 rel=1。
//
// locator_spans 固定指向 rel≥2 的两篇文档，段落下标由固定取模推导，
// 引文取该段首句（逐字）。
func buildQueries(docs []seedDoc) []queryJSON {
	queries := make([]queryJSON, 0, numQueries)
	for d := 0; d < numDomains; d++ {
		domain := domainNames[d]
		for a := 0; a < aspectsPerDomain; a++ {
			aspect := domainAspects[d][a]
			for g := 0; g < anglesPerAspect; g++ {
				qid := fmt.Sprintf("q-%02d", d*aspectsPerDomain*anglesPerAspect+a*anglesPerAspect+g)
				var text string
				if g == 0 {
					text = fmt.Sprintf("%s中%s的核心要点有哪些？", domain, aspect)
				} else {
					text = fmt.Sprintf("%s里%s的主流方案如何比较与取舍？", domain, aspect)
				}

				primary := docIdxFor(d, (a+g)%docsPerDomain)
				secondary := docIdxFor(d, (a+g+1)%docsPerDomain)
				rels := map[string]int{
					docID(primary):   3,
					docID(secondary): 2,
				}
				if (a*2+g)%3 == 0 {
					rels[docID(docIdxFor(d, (a+g+2)%docsPerDomain))] = 1
				}
				if (a+g)%3 == 1 {
					rels[docID(docIdxFor((d+2)%numDomains, (a+g)%docsPerDomain))] = 1
				}

				spans := make([]locatorSpan, 0, 2)
				for s, di := range []int{primary, secondary} {
					pIdx := (di*7 + a*3 + g*2 + s*5) % len(docs[di].Paragraphs)
					spans = append(spans, locatorSpan{
						Doc:       docID(di),
						ParaIndex: pIdx,
						Quote:     docs[di].Paragraphs[pIdx].First,
					})
				}

				queries = append(queries, queryJSON{
					Qid:  qid,
					Text: text,
					Gold: goldJSON{DocRels: rels, Locators: spans},
				})
			}
		}
	}
	return queries
}

// qrelsLines 由查询集合推导 qrels.txt 行（仅 rel>0，按 qid、docid 排序）。
func qrelsLines(queries []queryJSON) []string {
	type key struct{ qid, doc string }
	rel := map[key]int{}
	for _, q := range queries {
		for doc, r := range q.Gold.DocRels {
			if r > 0 {
				rel[key{q.Qid, doc}] = r
			}
		}
	}
	keys := make([]key, 0, len(rel))
	for k := range rel {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].qid != keys[j].qid {
			return keys[i].qid < keys[j].qid
		}
		return keys[i].doc < keys[j].doc
	})
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s 0 %s %d", k.qid, k.doc, rel[k]))
	}
	return lines
}

// ----------------------------------------------------------------------------
// 文件写出。
// ----------------------------------------------------------------------------

// generateAll 生成全部种子文件到 dir（corpus/、queries.jsonl、qrels.txt）。
func generateAll(dir string) error {
	docs := buildDocs()

	corpusDir := filepath.Join(dir, "corpus")
	if err := os.MkdirAll(corpusDir, 0o755); err != nil {
		return fmt.Errorf("创建 corpus 目录失败: %w", err)
	}
	for _, doc := range docs {
		if err := writeBytes(filepath.Join(corpusDir, doc.ID+".md"), []byte(renderDoc(doc))); err != nil {
			return err
		}
	}

	queries := buildQueries(docs)
	qf, err := os.Create(filepath.Join(dir, "queries.jsonl"))
	if err != nil {
		return fmt.Errorf("创建 queries.jsonl 失败: %w", err)
	}
	defer qf.Close()
	enc := json.NewEncoder(qf)
	enc.SetEscapeHTML(false)
	for _, q := range queries {
		if err := enc.Encode(q); err != nil {
			return fmt.Errorf("写入查询 %s 失败: %w", q.Qid, err)
		}
	}

	var qb strings.Builder
	for _, line := range qrelsLines(queries) {
		qb.WriteString(line)
		qb.WriteByte('\n')
	}
	if err := writeBytes(filepath.Join(dir, "qrels.txt"), []byte(qb.String())); err != nil {
		return err
	}
	return nil
}

func writeBytes(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return nil
}

// defaultSeedsDir 默认输出到脚本所在目录的上一级（即 testdata/index/seeds）。
func defaultSeedsDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Join(filepath.Dir(thisFile), "..")
}

func main() {
	out := flag.String("out", defaultSeedsDir(), "种子集输出目录")
	flag.Parse()
	if err := generateAll(*out); err != nil {
		fmt.Fprintf(os.Stderr, "gen_seeds: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "gen_seeds: 已生成种子集到 %s（%d 篇文档 / %d 条查询）\n", *out, numDocs, numQueries)
}
