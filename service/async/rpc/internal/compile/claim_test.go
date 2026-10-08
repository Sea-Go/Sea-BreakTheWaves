// ============================================================================
// claim_test.go —— 引用双表与 Claim 提取测试：有/无引用句子分类、悬空
// 编号、首角标优先、换行分句、"待人工补证"清单、双表 JSON 契约。
// ============================================================================

package compile

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// claimCitations 测试用引用表（编号 1、2 有效）。
var claimCitations = []Citation{
	{Index: 1, DocKey: "doc-a", RevisionID: "rev-A",
		Locator: &Locator{SectionPath: []string{"架构"}, ParaIndex: 3, Quote: "检索树按整篇组织"}},
	{Index: 2, DocKey: "doc-b"},
}

// TestExtractClaimsClassification 校验句子分类：有引用（角标命中）→
// HasEvidence；无角标 → 无证据且 SourceIndex=0；悬空编号 → 无证据且
// 保留编号。
func TestExtractClaimsClassification(t *testing.T) {
	text := "检索树是整篇组织形态[1]。这个断言没有任何角标。多向量两段式已是业界共识[2]！悬空编号引用[9]。"
	claims := ExtractClaims(text, claimCitations)
	want := []Claim{
		{Text: "检索树是整篇组织形态[1]。", SourceIndex: 1, HasEvidence: true},
		{Text: "这个断言没有任何角标。", SourceIndex: 0, HasEvidence: false},
		{Text: "多向量两段式已是业界共识[2]！", SourceIndex: 2, HasEvidence: true},
		{Text: "悬空编号引用[9]。", SourceIndex: 9, HasEvidence: false},
	}
	if !reflect.DeepEqual(claims, want) {
		t.Fatalf("claim 分类不符：\n得到 %+v\n想要 %+v", claims, want)
	}
}

// TestExtractClaimsFirstMarkerWins 校验多角标句以首个角标判定。
func TestExtractClaimsFirstMarkerWins(t *testing.T) {
	claims := ExtractClaims("[2][9] 双角标句应以首个为准。", claimCitations)
	if len(claims) != 1 {
		t.Fatalf("应得 1 个 claim，得到 %d", len(claims))
	}
	if claims[0].SourceIndex != 2 || !claims[0].HasEvidence {
		t.Fatalf("首角标 [2] 应命中证据：得到 %+v", claims[0])
	}
	// 首角标悬空而次角标可命中：仍以首角标判（口径：不回溯取次角标）。
	claims = ExtractClaims("[7][1] 首角标悬空。", claimCitations)
	if claims[0].SourceIndex != 7 || claims[0].HasEvidence {
		t.Fatalf("首角标悬空应为无证据：得到 %+v", claims[0])
	}
}

// TestExtractClaimsLineSplitting 校验换行分句（成文器按行组织正文，
// 行边界即句子边界）与空行/空白容错。
func TestExtractClaimsLineSplitting(t *testing.T) {
	text := "第 1 章：要点摘要\n甲 [1]\n乙 [2]\n\n第 2 章：要点摘要\n丙 没有角标"
	claims := ExtractClaims(text, claimCitations)
	want := []Claim{
		{Text: "第 1 章：要点摘要", SourceIndex: 0, HasEvidence: false},
		{Text: "甲 [1]", SourceIndex: 1, HasEvidence: true},
		{Text: "乙 [2]", SourceIndex: 2, HasEvidence: true},
		{Text: "第 2 章：要点摘要", SourceIndex: 0, HasEvidence: false},
		{Text: "丙 没有角标", SourceIndex: 0, HasEvidence: false},
	}
	if !reflect.DeepEqual(claims, want) {
		t.Fatalf("换行分句不符：\n得到 %+v\n想要 %+v", claims, want)
	}
	if got := ExtractClaims("  \n\n \n", nil); len(got) != 0 {
		t.Fatalf("全空白文本应无 claim，得到 %+v", got)
	}
}

// TestUnevidencedClaims 校验"待人工补证"清单：仅含无证据 claim 且保持
// 原序，SourceIndex 区分缺引用（0）与引用表缺条目（>0）。
func TestUnevidencedClaims(t *testing.T) {
	text := "有证据句[1]。无角标句。悬空句[9]。再有证据句[2]。"
	claims := ExtractClaims(text, claimCitations)
	pending := UnevidencedClaims(claims)
	want := []Claim{
		{Text: "无角标句。", SourceIndex: 0, HasEvidence: false},
		{Text: "悬空句[9]。", SourceIndex: 9, HasEvidence: false},
	}
	if !reflect.DeepEqual(pending, want) {
		t.Fatalf("待补证清单不符：\n得到 %+v\n想要 %+v", pending, want)
	}
	if rest := UnevidencedClaims(nil); len(rest) != 0 {
		t.Fatalf("空输入应得空清单，得到 %+v", rest)
	}
}

// TestClaimPipelineOnDevArticle 校验 dev 成文全链：Run 产出的终稿上做
// Claim 提取——引用表齐备时正文角标句全部有证据，标题行进待补证清单
// （dev 口径：claim=非空句子，标题亦计入；见 README 真实化路径）。
func TestClaimPipelineOnDevArticle(t *testing.T) {
	job, err := NewCompileJob("job-c", "mod-c", []SourceRef{
		{DocKey: "doc-a", Summary: "检索系统整篇编码基座", RevisionID: "rev-A"},
		{DocKey: "doc-b", Summary: "多向量两段式召回与融合", RevisionID: "rev-B"},
	})
	if err != nil {
		t.Fatalf("NewCompileJob: %v", err)
	}
	done, err := NewOrchestrator().Run(context.Background(), job, RunOptions{SkipPolish: true}, DevExecutor())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	draft := done.FinalArticle()
	claims := ExtractClaims(draft.FullText(), draft.Citations)
	if len(claims) == 0 {
		t.Fatal("终稿应可提取 claim")
	}
	for _, c := range claims {
		if c.SourceIndex > 0 && !c.HasEvidence {
			t.Fatalf("dev 引用表齐备时不应有悬空角标：%+v", c)
		}
	}
	if len(UnevidencedClaims(claims)) == 0 {
		t.Fatal("dev 口径下标题行应进待补证清单（真实化后由语义级 claim 切分消除）")
	}
}

// TestCitationJSONContract 校验引用双表/claim 的 JSON 契约（snake_case
// 字段、locator 段级定位字段），供前端与落盘消费。
func TestCitationJSONContract(t *testing.T) {
	blob, err := json.Marshal(claimCitations[0])
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"index":1,"doc_key":"doc-a","revision_id":"rev-A",` +
		`"locator":{"section_path":["架构"],"para_index":3,"quote":"检索树按整篇组织"}}`
	if string(blob) != want {
		t.Fatalf("Citation JSON 契约不符：\n得到 %s\n想要 %s", blob, want)
	}
	cb, _ := json.Marshal(Claim{Text: "句[1]。", SourceIndex: 1, HasEvidence: true})
	if string(cb) != `{"text":"句[1]。","source_index":1,"has_evidence":true}` {
		t.Fatalf("Claim JSON 契约不符：%s", cb)
	}
}
