// ============================================================================
// rule.go —— 规则规划器（balanced 档的 dev 形态，不调 LLM）。
//
// §3.1 balanced 行：1 次 LLM 规划，做 query2doc 式扩展 / K 个多视角
// 改写 / 过细查询加 step-back 变体，且"原查询永远保留为一路防漂移"。
// 本规划器是该 1 次调用的确定性规则替身：RewrittenQuery=TrimSpace 后的
// 原查询（防漂移的那一路），Variants=3 个确定性变体（见下），预算按
// 契约记 1（真实化后由 LLM 规划器替换，见 README）。
//
// dev 口径声明（写死小表，注释即规范）：
//   - 停用词表与同义词表均为人工写死的小表，覆盖常见中文功能词/近义
//     词，不做分词、不含词性；表内容只影响变体质量，不影响契约。
//   - 停用词按"最长优先"的固定次序做子串剔除（"为什么"先于"什么"，
//     防止长词被短词拆散）；剔除后可能露出已处理词的新出现，属可接受
//     的 dev 近似（结果仍确定）。
//   - 同义词查换用单趟正则替换（ReplaceAllStringFunc 不重扫替换产物），
//     键按"最长优先"的固定次序进入交替分支——既保证次序确定，也避免
//     "检索→搜索"的产物再被"搜索→检索"换回。对合成词可能误换（如
//     "搜索引擎"→"检索引擎"），dev 接受。
//   - 变体与 RewrittenQuery 或彼此重复时去重（重复变体没有召回价值），
//     因此特定查询下 Variants 可能少于 3 个。
// ============================================================================

package planner

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// keywordVariantN 是"取前 N 关键词"变体的 N（dev 口径：4）。
const keywordVariantN = 4

// stopwords 是去停用词变体用的停用词表（dev 写死小表）。
// 次序即剔除次序：最长优先，防止"为什么"被"什么"先拆散。
var stopwords = []string{
	"为什么", "什么", "怎么", "如何", "以及", "请问", "我想",
	"的", "了", "是", "在", "和", "与", "呢", "吗", "啊", "吧",
}

// synonymPairs 是同义词查换变体用的双向小表（dev 写死；键→值成对互
// 查）。切片次序确定；真实化路径：LLM 规划器 / 同义词库。
var synonymPairs = [][2]string{
	{"检索", "搜索"}, {"搜索", "检索"},
	{"文档", "文章"}, {"文章", "文档"},
	{"向量", "嵌入"}, {"嵌入", "向量"},
	{"配置", "设置"}, {"设置", "配置"},
	{"部署", "上线"}, {"上线", "部署"},
}

// synonymIndex 是 键→替换值 的索引。
var synonymIndex = func() map[string]string {
	m := make(map[string]string, len(synonymPairs))
	for _, p := range synonymPairs {
		m[p[0]] = p[1]
	}
	return m
}()

// synonymRe 是同义词查换的单趟匹配器：交替分支按"最长优先"的固定次序
// 排列（稳定排序保留同长度键的声明次序），保证同输入同输出。
var synonymRe = func() *regexp.Regexp {
	keys := make([]string, len(synonymPairs))
	for i, p := range synonymPairs {
		keys[i] = p[0]
	}
	// 插入排序（严格小于 → 稳定）：键长降序。
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && len(keys[j-1]) < len(keys[j]); j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	for i, k := range keys {
		keys[i] = regexp.QuoteMeta(k)
	}
	return regexp.MustCompile(strings.Join(keys, "|"))
}()

// Rule 规则规划器：只服务 balanced 档，产出确定性规则计划。
// 零值可用，无状态、并发安全。
type Rule struct{}

// Plan 产出 balanced 档规则计划：RewrittenQuery=TrimSpace 后的 query，
// Variants=3 个确定性变体（去停用词 / 取前 N 关键词 / 同义词表查换，
// 去重去空后可能少于 3 个），LLMBudget=1。不产 step-back（属 LLM
// 规划器职责，见 README 真实化路径）。
func (p Rule) Plan(ctx context.Context, query string, tier retrieval.Tier) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if !validTier(tier) {
		return Plan{}, fmt.Errorf("%w: %q（合法值 fast|balanced|deep）", ErrInvalidTier, tier)
	}
	if tier != retrieval.TierBalanced {
		return Plan{}, fmt.Errorf("%w: 规则规划器只服务 balanced 档，收到 %q", ErrTierMismatch, tier)
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return Plan{}, ErrEmptyQuery
	}
	return Plan{
		RewrittenQuery: trimmed,
		Variants:       p.variants(trimmed),
		Tier:           retrieval.TierBalanced,
		LLMBudget:      BudgetBalanced,
	}, nil
}

// variants 依固定次序产出 3 个确定性变体，并做去空去重（与
// RewrittenQuery 重复的变体没有价值——原查询已是一路，§3.1 ④）。
func (p Rule) variants(query string) []string {
	raw := []string{p.dropStopwords(query), p.firstKeywords(query), p.swapSynonyms(query)}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{query: true}
	for _, v := range raw {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// dropStopwords 变体一：按最长优先的固定次序做停用词子串剔除，再折叠
// 空白。
func (p Rule) dropStopwords(query string) string {
	s := query
	for _, w := range stopwords {
		s = strings.ReplaceAll(s, w, "")
	}
	return collapseSpaces(s)
}

// firstKeywords 变体二：按空白切词、剔除整词命中停用词表的词元、取前
// N 个关键词拼接。dev 口径：无分词器，连续中文串视作单 token，故纯
// 中文无空格查询下本变体退化为原句（随后被去重）。
func (p Rule) firstKeywords(query string) string {
	stop := map[string]bool{}
	for _, w := range stopwords {
		stop[w] = true
	}
	words := make([]string, 0, keywordVariantN)
	for _, tok := range strings.Fields(query) {
		if stop[tok] {
			continue
		}
		words = append(words, tok)
		if len(words) == keywordVariantN {
			break
		}
	}
	return strings.Join(words, " ")
}

// swapSynonyms 变体三：单趟查同义词小表，命中即替换（不重扫替换产物）。
func (p Rule) swapSynonyms(query string) string {
	return synonymRe.ReplaceAllStringFunc(query, func(m string) string {
		return synonymIndex[m]
	})
}

// collapseSpaces 把连续空白折叠为单个空格并去首尾空白（子串剔除后
// 可能留下连续空格）。
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
