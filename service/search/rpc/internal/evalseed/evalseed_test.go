package evalseed

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

// ============================================================================
// 手算小例：验证指标数学正确性。
//
// 基准查询 q：qrels = {d1:3, d2:1, d3:2}（|rel|=3）。
// 手算（gain=2^rel-1，折减 log2(rank+1)）：
//
//	run A = [d3,d1,d4,d2]：
//	  DCG@10 = 3/log2(2) + 7/log2(3) + 0/log2(4) + 1/log2(5)
//	         = 7.8471848330735945
//	  IDCG@10 = 7/log2(2) + 3/log2(3) + 1/log2(4) = 9.3927892607143715
//	  nDCG@10 = 0.83544776905563989
//	  Recall@100 = CappedRecall@100 = 3/3 = 1
//	  MRR@10 = 1/1 = 1（d3 在 rank 1）
//
//	run B = [d4,d5,d2,d1]：首个相关文档 d2 在 rank 3。
//	  DCG@10 = 1/log2(4) + 7/log2(5) = 3.5147359065137516
//	  nDCG@10 = 0.37419512020931228；MRR@10 = 1/3
//	  Recall@100 = CappedRecall@100 = 3/3 = 1
//
//	run C = [d3,d1]（只检出前两篇）：
//	  nDCG@10 = (3/1 + 7/log2(3)) / IDCG@10 = 0.78959594100763808
//
//	run D = [d1,d3,d2]：恰为理想排序，nDCG@10 = 1。
// ============================================================================

const (
	handIDCG  = 9.3927892607143715
	handNDCGA = 0.83544776905563989
	handNDCGB = 0.37419512020931228
	handNDCGC = 0.78959594100763808
	epsilon   = 1e-12
)

func handQrels() Qrels {
	return Qrels{"q": {"d1": 3, "d2": 1, "d3": 2}}
}

func approxEqual(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > epsilon {
		t.Errorf("%s = %.17g，期望 %.17g（偏差 %.3g 超过 %g）", name, got, want, math.Abs(got-want), epsilon)
	}
}

// TestNDCGAt10_HandCalc 用手算 DCG/IDCG/nDCG 断言精确值。
func TestNDCGAt10_HandCalc(t *testing.T) {
	qrels := handQrels()
	cases := []struct {
		name string
		run  Run
		want float64
	}{
		{"A", Run{"q": {"d3", "d1", "d4", "d2"}}, handNDCGA},
		{"B", Run{"q": {"d4", "d5", "d2", "d1"}}, handNDCGB},
		{"C", Run{"q": {"d3", "d1"}}, handNDCGC},
		{"D", Run{"q": {"d1", "d3", "d2"}}, 1.0},
	}
	for _, c := range cases {
		got, err := NDCGAt10(qrels, c.run)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		approxEqual(t, "nDCG@10/"+c.name, got, c.want)
	}
}

// TestNDCGAt10_RankDiscount 验证折减项 log2(rank+1)：
// 同一分级放在 rank 1 与 rank 2 的 DCG 差恰为 1/log2(3) 倍。
func TestNDCGAt10_RankDiscount(t *testing.T) {
	qrels := Qrels{"q": {"d1": 3}}
	r1 := Run{"q": {"d1"}}
	r2 := Run{"q": {"dx", "d1"}}
	v1, err := NDCGAt10(qrels, r1)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := NDCGAt10(qrels, r2)
	if err != nil {
		t.Fatal(err)
	}
	// v1 = 1；v2 = (7/log2(3))/(7/1) = 1/log2(3)。
	approxEqual(t, "rank2 折减", v2, 1/math.Log2(3))
	approxEqual(t, "rank1 无折减", v1, 1.0)
}

