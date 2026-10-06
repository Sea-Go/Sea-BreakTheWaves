// Command retr_eval 是 M3 检索内核的评测入口（dev 形态）：加载冻结种子集
// 语料（internal/devseed：假编码 + 工件量化 + 装载 + 补源）→ 对 50 查询
// 按 gold 生成确定性查询表示 → 三档（fast/balanced/deep）各跑一遍 →
// evalseed 评测输出报告。
//
// dev 口径声明（哪些是近似，详见本目录 README.md）：
//   - 查询表示来自 gold 文档（理想查询编码的替身）：dense = 相关文档去量
//     向量均值；sparse = 相关文档种子文本的词频 hash；multi = 主文档
//     （rel 最高、平局字典序最小）multi 矩阵的前 N 行；
//   - 命中定位为 dev 近似（文档首段整段）；
//   - deep 档 = 三路 + 双倍路内候选（模拟放宽）。
//
// 用法：
//
//	go run ./service/search/rpc/cmd/retr_eval [--seeds testdata/index/seeds]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/devseed"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evalseed"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "retr_eval: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	seeds := flag.String("seeds", "testdata/index/seeds", "种子集目录（含 corpus/、queries.jsonl、qrels.txt）")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_, err := runAll(ctx, *seeds, os.Stdout)
	return err
}

// runAll 跑完整评测并把三档报告打印到 w；返回 tier → Report（供测试断言）。
func runAll(ctx context.Context, seedsDir string, w io.Writer) (map[retrieval.Tier]evalseed.Report, error) {
	if seedsDir == "" {
		return nil, errors.New("--seeds is required（种子集目录，见 --help）")
	}
	corpus, err := devseed.LoadCorpus(seedsDir)
	if err != nil {
		return nil, err
	}
	queries, err := loadQueries(seedsDir)
	if err != nil {
		return nil, err
	}
	qrels, err := loadQrels(seedsDir)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(w, "retr_eval dev: seeds=%s docs=%d queries=%d encoder=%s manifest_id=%s\n",
		seedsDir, len(corpus.Docs), len(queries), corpus.EncoderID, corpus.ManifestID)

	searcher := retrieval.NewSearcher(corpus.Store)
	reports := map[retrieval.Tier]evalseed.Report{}
	for _, tier := range []retrieval.Tier{retrieval.TierFast, retrieval.TierBalanced, retrieval.TierDeep} {
		run := evalseed.Run{}
		for _, q := range queries {
			req, err := buildRequest(corpus.Store, qrels, q.Qid, tier)
			if err != nil {
				return nil, fmt.Errorf("retr_eval: 查询 %s: %w", q.Qid, err)
			}
			pack, err := searcher.Search(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("retr_eval: 查询 %s 检索（%s）: %w", q.Qid, tier, err)
			}
			keys := make([]string, 0, len(pack.Candidates))
			for _, c := range pack.Candidates {
				keys = append(keys, c.DocKey)
			}
			run[q.Qid] = keys
		}
		rep, err := evalseed.Evaluate(qrels, run)
		if err != nil {
			return nil, fmt.Errorf("retr_eval: 评测（%s）: %w", tier, err)
		}
		reports[tier] = rep
	}
	writeReports(w, reports)
	return reports, nil
}

// writeReports 打印三档聚合指标 + 按 qid 的前后对比。
func writeReports(w io.Writer, reports map[retrieval.Tier]evalseed.Report) {
	tiers := []retrieval.Tier{retrieval.TierFast, retrieval.TierBalanced, retrieval.TierDeep}
	fmt.Fprintf(w, "tier      ndcg_at10 recall_at100 capped_recall_at100 mrr_at10\n")
	for _, t := range tiers {
		r := reports[t]
		fmt.Fprintf(w, "%-9s %.6f %.6f %.6f %.6f\n", t, r.NDCGAt10, r.RecallAt100, r.CappedRecallAt100, r.MRRAt10)
	}
	// 按 qid 前后对比：nDCG@10 与 MRR@10 三档并排。
	fmt.Fprintf(w, "qid       ndcg_at10(fast balanced deep)  mrr_at10(fast balanced deep)\n")
	qids := make([]string, 0, len(reports[tiers[0]].Details))
	for _, d := range reports[tiers[0]].Details {
		qids = append(qids, d.Qid)
	}
	for _, qid := range qids {
		var ndcg, mrr [3]float64
		for i, t := range tiers {
			for _, d := range reports[t].Details {
				if d.Qid == qid {
					ndcg[i], mrr[i] = d.NDCGAt10, d.MRRAt10
					break
				}
			}
		}
		fmt.Fprintf(w, "%-9s %.6f %.6f %.6f        %.6f %.6f %.6f\n",
			qid, ndcg[0], ndcg[1], ndcg[2], mrr[0], mrr[1], mrr[2])
	}
}
