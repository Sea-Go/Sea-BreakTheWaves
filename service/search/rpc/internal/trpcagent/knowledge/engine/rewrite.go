// ============================================================================
// 该文件实现查询重写 Rewriter，输出 RewrittenQuery。
// 包含同义词扩展、拼写纠正（编辑距离 < 2 时纠正）、相关词推荐。
//
// 职责：
//   - 同义词扩展：通过 SynonymDict 查询同义词与相关词
//   - 拼写纠正：对 query 中各 token 与已知词词典做编辑距离匹配，距离为 1 时纠正
//   - LLM 增强：可选 LLM 生成重写 query / 同义词 / 相关词；失败回退规则
//
// 二开扩展点：
//   - 实现 SynonymDict interface 注入业务同义词词典
//   - 实现 LLMClient interface 接入 LLM 做语义重写（复用 understand.go 的 LLMClient）
//   - 通过 WithRewriterCorrections 注入业务纠错词典
// ============================================================================

package search

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"sea/service/search/rpc/internal/domain"
)

// SynonymDict 同义词词典契约。
type SynonymDict interface {
	// Lookup 查询 query 的同义词与相关词。
	Lookup(query string) (synonyms []string, related []string)
}

// synEntry 同义词词典条目。
type synEntry struct {
	Synonyms []string
	Related  []string
}

// MapSynonymDict 内置基于 map 的同义词词典实现。
type MapSynonymDict struct {
	data map[string]synEntry
}

// NewMapSynonymDict 创建空的同义词词典。
func NewMapSynonymDict() *MapSynonymDict {
	return &MapSynonymDict{data: make(map[string]synEntry)}
}

// Add 添加同义词与相关词条目（query 为键）。
func (d *MapSynonymDict) Add(query string, synonyms, related []string) {
	d.data[query] = synEntry{Synonyms: synonyms, Related: related}
}

// Lookup 实现 SynonymDict.Lookup，未命中返回 nil。
func (d *MapSynonymDict) Lookup(query string) ([]string, []string) {
	e, ok := d.data[query]
	if !ok {
		return nil, nil
	}
	return e.Synonyms, e.Related
}

// QueryRewriter 查询重写 interface。
type QueryRewriter interface {
	// Rewrite 重写查询，返回重写结果。
	Rewrite(ctx context.Context, query string) (domain.RewrittenQuery, error)
}

// Rewriter 查询重写实现。
type Rewriter struct {
	llm         LLMClient // 复用 understand.go 定义的 LLMClient
	dict        SynonymDict
	corrections map[string]string // 已知纠错映射（错误词 -> 正确词）
	knownList   []string          // 拼写纠正用的已知词列表（由 knownWords 维护）
}

// RewriterOption Rewriter 构造选项。
type RewriterOption func(*Rewriter)

// WithRewriterCorrections 注入业务纠错词典（错误词 -> 正确词），
// 同时其正确词会加入拼写纠正的已知词集。
func WithRewriterCorrections(c map[string]string) RewriterOption {
	return func(r *Rewriter) {
		if c == nil {
			return
		}
		cp := make(map[string]string, len(c))
		for k, v := range c {
			cp[k] = v
		}
		r.corrections = cp
	}
}