// TestRecallAndCappedRecall_Denominators 验证 R_cap@k 分母行为：
//   - |rel| < k 时分母为 |rel|（与 Recall 相同）；
//   - |rel| > k 时分母为 k（此时 Recall 分母仍为 |rel|）。
func TestRecallAndCappedRecall_Denominators(t *testing.T) {
	// 情形 1：|rel|=2 < k=100，top100 命中 1 篇。
	qrels := Qrels{"q": {"a": 3, "b": 1}}
	run := Run{"q": {"x", "y", "a"}}
	recall, err := RecallAt100(qrels, run)
	if err != nil {
		t.Fatal(err)
	}
	capped, err := CappedRecallAt100(qrels, run)
	if err != nil {
		t.Fatal(err)
	}
	approxEqual(t, "Recall(|rel|<k)", recall, 1.0/2)
	approxEqual(t, "R_cap(|rel|<k)", capped, 1.0/2)

	// 情形 2：|rel|=150 > k=100，top100 含 60 篇相关。
	big := map[string]int{}
	for i := 0; i < 150; i++ {
		big[fmt.Sprintf("r%03d", i)] = 2
	}
	qrels2 := Qrels{"q": big}
	ranking := make([]string, 0, 100)
	for i := 0; i < 60; i++ {
		ranking = append(ranking, fmt.Sprintf("r%03d", i))
	}
	for i := 0; i < 40; i++ {
		ranking = append(ranking, fmt.Sprintf("n%03d", i))
	}
	run2 := Run{"q": ranking}
	recall2, err := RecallAt100(qrels2, run2)
	if err != nil {
		t.Fatal(err)
	}
	capped2, err := CappedRecallAt100(qrels2, run2)
	if err != nil {
		t.Fatal(err)
	}
	approxEqual(t, "Recall(|rel|>k)", recall2, 60.0/150)
	approxEqual(t, "R_cap(|rel|>k)", capped2, 60.0/100)

	// 情形 3：|rel|=150 > k=100，top100 全为相关文档：
	// R_cap 触顶为 1，Recall 只能到 100/150。
	allRel := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		allRel = append(allRel, fmt.Sprintf("r%03d", i))
	}
	run3 := Run{"q": allRel}
	recall3, err := RecallAt100(qrels2, run3)
	if err != nil {
		t.Fatal(err)
	}
	capped3, err := CappedRecallAt100(qrels2, run3)
	if err != nil {
		t.Fatal(err)
	}
	approxEqual(t, "Recall(top100 全相关)", recall3, 100.0/150)
	approxEqual(t, "R_cap(top100 全相关)", capped3, 1.0)
}

// TestMRRAt10 验证首个相关文档排名的倒数与前十截断。
func TestMRRAt10(t *testing.T) {
	qrels := handQrels()
	cases := []struct {
		name string
		run  Run
		want float64
	}{
		{"首命中 rank1", Run{"q": {"d3", "d1"}}, 1.0},
		{"首命中 rank3", Run{"q": {"d4", "d5", "d2", "d1"}}, 1.0 / 3},
		{"首命中 rank10", Run{"q": {"x1", "x2", "x3", "x4", "x5", "x6", "x7", "x8", "x9", "d1"}}, 0.1},
		{"首命中 rank11 不计", Run{"q": append([]string{"x1", "x2", "x3", "x4", "x5", "x6", "x7", "x8", "x9", "x10"}, "d1")}, 0.0},
		{"无相关文档", Run{"q": {"x1", "x2"}}, 0.0},
	}
	for _, c := range cases {
		got, err := MRRAt10(qrels, c.run)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		approxEqual(t, "MRR@10/"+c.name, got, c.want)
	}
}

// TestNoRelevantDocs 无相关文档的查询各指标记 0（不产生 NaN）。
func TestNoRelevantDocs(t *testing.T) {
	qrels := Qrels{"q": {"d1": 0}} // 显式 0 分
	run := Run{"q": {"d1", "d2"}}
	for name, fn := range map[string]func(Qrels, Run) (float64, error){
		"NDCGAt10":          NDCGAt10,
		"RecallAt100":       RecallAt100,
		"CappedRecallAt100": CappedRecallAt100,
		"MRRAt10":           MRRAt10,
	} {
		got, err := fn(qrels, run)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if math.IsNaN(got) || got != 0 {
			t.Errorf("%s 期望 0（且非 NaN），实际 %v", name, got)
		}
	}
}

