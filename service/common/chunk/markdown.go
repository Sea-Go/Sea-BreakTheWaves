package chunk

import (
	"errors"
	"regexp"
	"strings"
)

// ParseMarkdownArticle 把符合约定的 Markdown 文档解析成 Article。
// 约定：
// - 一级标题：# 标题
// - 封面：紧跟标题后的第一张图片（Markdown 图片语法）
// - type 标签：形如 `type: xxx,yyy` 或 `类型: xxx`
// - 二级标题：## 章节
// - 章节内容：文字段落与图片混排
//
// 注意：这里是“工程可维护优先”的解析器：
// - 足够支撑你描述的文章样式
// - 不追求完整 Markdown AST（避免引入复杂依赖）
// 如果未来文章格式更复杂，建议引入专门的 Markdown 解析库并做单元测试覆盖。
func ParseMarkdownArticle(articleID string, md string, fallbackTitle string) (Article, error) {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	lines := strings.Split(md, "\n")

	var title string
	var cover string
	var typeTags []string
	titleFoundInMarkdown := false

	imgRe := regexp.MustCompile(`!\[[^\]]*\]\(([^\)]+)\)`)
	typeRe := regexp.MustCompile(`^(type|类型)\s*:\s*(.+)$`)

	// 1) 找 title（第一个 # ）
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(ln, "# "))
			titleFoundInMarkdown = true
			break
		}
	}
	if title == "" {
		title = strings.TrimSpace(fallbackTitle)
	}
	if title == "" {
		return Article{}, errors.New("未找到一级标题（# 标题），且未提供文章标题")
	}

	// 2) 找封面：title 之后第一张图片
	seenTitle := !titleFoundInMarkdown
	for _, ln := range lines {
		lnTrim := strings.TrimSpace(ln)
		if strings.HasPrefix(lnTrim, "# ") {
			seenTitle = true
			continue
		}
		if !seenTitle {
			continue
		}
		m := imgRe.FindStringSubmatch(lnTrim)
		if len(m) == 2 {
			cover = strings.TrimSpace(m[1])
			break
		}
	}

	// 3) 找 type 标签
	for _, ln := range lines {
		lnTrim := strings.TrimSpace(ln)
		m := typeRe.FindStringSubmatch(lnTrim)
		if len(m) == 3 {
			tags := strings.Split(m[2], ",")
			for _, t := range tags {
				t = strings.TrimSpace(t)
				if t != "" {
					typeTags = append(typeTags, t)
				}
			}
			break
		}
	}

	// 4) 解析二级标题分段
	var sections []Section
	var cur *Section
	var buf []string
	bodyStarted := !titleFoundInMarkdown

	flushText := func() {
		if cur == nil || len(buf) == 0 {
			buf = nil
			return
		}
		text := strings.TrimSpace(strings.Join(buf, "\n"))
		if text != "" {
			cur.Blocks = append(cur.Blocks, Block{Type: "text", Text: text})
		}
		buf = nil
	}

	for _, ln := range lines {
		lnTrim := strings.TrimSpace(ln)
		if strings.HasPrefix(lnTrim, "# ") {
			bodyStarted = true
			flushText()
			continue
		}
		if !bodyStarted {
			continue
		}
		if typeRe.MatchString(lnTrim) {
			continue
		}
		if strings.HasPrefix(lnTrim, "## ") {
			// 新章节开始
			flushText()
			if cur != nil {
				sections = append(sections, *cur)
			}
			h2 := strings.TrimSpace(strings.TrimPrefix(lnTrim, "## "))
			cur = &Section{H2: h2}
			continue
		}

		// 普通文本：先缓冲，遇到空行再 flush
		if lnTrim == "" {
			flushText()
			continue
		}

		if cur == nil {
			cur = &Section{}
		}

		// 图片
		img := imgRe.FindStringSubmatch(lnTrim)
		if len(img) == 2 {
			flushText()
			cur.Blocks = append(cur.Blocks, Block{Type: "image", ImageURL: strings.TrimSpace(img[1])})
			continue
		}
		buf = append(buf, lnTrim)
	}

	flushText()
	if cur != nil {
		sections = append(sections, *cur)
	}

	a := Article{
		ArticleID: articleID,
		Title:     title,
		Cover:     cover,
		TypeTags:  typeTags,
		Sections:  sections,
		Score:     0,
	}
	a.ArticleID = NormalizeArticleID(articleID, a)
	return a, nil
}
