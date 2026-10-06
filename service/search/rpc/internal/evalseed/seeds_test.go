package evalseed

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// ============================================================================
// seeds_test.go 用仓库冻结的 M0 种子集（testdata/index/seeds）做冒烟验证：
//   - 冻结 qrels.txt 可解析且规模符合声明；
//   - 按 gold 降序构造的“理想 run”应得到满分（数学自检）；
//   - run 缺失部分查询时按 0 分计入均值（trec_eval 口径）。
// ============================================================================

// seedsQrelsPath 相对包目录（service/search/rpc/internal/evalseed）指向仓库根的冻结种子集。
const seedsQrelsPath = "../../../../../testdata/index/seeds/qrels.txt"

func loadFrozenQrels(t *testing.T) Qrels {
	t.Helper()
	path, err := filepath.Abs(seedsQrelsPath)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开冻结 qrels 失败（%s）: %v", path, err)
	}
	defer f.Close()
	qrels, err := ParseQrels(f)
	if err != nil {
		t.Fatalf("解析冻结 qrels 失败: %v", err)
	}
	return qrels
}

// TestFrozenQrelsShape 验证冻结种子集的规模与分级范围。
func TestFrozenQrelsShape(t *testing.T) {
	qrels := loadFrozenQrels(t)
	if len(qrels) != 50 {
		t.Errorf("查询数期望 50，实际 %d", len(qrels))
	}
	total := 0
	for qid, rels := range qrels {
		if len(rels) == 0 {
			t.Errorf("%s 无任何 rel>0 标注", qid)
		}
		for doc, rel := range rels {
			if rel < 1 || rel > 3 {
				t.Errorf("%s 对 %s 的分级 %d 超出 [1,3]", qid, doc, rel)
			}
			total++
		}
	}
	if total != 140 {
		t.Errorf("qrels 总行数期望 140，实际 %d", total)
	}
}

// TestFrozenQrelsOracleRun 理想 run（gold 分级降序、docid 升序）应得全满分：
// nDCG=Recall=R_cap=MRR=1；且单指标函数与 Evaluate 聚合一致。
func TestFrozenQrelsOracleRun(t *testing.T) {
	qrels := loadFrozenQrels(t)
	run := make(Run, len(qrels))
	for qid, rels := range qrels {
		docs := make([]string, 0, len(rels))
		for doc := range rels {
			docs = append(docs, doc)
		}
		sort.Slice(docs, func(i, j int) bool {
			ri, rj := rels[docs[i]], rels[docs[j]]
			if ri != rj {
				return ri > rj
			}
			return docs[i] < docs[j]
		})
		run[qid] = docs
	}

	rep, err := Evaluate(qrels, run)
	if err != nil {
		t.Fatal(err)
	}
	if rep.NumQueries != 50 {
		t.Errorf("NumQueries 期望 50，实际 %d", rep.NumQueries)
	}
	for name, got := range map[string]float64{
		"NDCGAt10":          rep.NDCGAt10,
		"RecallAt100":       rep.RecallAt100,
		"CappedRecallAt100": rep.CappedRecallAt100,
		"MRRAt10":           rep.MRRAt10,
	} {
		approxEqual(t, "理想 run 的 "+name, got, 1.0)
	}
	// 明细按 qid 排序。
	for i := 1; i < len(rep.Details); i++ {
		if rep.Details[i-1].Qid >= rep.Details[i].Qid {
			t.Fatalf("明细未按 qid 严格升序: %s >= %s", rep.Details[i-1].Qid, rep.Details[i].Qid)
		}
	}

	// 单指标函数与聚合一致。
	for name, fn := range map[string]func(Qrels, Run) (float64, error){
		"NDCGAt10":          NDCGAt10,
		"RecallAt100":       RecallAt100,
		"CappedRecallAt100": CappedRecallAt100,
		"MRRAt10":           MRRAt10,
	} {
		v, err := fn(qrels, run)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		approxEqual(t, "单指标 "+name+" 与聚合一致", v, 1.0)
	}
}

// TestFrozenQrelsPartialRun run 缺失最后一个查询时，该查询记 0 分，
// 全满分指标应退化为 49/50（trec_eval 平均口径）。
func TestFrozenQrelsPartialRun(t *testing.T) {
	qrels := loadFrozenQrels(t)
	qids := make([]string, 0, len(qrels))
	for qid := range qrels {
		qids = append(qids, qid)
	}
	sort.Strings(qids)

	run := make(Run, len(qrels))
	for _, qid := range qids[:len(qids)-1] { // 丢弃最后一个 qid
		rels := qrels[qid]
		docs := make([]string, 0, len(rels))
		for doc := range rels {
			docs = append(docs, doc)
		}
		sort.Strings(docs)
		run[qid] = docs
	}
	rep, err := Evaluate(qrels, run)
	if err != nil {
		t.Fatal(err)
	}
	want := 49.0 / 50.0
	approxEqual(t, "缺一查询的 MRR@10", rep.MRRAt10, want)
	approxEqual(t, "缺一查询的 Recall@100", rep.RecallAt100, want)
	if d := rep.Details[len(rep.Details)-1]; d.Qid != qids[len(qids)-1] || d.MRRAt10 != 0 {
		t.Errorf("被缺失查询的明细期望 0 分，实际 %+v", d)
	}
}
