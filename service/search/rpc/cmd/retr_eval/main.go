// Command retr_eval 是 M3 检索内核的评测入口（dev 形态）：加载冻结种子集
// 语料 → 用与 cmd/indexer 相同口径的假编码把 20 篇编码并经工件量化落位 →
// retrieval.Load 装载成 Store → 对 50 查询按 gold 生成确定性查询表示 →
// 三档（fast/balanced/deep）各跑一遍 → evalseed 评测输出报告。
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
	"sort"
	"syscall"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evalseed"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// queryMultiTokens 是查询 multi 表示取主文档前 N 个 token 的 N（dev 口径）。
const queryMultiTokens = 8

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
	docs, err := loadCorpus(seedsDir)
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

	// 编码 + 量化落位（M2 工件格式）→ 装载 → 补齐结构树/源文本。
	sink, manifest, err := buildSink(docs)
	if err != nil {
		return nil, err
	}
	store, err := retrieval.Load(sink, manifest.Docs)
	if err != nil {
		return nil, fmt.Errorf("retr_eval: 装载工件: %w", err)
	}
	for _, d := range docs {
		tree, err := deriveTree(d.Source, d.RevisionID)
		if err != nil {
			return nil, err
		}
		if err := store.AttachSource(d.DocKey, tree, d.Source); err != nil {
			return nil, fmt.Errorf("retr_eval: 补齐文档 %s: %w", d.DocKey, err)
		}
	}

	fmt.Fprintf(w, "retr_eval dev: seeds=%s docs=%d queries=%d encoder=%s manifest_id=%s\n",
		seedsDir, len(docs), len(queries), fakeEncoderID, manifest.ManifestID)

	searcher := retrieval.NewSearcher(store)
	reports := map[retrieval.Tier]evalseed.Report{}
	for _, tier := range []retrieval.Tier{retrieval.TierFast, retrieval.TierBalanced, retrieval.TierDeep} {
		run := evalseed.Run{}
		for _, q := range queries {
			req, err := buildRequest(store, qrels, q.Qid, tier)
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

// buildRequest 按 gold 生成确定性查询表示（dev 口径：理想查询编码替身）。
//
//   - dense：全部相关文档（rel>0）去量 dense 的均值（按 doc_key 字典序
//     累加，浮点顺序固定）；
//   - sparse：相关文档种子文本拼接的词频 hash（与假编码同口径）；
//   - multi：主文档（rel 最高、平局字典序最小）multi 矩阵的前
//     queryMultiTokens 行。
func buildRequest(store *retrieval.Store, qrels evalseed.Qrels, qid string, tier retrieval.Tier) (retrieval.Request, error) {
	rels := qrels[qid]
	if len(rels) == 0 {
		return retrieval.Request{}, fmt.Errorf("qrels 缺少查询 %s 的 gold 标注", qid)
	}
	goldKeys := make([]string, 0, len(rels))
	for k := range rels {
		goldKeys = append(goldKeys, k)
	}
	sort.Strings(goldKeys)

	sn := store.Snapshot()
	var dense []float32
	primary := ""
	for _, k := range goldKeys {
		d, ok := sn.Doc(k)
		if !ok {
			return retrieval.Request{}, fmt.Errorf("gold 文档 %s 不在 Store 中", k)
		}
		if dense == nil {
			dense = append([]float32(nil), d.Dense...)
		} else {
			for i := range dense {
				dense[i] += d.Dense[i]
			}
		}
		if primary == "" || rels[k] > rels[primary] {
			primary = k
		}
	}
	for i := range dense {
		dense[i] /= float32(len(goldKeys))
	}

	// sparse：gold 种子文本的词频 hash（与 encodeDoc 同构；revision 段用
	// 确定性占位，真实查询编码属后续）。
	sparse := map[uint32]float32{}
	if toks := tokenize(goldSeedText(goldKeys)); len(toks) > 0 {
		counts := map[string]int{}
		for _, t := range toks {
			counts[t]++
		}
		maxCount := 0
		for _, c := range counts {
			if c > maxCount {
				maxCount = c
			}
		}
		keys := make([]string, 0, len(counts))
		for t := range counts {
			keys = append(keys, t)
		}
		sort.Strings(keys)
		for _, t := range keys {
			sparse[fnv32a(t)] = float32(retrieval.ImpactWeight(float32(counts[t]), float32(maxCount)))
		}
	}

	// multi：主文档 multi 矩阵前 N 行。
	pd, _ := sn.Doc(primary)
	multi := append([][]float32(nil), pd.Multi...)
	if len(multi) > queryMultiTokens {
		multi = multi[:queryMultiTokens]
	}
	return retrieval.Request{QueryID: qid, Tier: tier, Dense: dense, Sparse: sparse, Multi: multi}, nil
}

// goldSeedText 拼接 gold 文档的种子文本（与 encodeDoc 的种子同构：
// doc_key ‖ structure_ref ‖ revision_id；查询侧 revision 段用占位常量，
// 完整重现文档侧修订 ID 属真实编码器职责，dev 口径）。
func goldSeedText(goldKeys []string) string {
	s := ""
	for _, k := range goldKeys {
		s += k + "\x1fstructure/" + k + "\x1frev-seed\n"
	}
	return s
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
