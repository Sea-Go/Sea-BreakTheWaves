package evalseed

import (
	"reflect"
	"strings"
	"testing"
)

// ============================================================================
// parse_test.go 测试 qrels 与 run 两种文件格式的解析（正常路径 + 错误路径）。
// ============================================================================

// TestParseQrels 验证四列解析、rel<=0 跳过与注释/空行容忍。
func TestParseQrels(t *testing.T) {
	text := "" +
		"# qrels 冻结文件\n" +
		"q-00 0 doc-00 3\n" +
		"q-00 0 doc-05 2\n" +
		"\n" +
		"q-01 0 doc-05 3\n" +
		"q-01 0 doc-99 0\n" // rel=0 应被跳过
	qrels, err := ParseQrels(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	want := Qrels{
		"q-00": {"doc-00": 3, "doc-05": 2},
		"q-01": {"doc-05": 3},
	}
	if !reflect.DeepEqual(qrels, want) {
		t.Errorf("解析结果 %+v，期望 %+v", qrels, want)
	}
}

// TestParseQrels_Errors 验证格式错误的报错。
func TestParseQrels_Errors(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"字段数不足", "q 0 d\n", "字段数期望 4"},
		{"字段数过多", "q 0 d 3 x\n", "字段数期望 4"},
		{"iteration 非整数", "q x d 3\n", "iteration"},
		{"rel 非整数", "q 0 d three\n", "rel 字段"},
		{"rel 为负", "q 0 d -1\n", "不允许为负"},
		{"重复标注", "q 0 d 3\nq 0 d 2\n", "重复标注"},
	}
	for _, c := range cases {
		if _, err := ParseQrels(strings.NewReader(c.text)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s 期望包含 %q 的错误，实际 %v", c.name, c.want, err)
		}
	}
}

// TestParseRun_ThreeColumns 验证三列格式按 rank 升序稳定排序。
func TestParseRun_ThreeColumns(t *testing.T) {
	text := "" +
		"# run\n" +
		"q1 b 2\n" +
		"q1 a 1\n" +
		"q1 d 2\n" + // 与 b 同 rank，保持行序（b 在 d 前）
		"q1 c 3\n"
	run, err := ParseRun(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	want := Run{"q1": {"a", "b", "d", "c"}}
	if !reflect.DeepEqual(run, want) {
		t.Errorf("解析结果 %v，期望 %v", run, want)
	}
}

// TestParseRun_TwoColumns 验证两列格式按行序即 rank 升序。
func TestParseRun_TwoColumns(t *testing.T) {
	text := "q1 c\nq1 a\nq1 b\nq2 z\n"
	run, err := ParseRun(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	want := Run{"q1": {"c", "a", "b"}, "q2": {"z"}}
	if !reflect.DeepEqual(run, want) {
		t.Errorf("解析结果 %v，期望 %v", run, want)
	}
}

// TestParseRun_Errors 验证 run 格式错误的报错。
func TestParseRun_Errors(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"字段数过多", "q d 1 x\n", "字段数期望 2 或 3"},
		{"单列", "q\n", "字段数期望 2 或 3"},
		{"rank 非整数", "q d one\n", "rank 字段"},
		{"rank 为零", "q d 0\n", "必须 >=1"},
		{"rank 为负", "q d -2\n", "必须 >=1"},
		{"docid 重复", "q a 1\nq b 2\nq a 3\n", "重复"},
	}
	for _, c := range cases {
		if _, err := ParseRun(strings.NewReader(c.text)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s 期望包含 %q 的错误，实际 %v", c.name, c.want, err)
		}
	}
}

// TestParseRoundTripEvaluate 组合验证：qrels 文本 + run 文本 → Evaluate。
func TestParseRoundTripEvaluate(t *testing.T) {
	qrels, err := ParseQrels(strings.NewReader("q 0 d1 3\nq 0 d2 1\nq 0 d3 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := ParseRun(strings.NewReader("q d3 1\nq d1 2\nq d4 3\nq d2 4\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := NDCGAt10(qrels, run)
	if err != nil {
		t.Fatal(err)
	}
	approxEqual(t, "文件输入的手算例 A nDCG@10", got, handNDCGA)
}

func TestParseRunRejectsMixedFormats(t *testing.T) {
	mixed := "q-00 doc-01\nq-00 doc-02 1\n"
	if _, err := ParseRun(strings.NewReader(mixed)); err == nil {
		t.Fatal("expected error for mixed two/three column run rows")
	}
	// 同格式（全两列/全三列）不受影响。
	if _, err := ParseRun(strings.NewReader("q-00 doc-01\nq-00 doc-02\n")); err != nil {
		t.Fatalf("two-column run rejected: %v", err)
	}
	if _, err := ParseRun(strings.NewReader("q-00 doc-01 2\nq-00 doc-02 1\n")); err != nil {
		t.Fatalf("three-column run rejected: %v", err)
	}
}