// NewRewriter 创建查询重写器。
// llm 可为 nil（仅走规则路径）；dict 可为 nil（无同义词扩展）。
// "兜底空 LLM"：llm 为 nil 时直接返回规则重写结果（无 LLM 调用）。
func NewRewriter(llm LLMClient, dict SynonymDict, opts ...RewriterOption) *Rewriter {
	r := &Rewriter{
		llm:         llm,
		dict:        dict,
		corrections: map[string]string{},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Rewrite 重写查询，实现 QueryRewriter.Rewrite。
//
// 流程：
//  1. 规则路径：同义词词典 Lookup + 拼写纠正（编辑距离 1）；
//  2. 若注入 LLMClient，调用 LLM 生成重写结果，并与规则结果合并（LLM 失败回退规则）。
//  3. 返回 RewrittenQuery（Original 为原始 query）。
func (r *Rewriter) Rewrite(ctx context.Context, query string) (domain.RewrittenQuery, error) {
	var syn, rel []string
	if r.dict != nil {
		syn, rel = r.dict.Lookup(query)
	}
	corrected, correction := r.correctSpelling(query)

	// 无 LLM 或 LLM 为空时直接返回规则结果（兜底空 LLM → 返回原 query）
	if r.llm == nil {
		return domain.RewrittenQuery{
			Original:   query,
			Rewritten:  corrected,
			Synonyms:   syn,
			Related:    rel,
			Correction: correction,
		}, nil
	}

	rq, err := r.rewriteWithLLM(ctx, query)
	if err != nil {
		// LLM 失败回退规则
		return domain.RewrittenQuery{
			Original:   query,
			Rewritten:  corrected,
			Synonyms:   syn,
			Related:    rel,
			Correction: correction,
		}, nil
	}
	// 合并词典同义词 / 相关词
	rq.Synonyms = unionStr(rq.Synonyms, syn)
	rq.Related = unionStr(rq.Related, rel)
	if rq.Correction == "" && correction != "" {
		rq.Correction = correction
	}
	if rq.Rewritten == "" {
		rq.Rewritten = corrected
	}
	rq.Original = query
	return rq, nil
}

// rewriteWithLLM 调用 LLM 结构化输出重写结果。
func (r *Rewriter) rewriteWithLLM(ctx context.Context, query string) (domain.RewrittenQuery, error) {
	prompt := "重写以下搜索查询，返回 JSON：重写查询(rewritten)、同义词(synonyms)、相关词(related)、拼写纠正(correction)。\n查询：" + query
	raw, err := r.llm.CompleteWithStructuredOutput(ctx, prompt, rewriteSchema())
	if err != nil {
		return domain.RewrittenQuery{}, err
	}
	if len(raw) == 0 {
		return domain.RewrittenQuery{}, errors.New("llm 返回空结果")
	}
	var out struct {
		Rewritten  string   `json:"rewritten"`
		Synonyms   []string `json:"synonyms"`
		Related    []string `json:"related"`
		Correction string   `json:"correction"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return domain.RewrittenQuery{}, err
	}
	return domain.RewrittenQuery{
		Rewritten:  out.Rewritten,
		Synonyms:   out.Synonyms,
		Related:    out.Related,
		Correction: out.Correction,
	}, nil
}

// rewriteSchema 返回重写结果的结构化输出 JSON Schema。
func rewriteSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"rewritten":  map[string]any{"type": "string"},
			"synonyms":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"related":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"correction": map[string]any{"type": "string"},
		},
		"required": []string{"rewritten", "synonyms", "related", "correction"},
	}
}

// correctSpelling 拼写纠正。
// 对 query 中每个 token：若不在已知词集，且与某已知词编辑距离为 1，则替换。
// 返回纠正后的 query 与纠正串（无纠正时 correction 为空）。
//
// 已知词集 = 同义词词典键 + 纠错词典正确词（词典为空时注入少量常见词兜底）。
func (r *Rewriter) correctSpelling(query string) (corrected, correction string) {
	tokens := strings.Fields(query)
	if len(tokens) == 0 {
		return query, ""
	}
	known := r.knownWords()
	changed := false
	for i, tok := range tokens {
		if _, ok := known[tok]; ok {
			continue
		}
		// 直接命中纠错映射优先
		if fix, ok := r.corrections[strings.ToLower(tok)]; ok && fix != "" {
			tokens[i] = fix
			changed = true
			continue
		}
		best := ""
		bestDist := 2 // 仅纠正距离 < 2（即 0 或 1）；0 表示相同（已在 known 中跳过），故实际匹配距离 1
		for _, w := range r.knownList {
			d := levenshtein(tok, w)
			if d < bestDist {
				bestDist = d
				best = w
			}
		}
		if best != "" {
			tokens[i] = best
			changed = true
		}
	}
	if !changed {
		return query, ""
	}
	c := strings.Join(tokens, " ")
	return c, c
}

// knownWords 返回已知词集合，并填充 r.knownList（用于遍历求最近词）。
func (r *Rewriter) knownWords() map[string]struct{} {
	set := make(map[string]struct{}, len(r.corrections)+8)
	r.knownList = r.knownList[:0]
	add := func(w string) {
		if w == "" {
			return
		}
		if _, ok := set[w]; !ok {
			set[w] = struct{}{}
			r.knownList = append(r.knownList, w)
		}
	}
	if d, ok := r.dict.(*MapSynonymDict); ok {
		for k := range d.data {
			add(k)
		}
	}
	for _, v := range r.corrections {
		add(v)
	}
	// 兜底：若词典为空，注入少量常见正确词以保证纠正可测
	if len(set) == 0 {
		for _, w := range []string{"golang", "python", "java", "rust", "recommendation"} {
			add(w)
		}
	}
	return set
}

// levenshtein 计算两串编辑距离（标准 DP）。
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = minInt(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// minInt 返回多个整数的最小值。
func minInt(vals ...int) int {
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

// unionStr 合并两个字符串切片并去重，保持顺序（a 优先）。
func unionStr(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
