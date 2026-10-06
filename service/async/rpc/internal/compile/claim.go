// ============================================================================
// claim.go —— 引用双表与 Claim 追踪（工程方案 §4.8 决策 8 后半）。
//
// 引用双表=正文行内 [n] 角标（由 Article 阶段写入各章节正文）+
// 独立映射表 Citation（编号→(doc revision, locator)）——把 STORM 的
// "编号→URL"升级为 Sea 的证据定位（补齐 STORM 无段级定位的缺口）。
//
// Claim 追踪借 WikiChat"generate→claims→证据过滤"：ExtractClaims 从
// 正文提取句子级 claim，按引用表判定 HasEvidence；无证据 claim 列表
// （UnevidencedClaims）供前端标"待人工补证"——对冲 STORM 指出的
// over-association（无关事实过度关联）风险。
// ============================================================================

package compile

import (
	"regexp"
	"strconv"
	"strings"
)

// Locator 是引用的证据定位（C42 语义的段级定位；dev 执行器不产出，
// 真实化后由检索/结构层的 locator 语义填充）。
type Locator struct {
	// SectionPath 从文档根到命中段落的标题链（按文档序）。
	SectionPath []string `json:"section_path,omitempty"`
	// ParaIndex 全局段落序号（结构树派生序）。
	ParaIndex int `json:"para_index"`
	// Quote 证据引文（命中区间文本）。
	Quote string `json:"quote,omitempty"`
}

// Citation 是引用映射表（引用双表第二表）的一个条目：行内角标编号 n →
// (doc_key, revision_id, locator)。Index 从 1 递增；Article 阶段生成、
// Polish 阶段原样携带、发布时随 revision 冻结。
type Citation struct {
	// Index 行内 [n] 角标的 n（从 1 递增）。
	Index int `json:"index"`
	// DocKey 被引用资料源的文档键。
	DocKey string `json:"doc_key"`
	// RevisionID 被引用资料源的冻结修订 ID（可空=dev 未冻结）。
	RevisionID string `json:"revision_id,omitempty"`
	// Locator 段级证据定位（可空=dev 成文器未定位，真实化补齐）。
	Locator *Locator `json:"locator,omitempty"`
}

// Claim 是句子级事实声明：正文的一个句子+其证据判定。
type Claim struct {
	// Text 原句（TrimSpace 后，保留行内 [n] 角标原文）。
	Text string `json:"text"`
	// SourceIndex 句内首个 [n] 角标的 n；无角标为 0（区别于悬空编号：
	// n>0 且 !HasEvidence 表示编号在引用表中无对应条目）。
	SourceIndex int `json:"source_index"`
	// HasEvidence 句内首个角标能在引用表中命中即为 true；无角标或
	// 悬空编号均为 false（进"待人工补证"清单）。
	HasEvidence bool `json:"has_evidence"`
}

// citationMarker 匹配行内 [n] 角标（1..4 位数字，防误吞长数字串）。
var citationMarker = regexp.MustCompile(`\[(\d{1,4})\]`)

// isSentenceBoundary 报告 r 是否为分句边界：中英句末标点+分号+换行
// （成文器按行组织正文，行边界即句子边界）。
func isSentenceBoundary(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '!', '?', ';', '\n', '\r':
		return true
	default:
		return false
	}
}

// ExtractClaims 从正文提取句子级 claim 并按引用表判定证据：
//   - 分句：按句末标点与换行切分，边界标点保留在句尾；
//   - 判定：取句内首个 [n] 角标——编号命中 citations 则
//     HasEvidence=true、SourceIndex=n；编号悬空则 HasEvidence=false、
//     SourceIndex=n；整句无角标则 HasEvidence=false、SourceIndex=0。
//
// dev 口径：claim=非空句子（标题行亦计入，真实化后由 LLM 做语义级
// claim 切分，见 README 真实化路径）；多角标句以首个为准（引用表
// 命中性逐句判定，不重复计数）。
func ExtractClaims(text string, citations []Citation) []Claim {
	indexes := make(map[int]Citation, len(citations))
	for _, c := range citations {
		indexes[c.Index] = c
	}
	var claims []Claim
	for _, sentence := range splitSentences(text) {
		claim := Claim{Text: sentence}
		if m := citationMarker.FindStringSubmatch(sentence); m != nil {
			n, err := strconv.Atoi(m[1])
			if err == nil {
				claim.SourceIndex = n
				_, claim.HasEvidence = indexes[n]
			}
		}
		claims = append(claims, claim)
	}
	return claims
}

// splitSentences 把正文切成非空句子：边界标点（。！？；!?;）保留在句
// 尾，换行作纯分隔丢弃；连续边界不产生空句；句子 TrimSpace。
func splitSentences(text string) []string {
	var sentences []string
	var b strings.Builder
	flush := func() {
		s := strings.TrimSpace(b.String())
		if s != "" {
			sentences = append(sentences, s)
		}
		b.Reset()
	}
	for _, r := range text {
		if r == '\n' || r == '\r' {
			flush()
			continue
		}
		b.WriteRune(r)
		if isSentenceBoundary(r) {
			flush()
		}
	}
	flush()
	return sentences
}

// UnevidencedClaims 返回无证据 claim 列表（HasEvidence=false，保持原
// 序）——供前端标"待人工补证"的待办清单（含无角标句与悬空编号句，
// 以 SourceIndex 区分：0=缺引用，>0=引用表缺条目）。
func UnevidencedClaims(claims []Claim) []Claim {
	out := make([]Claim, 0, len(claims))
	for _, c := range claims {
		if !c.HasEvidence {
			out = append(out, c)
		}
	}
	return out
}
