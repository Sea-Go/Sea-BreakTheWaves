package chunk

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	types "sea/type"
)

// Article 表示一篇结构化文章（用于推荐系统入库）。
//
// 文章样式约定（你给的标准）：
//
//	标题 + 封面 + 手打 type 标签类型
//	二级标签 + 文字/图片
//	二级标签 + 文字/图片
//	...
type Article = types.Article

type Section = types.Section

type Block = types.Block

// Chunk 是“精召回”的最小证据单元。
type Chunk = types.Chunk

// SplitResult 是切分输出：粗召回文本 + 精召回 chunk 列表。
type SplitResult = types.SplitResult

// SplitArticle 按照 config.yaml 的 split 参数，把文章切成：
// - 粗召回向量文本（1 条）
// - 精召回 chunk（N 条）
//
// 规则（对应你给的定义）：
// 粗召回向量：标题 + 封面 + 手打 type 标签类型 + 关键词检测 + 各类二级标题
// 精召回向量：二级标题 + 段落内容（包括图片和文字）
func SplitArticle(a Article, maxTokens int, overlapTokens int, keywordTopK int) (SplitResult, error) {
	a.ArticleID = NormalizeArticleID(a.ArticleID, a)
	if strings.TrimSpace(a.Title) == "" {
		return SplitResult{}, errors.New("title 不能为空")
	}

	keywords := DetectKeywords(a, keywordTopK)

	// 拼 coarse text
	var h2s []string
	for _, s := range a.Sections {
		if strings.TrimSpace(s.H2) != "" {
			h2s = append(h2s, strings.TrimSpace(s.H2))
		}
	}

	coarseParts := []string{
		"标题：" + a.Title,
	}
	if a.Cover != "" {
		coarseParts = append(coarseParts, "封面："+a.Cover)
	}
	if len(a.TypeTags) > 0 {
		coarseParts = append(coarseParts, "类型："+strings.Join(a.TypeTags, ","))
	}
	if len(keywords) > 0 {
		coarseParts = append(coarseParts, "关键词："+strings.Join(keywords, ","))
	}
	if len(h2s) > 0 {
		coarseParts = append(coarseParts, "二级标题："+strings.Join(h2s, " | "))
	}

	coarseText := strings.Join(coarseParts, "\n")

	// build fine chunks
	var chunks []Chunk
	chunkSeq := 0
	for _, sec := range a.Sections {
		secText := buildSectionText(sec)
		secText = strings.TrimSpace(secText)
		if secText == "" {
			continue
		}

		// 先按 token 预算分块（只在段落特别长时才会拆）
		parts := SplitByTokenBudget(secText, maxTokens, overlapTokens)
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			chunkSeq++
			chunks = append(chunks, Chunk{
				ChunkID:   BuildChunkID(a.ArticleID, chunkSeq, sec.H2, p),
				ArticleID: a.ArticleID,
				H2:        strings.TrimSpace(sec.H2),
				Content:   p,
				Tokens:    ApproxTokenCount(p),
			})
		}
	}

	return SplitResult{
		CoarseText: coarseText,
		FineChunks: chunks,
		Keywords:   keywords,
	}, nil
}

