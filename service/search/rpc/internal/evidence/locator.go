// ============================================================================
// MapHitToLocator：文档级命中区间 → RTW structure 契约下的 Locator。
//
// 这是 B5 证据域的核心纯函数：检索层给出冻结修订源文本的一个字符区间
// [charStart, charEnd)，本函数把它映射为可跨服务传输、可被 RTW
// structure.Anchor 反向验证的 Locator。
// ============================================================================

package evidence

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ErrHitCrossParagraph 命中区间未完整落在单一段落节点内：跨段、落在标题
// 行上、或落在节点之间的空白带上。调用方应把命中按段细分后重试。
var ErrHitCrossParagraph = errors.New("命中跨段，请按段细分")

// MapHitToLocator 把命中区间 [charStart, charEnd)（源文本字节偏移）映射为
// Locator。规则：
//
//  1. 区间必须落在 source 边界内且非空（charStart < charEnd）；
//  2. 区间必须完整落在某一个段落节点（LevelParagraph）内——跨段或落在
//     标题上返回包装 ErrHitCrossParagraph 的错误（标题组织文档但永不
//     承载证据，与 RTW structure.Anchor 的锚定规则一致）；
//  3. revision_id 取结构树；para_index 取该段落节点；quote 取区间文本
//     TrimSpace 后的前 MaxQuoteRunes 个 rune（不足则原样）；
//  4. section_path 为从根到此段落之前最近的标题链：按文档序扫描该段落
//     之前的所有标题，维护层级栈（level ≤ 栈顶则弹栈，再压入该标题），
//     最终栈即路径（层级严格递增）。
//
// 空白区间（TrimSpace 后为空串）无法构造有效 quote，返回错误。
// TreeJSON 的结构合法性（层级 1..6/7、区间有序、para_index 全局序）由
// RTW structure.Derive 保证，本函数只校验自身逻辑依赖的前提。
func MapHitToLocator(tree TreeJSON, source []byte, charStart, charEnd int) (Locator, error) {
	if tree.RevisionID == "" {
		return Locator{}, fmt.Errorf("evidence: 结构树缺少 revision_id")
	}
	if charStart < 0 || charEnd > len(source) {
		return Locator{}, fmt.Errorf("evidence: 命中区间 [%d,%d) 越界（源文本长度 %d 字节）", charStart, charEnd, len(source))
	}
	if charStart >= charEnd {
		return Locator{}, fmt.Errorf("evidence: 命中区间 [%d,%d) 为空", charStart, charEnd)
	}

	// 定位完整包含命中区间的段落节点（按文档序取第一个）。
	idx := -1
	for i := range tree.Nodes {
		n := &tree.Nodes[i]
		if n.Level != LevelParagraph {
			continue
		}
		if n.CharStart <= charStart && charEnd <= n.CharEnd {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Locator{}, fmt.Errorf("evidence: %w：区间 [%d,%d) 未完整落在段落节点内", ErrHitCrossParagraph, charStart, charEnd)
	}

	// 计算从根到此段落的标题链：扫描此段落之前（文档序）的所有标题，
	// 维护层级栈：level <= top 则弹栈，压入该标题；最终栈即路径。
	var stack []NodeJSON
	for i := 0; i < idx; i++ {
		n := tree.Nodes[i]
		if n.Level == LevelParagraph {
			continue
		}
		for len(stack) > 0 && n.Level <= stack[len(stack)-1].Level {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, n)
	}
	sectionPath := make([]string, 0, len(stack))
	for _, n := range stack {
		sectionPath = append(sectionPath, n.Title)
	}

	// quote：区间文本 TrimSpace，超 MaxQuoteRunes 个 rune 则取前 MaxQuoteRunes 个。
	quote := strings.TrimSpace(string(source[charStart:charEnd]))
	if quote == "" {
		return Locator{}, fmt.Errorf("evidence: 命中区间 [%d,%d) 文本为空白，无法构造 quote", charStart, charEnd)
	}
	if utf8.RuneCountInString(quote) > MaxQuoteRunes {
		quote = string([]rune(quote)[:MaxQuoteRunes])
	}

	return Locator{
		RevisionID:  tree.RevisionID,
		SectionPath: sectionPath,
		ParaIndex:   tree.Nodes[idx].ParaIndex,
		Quote:       quote,
	}, nil
}
