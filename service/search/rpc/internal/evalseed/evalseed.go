// ============================================================================
// evalseed 包实现 M0 检索评测的分级指标计算器。
//
// 输入：
//   - qrels：人工分级相关性标注（qid → docid → rel，0-3 分级），来源为
//     仓库根目录 testdata/index/seeds/qrels.txt（trec qrels 风格四列格式）；
//   - run：待评测的检索结果（qid → 按 rank 升序的 docid 列表）。
//
// 指标（口径详见本目录 README.md，公式出处 BEIR arXiv:2104.08663 附录 G）：
//   - NDCGAt10          分级增益 nDCG@10（gain=2^rel-1，折减 log2(rank+1)）
//   - RecallAt100       Recall@100
//   - CappedRecallAt100 R_cap@100（分母 min(k, |rel|)，BEIR 附录 G）
//   - MRRAt10           MRR@10
//
// 确定性：Evaluate 返回的 Report.Details 按 qid 字典序排列；所有均值按
// qid 排序累加。同输入必然得到深相等（deep equal）的 Report。
// ============================================================================

package evalseed

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Qrels 分级相关性标注：qid → (docid → rel)。rel>0 视为相关，
// rel=0 或缺失均按 0（不相关）处理。
type Qrels map[string]map[string]int

// Run 检索结果：qid → 按 rank 升序排列的 docid 列表（rank 1 为首位）。
type Run map[string][]string

// QueryDetail 单条查询的指标明细。
type QueryDetail struct {
	// Qid 查询标识。
	Qid string
	// Retrieved 该查询 run 中的文档数。
	Retrieved int
	// Relevant qrels 中 rel>0 的文档数。
	Relevant int
	// NDCGAt10 该查询的 nDCG@10。
	NDCGAt10 float64
	// RecallAt100 该查询的 Recall@100。
	RecallAt100 float64
	// CappedRecallAt100 该查询的 R_cap@100。
	CappedRecallAt100 float64
	// MRRAt10 该查询的 MRR@10。
	MRRAt10 float64
}

// Report 一次完整评测的报告：各指标的全集均值 + 每 qid 明细。
//
// 均值口径与 trec_eval 一致：对 qrels 中的全部 qid 取平均；某 qid 在 run
// 中缺失（或结果为空）时该查询各指标记 0 分；run 中出现 qrels 未知的
// qid 视为输入错误，由 Evaluate 直接报错。
type Report struct {
	// NumQueries 参与均值的查询数（= len(qrels)）。
	NumQueries int
	// NDCGAt10 全集 nDCG@10 均值。
	NDCGAt10 float64
	// RecallAt100 全集 Recall@100 均值。
	RecallAt100 float64
	// CappedRecallAt100 全集 R_cap@100 均值。
	CappedRecallAt100 float64
	// MRRAt10 全集 MRR@10 均值。
	MRRAt10 float64
	// Details 每条查询的明细，按 qid 字典序排列（确定性输出）。
	Details []QueryDetail
}

// Evaluate 计算 qrels × run 的全部指标，返回聚合 Report 与每 qid 明细。
//
// 错误处理（与各单指标函数一致）：
//   - qrels 为空 → 报错；
//   - run 为空 → 报错（空 run 通常意味着上游检索失败，而非真实的零召回）；
//   - run 中存在 qrels 未知的 qid → 报错（防止把未标注查询混入均值）。
func Evaluate(qrels Qrels, run Run) (Report, error) {
	if err := validate(qrels, run); err != nil {
		return Report{}, err
	}
	qids := sortedQids(qrels)
	report := Report{NumQueries: len(qids), Details: make([]QueryDetail, 0, len(qids))}
	for _, qid := range qids {
		rels := qrels[qid]
		ranking := run[qid]
		detail := QueryDetail{
			Qid:               qid,
			Retrieved:         len(ranking),
			Relevant:          countRelevant(rels),
			NDCGAt10:          ndcgQuery(rels, ranking, ndcgCutoff),
			RecallAt100:       recallQuery(rels, ranking, recallCutoff, false),
			CappedRecallAt100: recallQuery(rels, ranking, recallCutoff, true),
			MRRAt10:           mrrQuery(rels, ranking, mrrCutoff),
		}
		report.Details = append(report.Details, detail)
		report.NDCGAt10 += detail.NDCGAt10
		report.RecallAt100 += detail.RecallAt100
		report.CappedRecallAt100 += detail.CappedRecallAt100
		report.MRRAt10 += detail.MRRAt10
	}
	n := float64(len(qids))
	report.NDCGAt10 /= n
	report.RecallAt100 /= n
	report.CappedRecallAt100 /= n
	report.MRRAt10 /= n
	return report, nil
}

// WriteText 把报告写成确定性纯文本：先聚合指标，再按 qid 排序输出明细。
// 同一 Report 的输出恒定，可用作回归基线 diff。
func (r Report) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "num_queries %d\n", r.NumQueries)
	fmt.Fprintf(&b, "ndcg_at10 %.6f\n", r.NDCGAt10)
	fmt.Fprintf(&b, "recall_at100 %.6f\n", r.RecallAt100)
	fmt.Fprintf(&b, "capped_recall_at100 %.6f\n", r.CappedRecallAt100)
	fmt.Fprintf(&b, "mrr_at10 %.6f\n", r.MRRAt10)
	b.WriteString("qid retrieved relevant ndcg_at10 recall_at100 capped_recall_at100 mrr_at10\n")
	for _, d := range r.Details {
		fmt.Fprintf(&b, "%s %d %d %.6f %.6f %.6f %.6f\n",
			d.Qid, d.Retrieved, d.Relevant,
			d.NDCGAt10, d.RecallAt100, d.CappedRecallAt100, d.MRRAt10)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// sortedQids 返回 qrels 的 qid 字典序（确定性遍历的唯一入口）。
func sortedQids(qrels Qrels) []string {
	qids := make([]string, 0, len(qrels))
	for qid := range qrels {
		qids = append(qids, qid)
	}
	sort.Strings(qids)
	return qids
}
