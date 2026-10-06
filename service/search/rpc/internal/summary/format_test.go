// ============================================================================
// format_test.go：FormatAnswer 的替换（多引用 / 无引用 / 嵌套方括号
// [10] vs [1]0 不误匹配 / 角标重复报错）与 RenderCitations 的脚注格式。
// ============================================================================

package summary

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
)

func formatResult() SummaryResult {
	return SummaryResult{
		QueryID: "q-f",
		Answer:  "先说 [1]，再说 [2]，回扣 [1]。",
		Citations: []Citation{
			{Index: 1, DocKey: "doc-a", Locator: testLocator("引文甲", 1)},
			{Index: 2, DocKey: "doc-b", Locator: testLocator("引文乙", 2)},
		},
	}
}

func TestFormatAnswer_MultipleMarkers(t *testing.T) {
	got, err := FormatAnswer(formatResult())
	if err != nil {
		t.Fatalf("FormatAnswer 失败: %v", err)
	}
	want := "先说 [1](#cit-1)，再说 [2](#cit-2)，回扣 [1](#cit-1)。"
	if got != want {
		t.Fatalf("替换不正确：\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatAnswer_NoMarkers(t *testing.T) {
	// 答案里没有任何 [n] 角标 → 原样返回（格式化器不做出口校验）。
	r := formatResult()
	r.Answer = "一段没有任何角标的答案"
	got, err := FormatAnswer(r)
	if err != nil {
		t.Fatalf("FormatAnswer 失败: %v", err)
	}
	if got != r.Answer {
		t.Fatalf("无角标应原样返回: got %q", got)
	}

	// 空引用列表同样原样返回。
	r2 := SummaryResult{QueryID: "q-n", Answer: "答案内有一个 [3] 但没有引用列表"}
	got2, err := FormatAnswer(r2)
	if err != nil {
		t.Fatalf("FormatAnswer 失败: %v", err)
	}
	if got2 != r2.Answer {
		t.Fatalf("无引用列表应原样返回: got %q", got2)
	}
}

func TestFormatAnswer_NestedBracketsNoMismatch(t *testing.T) {
	// [10] 是完整 token：n=10 不在引用集 {1} 内 → 原样保留，
	// 不得误拆成 "[1](#cit-1)0"；[1]0 中的 [1] 是合法角标 → 替换；
	// [[1]] 内层 [1] 亦是完整 token → 替换（外层方括号原样）。
	r := SummaryResult{
		QueryID: "q-b",
		Answer:  "编号 [10]、随后 [1]0、再嵌套 [[1]]。",
		Citations: []Citation{
			{Index: 1, DocKey: "doc-a", Locator: testLocator("引文甲", 1)},
		},
	}
	got, err := FormatAnswer(r)
	if err != nil {
		t.Fatalf("FormatAnswer 失败: %v", err)
	}
	want := "编号 [10]、随后 [1](#cit-1)0、再嵌套 [[1](#cit-1)]。"
	if got != want {
		t.Fatalf("嵌套方括号替换不正确：\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatAnswer_DuplicateIndex(t *testing.T) {
	r := formatResult()
	r.Citations[1].Index = 1 // 两条引用同为 1 → #cit-1 锚点不唯一
	if _, err := FormatAnswer(r); err == nil {
		t.Fatalf("角标重复应报错")
	}
}

func TestRenderCitations_Format(t *testing.T) {
	r := SummaryResult{
		QueryID: "q-r",
		Citations: []Citation{
			{Index: 1, DocKey: "doc-a", Locator: evidence.Locator{
				SectionPath: []string{"海洋学导论", "第一章 潮汐"}, ParaIndex: 3, Quote: "引文甲",
			}},
			{Index: 2, DocKey: "doc-b", Locator: evidence.Locator{
				SectionPath: nil, ParaIndex: 7, Quote: "引文乙",
			}},
		},
	}
	got := RenderCitations(r)
	want := "[1] doc-a §海洋学导论/第一章 潮汐 ¶3 — 引文甲\n" +
		"[2] doc-b § ¶7 — 引文乙"
	if got != want {
		t.Fatalf("脚注格式不正确：\n got: %q\nwant: %q", got, want)
	}
}

func TestFormat_StubResultEndToEnd(t *testing.T) {
	// stub 产出 → 格式化：行内角标变锚点链接，脚注列表可渲染。
	pack := mkPack("q-e2e",
		cand("doc-b", 0.9),
		cand("doc-a", 0.3),
		cand("doc-c", 0.1),
	)
	res, err := NewStub().Summarize(context.Background(), SummaryRequest{QueryID: "q-e2e", Query: "洋流", Pack: pack})
	if err != nil {
		t.Fatalf("Summarize 失败: %v", err)
	}

	formatted, err := FormatAnswer(res)
	if err != nil {
		t.Fatalf("FormatAnswer 失败: %v", err)
	}
	for i := 1; i <= 3; i++ {
		want := fmt.Sprintf("[%d](#cit-%d)", i, i)
		if !strings.Contains(formatted, want) {
			t.Fatalf("格式化后应含锚点链接 %s: %q", want, formatted)
		}
	}
	footnotes := RenderCitations(res)
	if !strings.Contains(footnotes, "[1] doc-b §") || !strings.Contains(footnotes, "[3] doc-c §") {
		t.Fatalf("脚注列表应含各引用行: %q", footnotes)
	}
}
