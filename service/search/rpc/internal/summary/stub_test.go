// ============================================================================
// stub_test.go：dev stub 的确定性 / 候选选择 / 错误分支。
// fixture 直接构造 EvidencePack（不经 BuildPack），聚焦 B6 域逻辑。
// ============================================================================

package summary

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
)

// mkPack 构造一个通过 EvidencePack.Validate 的证据包。
func mkPack(qid string, cands ...evidence.EvidenceCandidate) evidence.EvidencePack {
	return evidence.EvidencePack{QueryID: qid, Candidates: cands}
}

// cand 构造一个候选：key + RRF 分数 + 若干条 evidence quote（缺省 1 条）。
func cand(key string, rrf float32, quotes ...string) evidence.EvidenceCandidate {
	if len(quotes) == 0 {
		quotes = []string{"关于" + key + "的段落"}
	}
	evs := make([]evidence.Locator, 0, len(quotes))
	for i, q := range quotes {
		evs = append(evs, evidence.Locator{
			RevisionID:  "rev-test",
			SectionPath: []string{"总纲", key},
			ParaIndex:   i + 1,
			Quote:       q,
		})
	}
	return evidence.EvidenceCandidate{DocKey: key, RRFScore: rrf, Evidence: evs}
}

// fivePack 打乱顺序的 5 个候选，RRF 降序应为 doc-b > doc-d > doc-a >
// doc-e > doc-c。
func fivePack() evidence.EvidencePack {
	return mkPack("q-fx",
		cand("doc-a", 0.30),
		cand("doc-b", 0.90),
		cand("doc-c", 0.10),
		cand("doc-d", 0.70),
		cand("doc-e", 0.20),
	)
}

func TestStubSummarize_Deterministic(t *testing.T) {
	req := SummaryRequest{QueryID: "q-fx", Query: "潮汐的成因", Pack: fivePack()}
	s := NewStub()

	r1, err1 := s.Summarize(context.Background(), req)
	r2, err2 := s.Summarize(context.Background(), req)

	if err1 != nil || err2 != nil {
		t.Fatalf("两次调用均应成功：err1=%v err2=%v", err1, err2)
	}
	if !reflect.DeepEqual(r1, r2) {
		t.Fatalf("同输入两次产出应深相等：\n第一次: %+v\n第二次: %+v", r1, r2)
	}
}

func TestStubSummarize_Top3ByRRFDesc(t *testing.T) {
	pack := fivePack() // 顺序见 fivePack 注释：前 3 为 doc-b/doc-d/doc-a
	s := NewStub()

	got, err := s.Summarize(context.Background(), SummaryRequest{Pack: pack})
	if err != nil {
		t.Fatalf("Summarize 失败: %v", err)
	}

	// 答案前缀 + 前 3 候选首条 quote 按序拼接，角标 [1][2][3] 与候选序对应。
	wantAnswer := "根据 5 篇文档……\n" +
		"「关于doc-b的段落」[1]\n" +
		"「关于doc-d的段落」[2]\n" +
		"「关于doc-a的段落」[3]"
	if got.Answer != wantAnswer {
		t.Fatalf("answer 不符：\n got: %q\nwant: %q", got.Answer, wantAnswer)
	}
	if strings.Contains(got.Answer, "doc-c") || strings.Contains(got.Answer, "doc-e") {
		t.Fatalf("第 4、5 名候选不应进入答案: %q", got.Answer)
	}

	// citations 从候选 evidence 取前 3（各取首条 Locator），Index 与候选序对应。
	wantCits := []Citation{
		{Index: 1, DocKey: "doc-b", Locator: pack.Candidates[1].Evidence[0]},
		{Index: 2, DocKey: "doc-d", Locator: pack.Candidates[3].Evidence[0]},
		{Index: 3, DocKey: "doc-a", Locator: pack.Candidates[0].Evidence[0]},
	}
	if !reflect.DeepEqual(got.Citations, wantCits) {
		t.Fatalf("citations 不符：\n got: %+v\nwant: %+v", got.Citations, wantCits)
	}
	if got.QueryID != "q-fx" {
		t.Fatalf("query_id 应继承自证据包: %q", got.QueryID)
	}
}