// ============================================================================
// 错误处理：空 qrels / 空 run / 未知 qid。
// ============================================================================

// TestInputErrors 验证空输入与未知 qid 的错误路径。
func TestInputErrors(t *testing.T) {
	qrels := handQrels()
	metrics := map[string]func(Qrels, Run) (float64, error){
		"NDCGAt10":          NDCGAt10,
		"RecallAt100":       RecallAt100,
		"CappedRecallAt100": CappedRecallAt100,
		"MRRAt10":           MRRAt10,
	}

	for name, fn := range metrics {
		if _, err := fn(Qrels{}, Run{"q": {"d1"}}); !errors.Is(err, errEmptyQrels) {
			t.Errorf("%s 空 qrels 期望 errEmptyQrels，实际 %v", name, err)
		}
		if _, err := fn(qrels, Run{}); !errors.Is(err, errEmptyRun) {
			t.Errorf("%s 空 run 期望 errEmptyRun，实际 %v", name, err)
		}
		if _, err := fn(qrels, Run{"unknown": {"d1"}}); err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Errorf("%s 未知 qid 期望报错并包含 qid，实际 %v", name, err)
		}
		if _, err := fn(qrels, Run{"q": {"d1", "d1"}}); err == nil || !strings.Contains(err.Error(), "重复") {
			t.Errorf("%s 重复 docid 期望报错并包含重复，实际 %v", name, err)
		}
	}

	if _, err := Evaluate(Qrels{}, Run{"q": {"d1"}}); !errors.Is(err, errEmptyQrels) {
		t.Errorf("Evaluate 空 qrels 期望 errEmptyQrels，实际 %v", err)
	}
	if _, err := Evaluate(qrels, Run{}); !errors.Is(err, errEmptyRun) {
		t.Errorf("Evaluate 空 run 期望 errEmptyRun，实际 %v", err)
	}
	if _, err := Evaluate(qrels, Run{"q": {}, "unknown": {"d1"}}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("Evaluate 未知 qid 期望报错，实际 %v", err)
	}
	if _, err := Evaluate(qrels, Run{"q": {"d1", "d1"}}); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Errorf("Evaluate 重复 docid 期望报错，实际 %v", err)
	}
}

// TestMissingQueryInRunCountsZero 验证 qrels 中存在、run 中缺失的查询记 0 分
// （与 trec_eval 的平均口径一致），且空结果列表同样记 0。
func TestMissingQueryInRunCountsZero(t *testing.T) {
	qrels := Qrels{
		"q1": {"a": 3},
		"q2": {"b": 2},
	}
	run := Run{"q1": {"a"}} // q2 缺失
	rep, err := Evaluate(qrels, run)
	if err != nil {
		t.Fatal(err)
	}
	approxEqual(t, "Report.NDCGAt10", rep.NDCGAt10, 0.5)
	approxEqual(t, "Report.MRRAt10", rep.MRRAt10, 0.5)
	if len(rep.Details) != 2 {
		t.Fatalf("明细期望 2 条，实际 %d", len(rep.Details))
	}
	if rep.Details[0].Qid != "q1" || rep.Details[1].Qid != "q2" {
		t.Errorf("明细未按 qid 排序: %s, %s", rep.Details[0].Qid, rep.Details[1].Qid)
	}
	if d := rep.Details[1]; d.NDCGAt10 != 0 || d.Retrieved != 0 || d.Relevant != 1 {
		t.Errorf("q2 明细期望零分且 Retrieved=0/Relevant=1，实际 %+v", d)
	}

	// run 中显式给出空列表：同样记 0，不报错。
	run2 := Run{"q1": {"a"}, "q2": {}}
	rep2, err := Evaluate(qrels, run2)
	if err != nil {
		t.Fatal(err)
	}
	approxEqual(t, "空列表 Report.NDCGAt10", rep2.NDCGAt10, 0.5)
}