func buildSectionText(sec Section) string {
	var b strings.Builder
	if strings.TrimSpace(sec.H2) != "" {
		b.WriteString("二级标题：")
		b.WriteString(strings.TrimSpace(sec.H2))
		b.WriteString("\n")
	}
	for _, blk := range sec.Blocks {
		switch strings.ToLower(strings.TrimSpace(blk.Type)) {
		case "image":
			if strings.TrimSpace(blk.ImageURL) != "" {
				b.WriteString("图片：")
				b.WriteString(strings.TrimSpace(blk.ImageURL))
				b.WriteString("\n")
			}
		default:
			if strings.TrimSpace(blk.Text) != "" {
				b.WriteString(strings.TrimSpace(blk.Text))
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// DetectKeywords 做一个“轻量关键词检测”。
// 目标：粗召回要能覆盖文章主题，但不要引入高成本/高耦合的 NLP 依赖。
// 这里采用可维护的折中：
// - type_tags / tags 直接作为关键词
// - 标题、二级标题里出现的 #话题 也计入关键词
func DetectKeywords(a Article, topK int) []string {
	m := map[string]struct{}{}

	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		m[s] = struct{}{}
	}

	for _, t := range a.TypeTags {
		add(t)
	}
	for _, t := range a.Tags {
		add(t)
	}

	// 识别 #话题
	hashTagRe := regexp.MustCompile(`#([\p{Han}A-Za-z0-9_\-]+)`)
	for _, s := range append([]string{a.Title}, collectH2(a)...) {
		for _, sub := range hashTagRe.FindAllStringSubmatch(s, -1) {
			if len(sub) == 2 {
				add(sub[1])
			}
		}
	}

	// 输出时保持稳定：按“出现顺序”粗略稳定（这里不做复杂排序）
	res := make([]string, 0, len(m))
	for k := range m {
		res = append(res, k)
	}
	if topK > 0 && len(res) > topK {
		res = res[:topK]
	}
	return res
}

func collectH2(a Article) []string {
	var hs []string
	for _, s := range a.Sections {
		if strings.TrimSpace(s.H2) != "" {
			hs = append(hs, strings.TrimSpace(s.H2))
		}
	}
	return hs
}

// ApproxTokenCount：近似 token 计数。
// 说明：不同模型的 tokenizer 不同，这里只做“切分预算”的粗估。
// 规则：按 rune 数统计（中文场景相对可用）。
func ApproxTokenCount(s string) int {
	return utf8.RuneCountInString(s)
}

// SplitByTokenBudget 将文本按 token 预算切分，并保留 overlap（防止检索丢边界信息）。
//
// ⚠️ 该函数只用于“很长段落”的兜底拆分：整体上我们仍以“二级标题”为单位组织 chunk，避免切得太碎。
func SplitByTokenBudget(text string, maxTokens int, overlapTokens int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxTokens <= 0 {
		return []string{text}
	}

	// 先按换行切，保持自然段结构
	paras := strings.Split(text, "\n")
	var chunks []string

	var cur []string
	curTokens := 0

	flush := func() {
		if len(cur) == 0 {
			return
		}
		chunks = append(chunks, strings.TrimSpace(strings.Join(cur, "\n")))
		cur = nil
		curTokens = 0
	}

	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		t := ApproxTokenCount(p)
		if curTokens+t <= maxTokens {
			cur = append(cur, p)
			curTokens += t
			continue
		}

		// 当前块满了
		flush()

		// 单段过长：按字符粗拆（保证不会无限大）
		if t > maxTokens {
			parts := splitLongLine(p, maxTokens, overlapTokens)
			chunks = append(chunks, parts...)
			continue
		}

		cur = append(cur, p)
		curTokens = t
	}

	flush()

	// overlap：把上一个 chunk 的尾部带到下一个 chunk 的开头（轻量实现）
	if overlapTokens > 0 && len(chunks) > 1 {
		var withOverlap []string
		withOverlap = append(withOverlap, chunks[0])
		for i := 1; i < len(chunks); i++ {
			prev := chunks[i-1]
			tail := tailByTokens(prev, overlapTokens)
			withOverlap = append(withOverlap, strings.TrimSpace(tail+"\n"+chunks[i]))
		}
		chunks = withOverlap
	}

	return chunks
}

func splitLongLine(s string, maxTokens int, overlapTokens int) []string {
	runes := []rune(s)
	if len(runes) <= maxTokens {
		return []string{s}
	}

	step := maxTokens
	if overlapTokens > 0 && overlapTokens < maxTokens {
		step = maxTokens - overlapTokens
	}

	var res []string
	for i := 0; i < len(runes); i += step {
		end := i + maxTokens
		if end > len(runes) {
			end = len(runes)
		}
		res = append(res, string(runes[i:end]))
		if end == len(runes) {
			break
		}
	}
	return res
}

func tailByTokens(s string, tokens int) string {
	r := []rune(s)
	if tokens <= 0 || len(r) <= tokens {
		return s
	}
	return string(r[len(r)-tokens:])
}