func TestStubSummarize_TieBreakByDocKey(t *testing.T) {
	// 三者 RRF 相同 → 平局按 doc_key 字典序；第四名分数最低不入选。
	pack := mkPack("q-tie",
		cand("doc-c", 0.5),
		cand("doc-a", 0.5),
		cand("doc-b", 0.5),
		cand("doc-z", 0.4),
	)
	got, err := NewStub().Summarize(context.Background(), SummaryRequest{Pack: pack})
	if err != nil {
		t.Fatalf("Summarize 失败: %v", err)
	}
	wantOrder := []string{"doc-a", "doc-b", "doc-c"}
	for i, want := range wantOrder {
		if got.Citations[i].DocKey != want {
			t.Fatalf("平局候选[%d]应为 %s，实为 %s（citations=%+v）", i, want, got.Citations[i].DocKey, got.Citations)
		}
	}
}

func TestStubSummarize_FewerCandidatesThanTopK(t *testing.T) {
	pack := mkPack("q-one", cand("doc-only", 0.5, "唯一候选的段落", "第二条不入选"))
	got, err := NewStub().Summarize(context.Background(), SummaryRequest{QueryID: "q-one", Pack: pack})
	if err != nil {
		t.Fatalf("Summarize 失败: %v", err)
	}
	wantAnswer := "根据 1 篇文档……\n「唯一候选的段落」[1]"
	if got.Answer != wantAnswer {
		t.Fatalf("answer 不符： got %q want %q", got.Answer, wantAnswer)
	}
	if len(got.Citations) != 1 {
		t.Fatalf("应只有 1 条引用: %+v", got.Citations)
	}
	// 引用取首条 evidence（"第二条不入选" 不出现）。
	if got.Citations[0].Locator.Quote != "唯一候选的段落" {
		t.Fatalf("应取候选首条 evidence: %+v", got.Citations[0].Locator)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("stub 出口必须过校验: %v", err)
	}
}

func TestStubSummarize_RejectsOverlongQuotePack(t *testing.T) {
	// quote >200 rune 的包在入口即被 EvidencePack.Validate 拒绝；
	// stub 内的截断只是双保险（正常链路不可达），不越权修补坏包。
	pack := mkPack("q-long", cand("doc-long", 0.5, strings.Repeat("长", 201)))
	if _, err := NewStub().Summarize(context.Background(), SummaryRequest{Pack: pack}); err == nil {
		t.Fatalf("quote 超 200 rune 的证据包应被入口校验拒绝")
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("短引文", evidence.MaxQuoteRunes); got != "短引文" {
		t.Fatalf("未超限的 quote 应原样返回: %q", got)
	}
	if got := truncateRunes(strings.Repeat("长", 250), evidence.MaxQuoteRunes); got != strings.Repeat("长", 200) {
		t.Fatalf("超限 quote 应截到 200 rune")
	}
	// 多字节字符按 rune 边界截断，不产生半个 rune。
	if got := truncateRunes("ab长", 2); got != "ab" {
		t.Fatalf("应按 rune 边界截断: %q", got)
	}
}

func TestStubSummarize_RejectsInvalidInput(t *testing.T) {
	badCtx, cancel := context.WithCancel(context.Background())
	cancel()

	mismatch := mkPack("q-pack", cand("doc-a", 0.5))
	emptyDoc := mkPack("q-bad", evidence.EvidenceCandidate{DocKey: "", RRFScore: 1, Evidence: []evidence.Locator{{Quote: "x"}}})
	noEvidence := mkPack("q-bad2", evidence.EvidenceCandidate{DocKey: "d", RRFScore: 1})
	emptyPack := mkPack("q-empty")

	cases := []struct {
		name string
		req  SummaryRequest
	}{
		{"ctx 已取消", SummaryRequest{QueryID: "q-1", Pack: mkPack("q-1", cand("d", 1))}},
		{"query_id 不一致", SummaryRequest{QueryID: "q-req", Pack: mismatch}},
		{"包内 doc_key 为空", SummaryRequest{QueryID: "q-bad", Pack: emptyDoc}},
		{"包内候选无 evidence", SummaryRequest{QueryID: "q-bad2", Pack: noEvidence}},
		{"空包（0 候选）", SummaryRequest{QueryID: "q-empty", Pack: emptyPack}},
	}
	for _, tc := range cases {
		ctx := context.Background()
		if tc.name == "ctx 已取消" {
			ctx = badCtx
		}
		got, err := NewStub().Summarize(ctx, tc.req)
		if err == nil {
			t.Fatalf("%s：应报错，实得 %+v", tc.name, got)
		}
		if !reflect.DeepEqual(got, SummaryResult{}) {
			t.Fatalf("%s：失败时应返回零值结果，实得 %+v", tc.name, got)
		}
	}
}