// ============================================================================
// Evaluate 聚合与确定性。
// ============================================================================

// TestEvaluate_AggregatesAndDetails 验证 Report 聚合值与单指标函数一致、
// 明细按 qid 排序且逐查询数值正确。
func TestEvaluate_AggregatesAndDetails(t *testing.T) {
	qrels := Qrels{
		"qB": {"d1": 3, "d2": 1, "d3": 2},
		"qA": {"e1": 2, "e2": 1},
	}
	run := Run{
		"qB": {"d3", "d1", "d4", "d2"},
		"qA": {"e9", "e1"},
	}
	rep, err := Evaluate(qrels, run)
	if err != nil {
		t.Fatal(err)
	}
	if rep.NumQueries != 2 {
		t.Errorf("NumQueries 期望 2，实际 %d", rep.NumQueries)
	}
	if got := []string{rep.Details[0].Qid, rep.Details[1].Qid}; !reflect.DeepEqual(got, []string{"qA", "qB"}) {
		t.Errorf("明细期望按 qid 排序 [qA qB]，实际 %v", got)
	}

	n, err := NDCGAt10(qrels, run)
	if err != nil {
		t.Fatal(err)
	}
	approxEqual(t, "Report.NDCGAt10 == NDCGAt10()", rep.NDCGAt10, n)

	// qB 的单查询值与手算例 A 一致（qA 记入均值）。
	qB := rep.Details[1]
	approxEqual(t, "qB.nDCG@10", qB.NDCGAt10, handNDCGA)
	approxEqual(t, "qB.MRR@10", qB.MRRAt10, 1.0)
	// qA：run=[e9,e1]，DCG=(2^2-1)/log2(3)，IDCG=3/log2(2)+1/log2(3)。
	approxEqual(t, "qA.nDCG@10", rep.Details[0].NDCGAt10,
		(3/math.Log2(3))/(3/math.Log2(2)+1/math.Log2(3)))

	// 聚合 = 单查询值的算术平均。
	m, err := MRRAt10(qrels, run)
	if err != nil {
		t.Fatal(err)
	}
	approxEqual(t, "Report.MRRAt10 == MRRAt10()", rep.MRRAt10, m)
	approxEqual(t, "MRR 均值", m, (0.5+1.0)/2)
}

// TestEvaluate_Deterministic 同输入两次评测必须 Report 深相等。
func TestEvaluate_Deterministic(t *testing.T) {
	qrels := Qrels{
		"q3": {"a": 1, "b": 2, "c": 3},
		"q1": {"x": 2},
		"q2": {"y": 1, "z": 3},
	}
	run := Run{
		"q3": {"c", "b", "a", "n1", "n2"},
		"q1": {"n1", "x"},
		"q2": {"y"},
	}
	r1, err := Evaluate(qrels, run)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Evaluate(qrels, run)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r1, r2) {
		t.Errorf("同输入的两次 Evaluate 不深相等:\n%+v\n%+v", r1, r2)
	}

	// WriteText 输出同样恒定，且 qid 行有序。
	var b1, b2 bytes.Buffer
	if err := r1.WriteText(&b1); err != nil {
		t.Fatal(err)
	}
	if err := r2.WriteText(&b2); err != nil {
		t.Fatal(err)
	}
	if b1.String() != b2.String() {
		t.Errorf("WriteText 两次输出不一致")
	}
	lines := strings.Split(strings.TrimRight(b1.String(), "\n"), "\n")
	if len(lines) != 9 { // num_queries + 4 指标行 + 表头 + 3 条明细
		t.Errorf("WriteText 行数 %d 与期望 9 不符:\n%s", len(lines), b1.String())
	}
	if !strings.Contains(b1.String(), "q1 2 1 ") {
		t.Errorf("WriteText 缺少 q1 明细行:\n%s", b1.String())
	}
}
