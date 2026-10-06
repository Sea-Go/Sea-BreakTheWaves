// ============================================================================
// executor_test.go —— C5 四个 dev 执行器的确定性与契约测试：
// 高频词要点/固定视角、均分大纲、成文引用（行内 [n]+引用表）、润色
// 原样+末尾声明；以及各执行器的入口护栏。
// ============================================================================

package compile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestDevCuratorPoints 校验要点提取：高频词排序（次数降序、并列按
// 字典序）、ASCII 小写化、top-8 上限。
func TestDevCuratorPoints(t *testing.T) {
	srcs := []SourceRef{
		{DocKey: "d1", Summary: "检索系统 检索系统"},            // bigram 检索/索系/系统 各 2 次
		{DocKey: "d2", Summary: "FaaS faas Serverless"}, // faas:2 serverless:1
	}
	out, err := DevCurator{}.Execute(context.Background(), StageInput{Sources: srcs})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// 期望：检索:2、索系:2、系统:2、faas:2 并列 4 项按字节序（ASCII 在前：
	// faas < 检索 < 系统 < 索系），随后 serverless:1。
	want := []string{"faas", "检索", "系统", "索系", "serverless"}
	if !reflect.DeepEqual(out.Points, want) {
		t.Fatalf("要点提取不符：得到 %v 想 %v", out.Points, want)
	}
	if len(out.Perspectives) != 3 {
		t.Fatalf("应恒产 3 个视角问题，得到 %d 个", len(out.Perspectives))
	}
	if !reflect.DeepEqual(out.Perspectives, devPerspectives) {
		t.Fatalf("视角问题应为固定集，得到 %v", out.Perspectives)
	}
}

// TestDevCuratorTopN 校验 top-8 上限与并列字典序。
func TestDevCuratorTopN(t *testing.T) {
	// 10 个各出现 1 次的 ASCII 词 → 取字典序前 8。
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"}
	srcs := []SourceRef{{DocKey: "d1", Summary: strings.Join(words, " ")}}
	out, err := DevCurator{}.Execute(context.Background(), StageInput{Sources: srcs})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out.Points) != maxDevPoints {
		t.Fatalf("应截取前 %d 个要点，得到 %d", maxDevPoints, len(out.Points))
	}
	want := append([]string(nil), words[:maxDevPoints]...)
	if !reflect.DeepEqual(out.Points, want) {
		t.Fatalf("并列时按字典序取前 8：得到 %v 想 %v", out.Points, want)
	}
}

// TestDevCuratorGuards 校验入口护栏：无 sources 报错、无可提取词元报错。
func TestDevCuratorGuards(t *testing.T) {
	if _, err := (DevCurator{}.Execute(context.Background(), StageInput{})); !errors.Is(err, ErrNoSources) {
		t.Fatalf("无 sources 应报 ErrNoSources，得到 %v", err)
	}
	empty := []SourceRef{{DocKey: "d1", Summary: "？！，。"}}
	if _, err := (DevCurator{}.Execute(context.Background(), StageInput{Sources: empty})); !errors.Is(err, ErrNoPoints) {
		t.Fatalf("无词元应报 ErrNoPoints，得到 %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (DevCurator{}.Execute(ctx, StageInput{Sources: devSources(1)})); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 取消应即返回，得到 %v", err)
	}
}

// TestDevOutlinerEvenSplit 校验均分口径：12 要点→5 章为 3/3/2/2/2，
// 标题固定、要点下标连续覆盖。
func TestDevOutlinerEvenSplit(t *testing.T) {
	points := make([]string, 12)
	for i := range points {
		points[i] = "要点" + string(rune('A'+i))
	}
	out, err := DevOutliner{}.Execute(context.Background(), StageInput{Points: points})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	secs := out.Outline.Sections
	if len(secs) != 5 {
		t.Fatalf("12 要点应分 5 章，得到 %d", len(secs))
	}
	wantSizes := []int{3, 3, 2, 2, 2}
	pos := 0
	for i, sec := range secs {
		if sec.Title != "第 "+string(rune('1'+i))+" 章：要点摘要" {
			t.Fatalf("章 %d 标题不符：%q", i, sec.Title)
		}
		if len(sec.PointIndexes) != wantSizes[i] {
			t.Fatalf("章 %d 应 %d 个要点，得到 %d", i, wantSizes[i], len(sec.PointIndexes))
		}
		for j, idx := range sec.PointIndexes {
			if idx != pos+j {
				t.Fatalf("章 %d 下标不连续：%v", i, sec.PointIndexes)
			}
		}
		pos += len(sec.PointIndexes)
	}
	if pos != 12 {
		t.Fatalf("要点应全覆盖，覆盖 %d/12", pos)
	}
}

