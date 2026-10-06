// ============================================================================
// metrics.go 实现 evalseed 包的四个检索指标及其公共校验/聚合辅助。
//
// 公式（详见 README.md「口径与公式出处」）：
//   - DCG@k  = Σ_{i=1..k} (2^rel_i - 1) / log2(i+1)     （rel_i 取 rank i 文档的 qrels 分级）
//   - IDCG@k = 将该查询全部分级降序排列后取前 k 计算的 DCG@k
//   - nDCG@k = DCG@k / IDCG@k（IDCG@k = 0 时记 0，避免除零）
//   - Recall@k    = |topk ∩ rel| / |rel|
//   - R_cap@k     = |topk ∩ rel| / min(k, |rel|)         （BEIR arXiv:2104.08663 附录 G）
//   - MRR@k       = 1 / rank(首个 rel>0 文档)，前十无命中记 0
//
// 其中 rel 集合 = qrels 中该 qid 下 rel>0 的文档；|rel|=0 时各指标记 0。
// ============================================================================

package evalseed

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// 指标截断点常量。
const (
	ndcgCutoff   = 10
	recallCutoff = 100
	mrrCutoff    = 10
)

// errEmptyQrels / errEmptyRun 空输入错误（可 errors.Is 判别）。
var (
	// errEmptyQrels qrels 未包含任何查询。
	errEmptyQrels = errors.New("evalseed: qrels 为空，无法计算指标")
	// errEmptyRun run 未包含任何查询结果。
	errEmptyRun = errors.New("evalseed: run 为空，无法计算指标")
)

// validate 校验 qrels/run 的公共前置条件。
func validate(qrels Qrels, run Run) error {
	if len(qrels) == 0 {
		return errEmptyQrels
	}
	if len(run) == 0 {
		return errEmptyRun
	}
	// 按 qid 排序遍历：多个未知 qid 时错误信息也保持确定。
	runQids := make([]string, 0, len(run))
	for qid := range run {
		runQids = append(runQids, qid)
	}
	sort.Strings(runQids)
	for _, qid := range runQids {
		if _, ok := qrels[qid]; !ok {
			return fmt.Errorf("evalseed: run 中的 qid %q 未出现在 qrels（拒绝把未标注查询混入均值）", qid)
		}
	}
	return nil
}

// meanOverQrels 按 qid 字典序对各查询得分求均值（保证浮点累加顺序确定）。
func meanOverQrels(qrels Qrels, run Run, score func(qid string) float64) (float64, error) {
	if err := validate(qrels, run); err != nil {
		return 0, err
	}
	qids := sortedQids(qrels)
	sum := 0.0
	for _, qid := range qids {
		sum += score(qid)
	}
	return sum / float64(len(qids)), nil
}

// NDCGAt10 计算分级增益 nDCG@10 的全集均值。
func NDCGAt10(qrels Qrels, run Run) (float64, error) {
	return meanOverQrels(qrels, run, func(qid string) float64 {
		return ndcgQuery(qrels[qid], run[qid], ndcgCutoff)
	})
}

// RecallAt100 计算 Recall@100 的全集均值。
func RecallAt100(qrels Qrels, run Run) (float64, error) {
	return meanOverQrels(qrels, run, func(qid string) float64 {
		return recallQuery(qrels[qid], run[qid], recallCutoff, false)
	})
}

// CappedRecallAt100 计算 R_cap@100 的全集均值（分母 min(k, |rel|)）。
func CappedRecallAt100(qrels Qrels, run Run) (float64, error) {
	return meanOverQrels(qrels, run, func(qid string) float64 {
		return recallQuery(qrels[qid], run[qid], recallCutoff, true)
	})
}

// MRRAt10 计算 MRR@10 的全集均值（前十无相关命中记 0）。
func MRRAt10(qrels Qrels, run Run) (float64, error) {
	return meanOverQrels(qrels, run, func(qid string) float64 {
		return mrrQuery(qrels[qid], run[qid], mrrCutoff)
	})
}

// ----------------------------------------------------------------------------
// 单查询指标实现。
// ----------------------------------------------------------------------------

// relOf 取文档分级：缺失或 rel<0 一律按 0 处理。
func relOf(rels map[string]int, doc string) int {
	if r, ok := rels[doc]; ok && r > 0 {
		return r
	}
	return 0
}

// countRelevant 统计 rel>0 的文档数（即 |rel|）。
func countRelevant(rels map[string]int) int {
	n := 0
	for _, r := range rels {
		if r > 0 {
			n++
		}
	}
	return n
}

// ndcgQuery 计算单查询 nDCG@k。
func ndcgQuery(rels map[string]int, ranking []string, k int) float64 {
	dcg := 0.0
	top := ranking
	if len(top) > k {
		top = top[:k]
	}
	for i, doc := range top {
		gain := math.Pow(2, float64(relOf(rels, doc))) - 1
		if gain > 0 {
			dcg += gain / math.Log2(float64(i+2)) // rank=i+1（1 基）→ 折减 log2(rank+1)
		}
	}
	idcg := idcgQuery(rels, k)
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// idcgQuery 计算单查询理想 DCG@k：全部分级降序取前 k。
func idcgQuery(rels map[string]int, k int) float64 {
	grades := make([]int, 0, len(rels))
	for _, r := range rels {
		if r > 0 {
			grades = append(grades, r)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(grades)))
	if len(grades) > k {
		grades = grades[:k]
	}
	idcg := 0.0
	for i, r := range grades {
		idcg += (math.Pow(2, float64(r)) - 1) / math.Log2(float64(i+2))
	}
	return idcg
}

// recallQuery 计算单查询 Recall@k 或 R_cap@k（capped=true 时分母取 min(k,|rel|)）。
func recallQuery(rels map[string]int, ranking []string, k int, capped bool) float64 {
	relSet := make(map[string]struct{}, len(rels))
	for doc, r := range rels {
		if r > 0 {
			relSet[doc] = struct{}{}
		}
	}
	if len(relSet) == 0 {
		return 0
	}
	top := ranking
	if len(top) > k {
		top = top[:k]
	}
	hits := 0
	for _, doc := range top {
		if _, ok := relSet[doc]; ok {
			hits++
		}
	}
	denom := float64(len(relSet))
	if capped && len(relSet) > k {
		denom = float64(k)
	}
	return float64(hits) / denom
}

// mrrQuery 计算单查询 MRR@k：前 k 位中首个 rel>0 文档的倒数排名。
func mrrQuery(rels map[string]int, ranking []string, k int) float64 {
	top := ranking
	if len(top) > k {
		top = top[:k]
	}
	for i, doc := range top {
		if relOf(rels, doc) > 0 {
			return 1 / float64(i+1)
		}
	}
	return 0
}
