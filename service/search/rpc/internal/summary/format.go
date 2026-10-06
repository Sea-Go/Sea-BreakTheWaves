// ============================================================================
// summary 交付的前端格式化：answer 内 [n] 角标 → Markdown 锚点链接，
// citations → 脚注式引用列表。
//
// 与前端的约定：CitationCard 按引用 Index 挂锚点 id="cit-<Index>"，
// FormatAnswer 把答案内的 [n] 替换为 [n](#cit-n) 实现页内跳转；
// RenderCitations 输出的列表紧跟答案之后（脚注区）。
// ============================================================================

package summary

import (
	"fmt"
	"strconv"
	"strings"
)

// FormatAnswer 把 result.Answer 内的行内引用角标 [n] 替换为 Markdown
// 链接 `[n](#cit-n)`（前端 CitationCard 挂 #cit-n 锚点）。
//
//   - 只替换 n 命中 result.Citations 中某个 Index 的角标；其余数字
//     方括号（如正文里的 [10]）原样保留；
//   - 角标识别为完整 token（`\[\d+\]`）：[10] 不会被误拆成 [1]0，
//     [1]0 中的 [1] 是合法角标、照常替换；
//   - 引用 Index 重复时返回错误（#cit-n 锚点必须唯一，否则跳转歧义）。
//
// FormatAnswer 是纯格式化器，不做出口校验（闸门是 Validate）；空
// 引用列表时原样返回 answer。
func FormatAnswer(result SummaryResult) (string, error) {
	indexes := make(map[int]bool, len(result.Citations))
	for _, c := range result.Citations {
		if indexes[c.Index] {
			return "", fmt.Errorf("summary: 引用角标 %d 重复，锚点 #cit-%d 不唯一", c.Index, c.Index)
		}
		indexes[c.Index] = true
	}
	return markerRe.ReplaceAllStringFunc(result.Answer, func(m string) string {
		n, err := strconv.Atoi(m[1 : len(m)-1])
		if err != nil || !indexes[n] {
			return m
		}
		return fmt.Sprintf("[%d](#cit-%d)", n, n)
	}), nil
}

// RenderCitations 把引用列表渲染为脚注式文本，每条引用一行：
//
//	[n] docKey §path ¶para — quote
//
// path 为 Locator.SectionPath 以 "/" 连接（空链渲染为空），para 为
// ParaIndex。按 result.Citations 的给出顺序渲染，行间以 "\n" 连接。
func RenderCitations(result SummaryResult) string {
	lines := make([]string, 0, len(result.Citations))
	for _, c := range result.Citations {
		lines = append(lines, fmt.Sprintf("[%d] %s §%s ¶%d — %s",
			c.Index, c.DocKey,
			strings.Join(c.Locator.SectionPath, "/"),
			c.Locator.ParaIndex,
			c.Locator.Quote,
		))
	}
	return strings.Join(lines, "\n")
}