// TestDevOutlinerSmallCounts 校验少量要点：3 要点 3 章、1 要点 1 章、
// 0 要点报错。
func TestDevOutlinerSmallCounts(t *testing.T) {
	out, err := DevOutliner{}.Execute(context.Background(), StageInput{Points: []string{"甲", "乙", "丙"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out.Outline.Sections) != 3 || len(out.Outline.Sections[0].PointIndexes) != 1 {
		t.Fatalf("3 要点应 3 章各 1 要点，得到 %+v", out.Outline.Sections)
	}
	out, err = DevOutliner{}.Execute(context.Background(), StageInput{Points: []string{"独"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out.Outline.Sections) != 1 || out.Outline.Sections[0].Title != "第 1 章：要点摘要" {
		t.Fatalf("1 要点应 1 章，得到 %+v", out.Outline.Sections)
	}
	if _, err := (DevOutliner{}.Execute(context.Background(), StageInput{})); !errors.Is(err, ErrNoPoints) {
		t.Fatalf("0 要点应报 ErrNoPoints，得到 %v", err)
	}
}

// TestDevArticleWriterCitations 校验成文：正文行=要点+[n] 角标（从 1
// 递增、按源数回卷），引用表=编号 1..S 且 doc_key/revision 取自 sources。
func TestDevArticleWriterCitations(t *testing.T) {
	in := StageInput{
		Sources: []SourceRef{
			{DocKey: "doc-a", Summary: "s", RevisionID: "rev-A"},
			{DocKey: "doc-b", Summary: "s"},
		},
		Points: []string{"甲", "乙", "丙"},
		Outline: &OutlineTree{Sections: []Section{
			{Title: "第 1 章：要点摘要", PointIndexes: []int{0}},
			{Title: "第 2 章：要点摘要", PointIndexes: []int{1, 2}},
		}},
	}
	out, err := DevArticleWriter{}.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	draft := out.Article
	wantBody := []string{"甲 [1]", "乙 [2]\n丙 [1]"}
	for i, sec := range draft.Sections {
		if sec.Body != wantBody[i] {
			t.Fatalf("章 %d 正文不符：得到 %q 想 %q", i, sec.Body, wantBody[i])
		}
	}
	wantCit := []Citation{
		{Index: 1, DocKey: "doc-a", RevisionID: "rev-A"},
		{Index: 2, DocKey: "doc-b"},
	}
	if !reflect.DeepEqual(draft.Citations, wantCit) {
		t.Fatalf("引用表不符：得到 %+v 想 %+v", draft.Citations, wantCit)
	}
	// 全文形态供 Claim 提取：行内角标可被 ExtractClaims 命中。
	claims := ExtractClaims(draft.FullText(), draft.Citations)
	if len(claims) == 0 {
		t.Fatal("成文全文应可提取 claim")
	}
}

// TestDevArticleWriterGuards 校验大纲一致性护栏与入口护栏。
func TestDevArticleWriterGuards(t *testing.T) {
	ctx := context.Background()
	base := StageInput{
		Sources: devSources(2),
		Points:  []string{"甲", "乙"},
		Outline: &OutlineTree{Sections: []Section{{Title: "t", PointIndexes: []int{0, 1}}}},
	}
	if _, err := (DevArticleWriter{}.Execute(ctx, StageInput{Points: base.Points, Outline: base.Outline})); !errors.Is(err, ErrNoSources) {
		t.Fatalf("无 sources 应报 ErrNoSources，得到 %v", err)
	}
	if _, err := (DevArticleWriter{}.Execute(ctx, StageInput{Sources: base.Sources, Outline: base.Outline})); !errors.Is(err, ErrNoPoints) {
		t.Fatalf("无 points 应报 ErrNoPoints，得到 %v", err)
	}
	if _, err := (DevArticleWriter{}.Execute(ctx, StageInput{Sources: base.Sources, Points: base.Points})); !errors.Is(err, ErrNoOutline) {
		t.Fatalf("无 outline 应报 ErrNoOutline，得到 %v", err)
	}
	// 下标越界。
	oob := StageInput{Sources: base.Sources, Points: base.Points,
		Outline: &OutlineTree{Sections: []Section{{Title: "t", PointIndexes: []int{5}}}}}
	if _, err := (DevArticleWriter{}.Execute(ctx, oob)); !errors.Is(err, ErrOutlineMismatch) {
		t.Fatalf("越界下标应报 ErrOutlineMismatch，得到 %v", err)
	}
	// 下标重叠（非严格递增）。
	overlap := StageInput{Sources: base.Sources, Points: base.Points,
		Outline: &OutlineTree{Sections: []Section{{Title: "t", PointIndexes: []int{1, 0}}}}}
	if _, err := (DevArticleWriter{}.Execute(ctx, overlap)); !errors.Is(err, ErrOutlineMismatch) {
		t.Fatalf("重叠下标应报 ErrOutlineMismatch，得到 %v", err)
	}
}

// TestDevPolisherPassthrough 校验润色：正文原样+末章追加声明、引用表
// 原样携带、去重说明为空。
func TestDevPolisherPassthrough(t *testing.T) {
	src := &ArticleDraft{
		Sections: []SectionDraft{
			{Title: "第 1 章：要点摘要", Body: "甲 [1]"},
			{Title: "第 2 章：要点摘要", Body: "乙 [2]"},
		},
		Citations: []Citation{{Index: 1, DocKey: "doc-a"}, {Index: 2, DocKey: "doc-b"}},
	}
	out, err := DevPolisher{}.Execute(context.Background(), StageInput{Article: src})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.Polished
	if got.Sections[0].Body != "甲 [1]" {
		t.Fatalf("非末章正文应原样：得到 %q", got.Sections[0].Body)
	}
	want := "乙 [2]\n\n" + devPolishDeclaration
	if got.Sections[1].Body != want {
		t.Fatalf("末章应追加声明：得到 %q 想 %q", got.Sections[1].Body, want)
	}
	if !reflect.DeepEqual(got.Citations, src.Citations) {
		t.Fatalf("引用表应原样携带：%+v", got.Citations)
	}
	if len(out.DedupNotes) != 0 {
		t.Fatalf("dev 润色不去重，DedupNotes 应为空：%v", out.DedupNotes)
	}
	if len(out.Points) != 0 || out.Article != nil || out.Outline != nil {
		t.Fatalf("polish 输出只应填 Polished/DedupNotes：%+v", out)
	}
	if _, err := (DevPolisher{}.Execute(context.Background(), StageInput{})); !errors.Is(err, ErrNoArticle) {
		t.Fatalf("无 article 应报 ErrNoArticle，得到 %v", err)
	}
}

// TestDevExecutorsDeterministic 校验四个 dev 执行器同输入同输出
// （纯域层的确定性底线：两次执行 DeepEqual）。
func TestDevExecutorsDeterministic(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		fn   StageFunc
		in   StageInput
	}{
		{"curator", DevCurator{}, StageInput{Sources: devSources(3)}},
		{"outliner", DevOutliner{}, StageInput{Points: []string{"甲", "乙", "丙", "丁"}}},
		{"writer", DevArticleWriter{}, StageInput{
			Sources: devSources(2),
			Points:  []string{"甲", "乙", "丙", "丁"},
			Outline: &OutlineTree{Sections: []Section{
				{Title: "第 1 章：要点摘要", PointIndexes: []int{0, 1}},
				{Title: "第 2 章：要点摘要", PointIndexes: []int{2, 3}},
			}},
		}},
		{"polisher", DevPolisher{}, StageInput{Article: &ArticleDraft{
			Sections:  []SectionDraft{{Title: "t", Body: "甲 [1]"}},
			Citations: []Citation{{Index: 1, DocKey: "doc-a"}},
		}}},
	}
	for _, c := range cases {
		first, err := c.fn.Execute(ctx, c.in)
		if err != nil {
			t.Fatalf("%s 首次执行: %v", c.name, err)
		}
		for i := 0; i < 3; i++ {
			again, err := c.fn.Execute(ctx, c.in)
			if err != nil {
				t.Fatalf("%s 第 %d 次重复执行: %v", c.name, i+2, err)
			}
			if !reflect.DeepEqual(first, again) {
				t.Fatalf("%s 执行不确定：\n首次 %+v\n重复 %+v", c.name, first, again)
			}
		}
	}
}
