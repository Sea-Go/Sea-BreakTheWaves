// ============================================================================
// validate_test.go：SummaryResult.Validate 全分支——空 answer / 超长 /
// 引用数越界 / 角标越界或重复 / 角标与正文不一致（含 [10] 不算 [1]）/
// quote 空或超限 / doc_key 空 / query_id 空。
// ============================================================================

package summary

import (
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
)

// baseResult 一个最小合法结果：answer 含 [1][2] 角标，两条引用。
func baseResult() SummaryResult {
	return SummaryResult{
		QueryID: "q-v",
		Answer:  "先说结论 [1]，再说依据 [2]。",
		Citations: []Citation{
			{Index: 1, DocKey: "doc-a", Locator: testLocator("引文甲", 1)},
			{Index: 2, DocKey: "doc-b", Locator: testLocator("引文乙", 2)},
		},
	}
}

func testLocator(quote string, para int) evidence.Locator {
	return evidence.Locator{
		RevisionID:  "rev-test",
		SectionPath: []string{"章"},
		ParaIndex:   para,
		Quote:       quote,
	}
}

func TestValidate_OK(t *testing.T) {
	if err := baseResult().Validate(); err != nil {
		t.Fatalf("最小合法结果不应报错: %v", err)
	}

	// 边界值：answer 恰 4000 rune、角标 [8]、quote 恰 200 rune。
	edge := SummaryResult{
		QueryID: "q-edge",
		Answer:  strings.Repeat("字", MaxAnswerRunes-4) + " [8]", // 恰 4000 rune,
		Citations: []Citation{
			{Index: 8, DocKey: "doc-edge", Locator: testLocator(strings.Repeat("引", 200), 8)},
		},
	}
	if err := edge.Validate(); err != nil {
		t.Fatalf("边界值结果不应报错: %v", err)
	}
}

func TestValidate_Branches(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SummaryResult)
	}{
		{"query_id 为空", func(r *SummaryResult) { r.QueryID = "" }},
		{"answer 为空", func(r *SummaryResult) { r.Answer = "" }},
		{"answer 超 4000 rune", func(r *SummaryResult) {
			r.Answer = strings.Repeat("字", MaxAnswerRunes-2) + " [1]" // 4001 rune
		}},
		{"citations 为空", func(r *SummaryResult) { r.Citations = nil }},
		{"citations 超上限（9 条）", func(r *SummaryResult) {
			r.Answer = "角标 [1] [2] [3] [4] [5] [6] [7] [8] [9]"
			r.Citations = make([]Citation, 0, 9)
			for i := 1; i <= 9; i++ {
				r.Citations = append(r.Citations, Citation{Index: i, DocKey: "d", Locator: testLocator("q", i)})
			}
		}},
		{"doc_key 为空", func(r *SummaryResult) { r.Citations[0].DocKey = "" }},
		{"角标小于 1", func(r *SummaryResult) {
			r.Answer = "角标 [0] 与 [2]"
			r.Citations[0].Index = 0
		}},
		{"角标大于上限", func(r *SummaryResult) {
			r.Answer = "角标 [1] 与 [9]"
			r.Citations[1].Index = 9
		}},
		{"角标重复", func(r *SummaryResult) {
			r.Answer = "角标 [1] 与 [1]"
			r.Citations[1].Index = 1
		}},
		{"quote 为空", func(r *SummaryResult) { r.Citations[0].Locator.Quote = "" }},
		{"quote 超 200 rune", func(r *SummaryResult) {
			r.Citations[0].Locator.Quote = strings.Repeat("引", 201)
		}},
		{"角标未在 answer 出现（不一致）", func(r *SummaryResult) {
			r.Answer = "只有 [1]，没有第二个角标"
		}},
		{"answer 内 [10] 不能算角标 [1] 出现", func(r *SummaryResult) {
			r.Answer = "编号列表 [10] 不含 [1] 角标" // [10] 是完整 token ≠ [1]
		}},
	}
	for _, tc := range cases {
		r := baseResult()
		tc.mutate(&r)
		if err := r.Validate(); err == nil {
			t.Fatalf("%s：应报错，实得通过（result=%+v）", tc.name, r)
		}
	}
}

func TestValidate_MarkerCounting(t *testing.T) {
	// 计数只认完整 [数字] token：[1] 出现 2 次、[2] 出现 1 次即合法；
	// 额外的 [10] 不影响判定。
	r := SummaryResult{
		QueryID: "q-count",
		Answer:  "甲 [1] 乙 [1] 丙 [2]，另有编号 [10] 不算角标。",
		Citations: []Citation{
			{Index: 1, DocKey: "doc-a", Locator: testLocator("甲", 1)},
			{Index: 2, DocKey: "doc-b", Locator: testLocator("丙", 2)},
		},
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("重复出现的角标应合法: %v", err)
	}
}
