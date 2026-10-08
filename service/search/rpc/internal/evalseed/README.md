# evalseed —— M0 检索评测指标计算器

`service/search/rpc/internal/evalseed` 提供 Sea 检索评测（M0）的分级指标计算：输入 trec qrels 风格的相关性标注与待评测 run，输出 nDCG@10 / Recall@100 / CappedRecall@100 / MRR@10 及每 qid 明细。默认数据源为仓库冻结种子集 [testdata/index/seeds](../../../../../testdata/index/seeds/)。

## API

```go
qrels, err := evalseed.ParseQrels(qrelsFile)   // "qid 0 docid rel"（四列，仅 rel>0 行）
run, err := evalseed.ParseRun(runFile)         // "qid docid rank"（三列）或 "qid docid"（两列，行序即 rank）

rep, err := evalseed.Evaluate(qrels, run)      // Report：四指标聚合 + Details（按 qid 排序）
v, err := evalseed.NDCGAt10(qrels, run)        // 单指标也可独立调用（均返回 (float64, error)）
// RecallAt100 / CappedRecallAt100 / MRRAt10 同上
rep.WriteText(w)                               // 确定性纯文本报告（可作回归基线 diff）
```

内存构造同样可行：`Qrels map[qid]map[docid]int`、`Run map[qid][]string`（后者按 rank 升序，且同一 qid 内 docid 不可重复）。

## 指标口径与公式出处

| 指标 | 公式 | 说明 |
| --- | --- | --- |
| `NDCGAt10` | DCG@k = Σ_{i=1..k} (2^rel_i − 1) / log2(i+1)；IDCG@k 为该查询全部分级降序取前 k 的 DCG；nDCG = DCG/IDCG | 分级增益 2^rel−1，位置折减 log2(rank+1)，rank 1 基；IDCG=0（无相关文档）时记 0 |
| `RecallAt100` | \|topk ∩ rel\| / \|rel\|，k=100 | rel = qrels 中该查询 rel>0 的文档集合 |
| `CappedRecallAt100` | R_cap@k = \|topk ∩ rel\| / min(k, \|rel\|)，k=100 | 公式依据 **BEIR（arXiv:2104.08663）附录 G**：当 \|rel\|>k 时分母封顶为 k，使多相关文档查询可达满分 |
| `MRRAt10` | 1 / rank(首个 rel>0 文档) | 前 10 位无相关文档记 0 |

聚合口径（与 trec_eval 一致）：**各指标对 qrels 中的全部 qid 求算术平均**；run 中缺失的查询（或结果列表为空）按 0 分计入，不剔除。

## trec_eval 兼容性说明

- **qrels**：直接兼容四列 trec qrels（`qid iteration docid rel`）。第二列 iteration 按惯例为 0，解析时要求为整数、不限定取值；rel<=0 行跳过；同一 (qid, docid) 重复标注报错。
- **run**：本包接受三列 `qid docid rank`（按 rank 升序稳定排序，rank 相同按文件行序）与两列 `qid docid`（行序即排名），是 trec_eval 六列 run 的最小子集。trec_eval 本体评测前需转换为六列：

  ```sh
  # 三列输入（qid docid rank）：rank 用第 3 列，score 随 rank 严格递减
  awk '{print $1, "Q0", $2, $3, 1-$3*0.001, "sea"}' run.txt > run.trec
  # 两列输入（qid docid）：按行序为每个 qid 生成从 1 递增的 rank 与递减 score
  awk '{c[$1]++; print $1, "Q0", $2, c[$1], 1-c[$1]*0.001, "sea"}' run.txt > run.trec
  ```

  常用对照命令：`trec_eval -m ndcg_cut.10 -m recall.100 -m recip_rank qrels.txt run.trec`（trec_eval 的 recip_rank 截断到前 10 需自行用 `mrr_cut.10` 口径核对；本包 MRR@10 与其 `recip_rank`（全排名）在首位命中在前 10 时一致）。
- **nDCG 细节差异提示**：trec_eval 的 `ndcg_cut` 默认使用同样的 2^rel−1 增益与 log2(i+1) 折减，结果应一致；若使用其他实现（如 pytrec_eval 的旧版本线性增益变体），请先以本包 README 公式为准对齐。

## 错误处理

| 场景 | 行为 |
| --- | --- |
| qrels 为空 / run 为空 | `errEmptyQrels` / `errEmptyRun`（可 `errors.Is` 判别）；空 run 视为上游检索失败而非真实零分 |
| run 中出现 qrels 未知的 qid | 报错并指明 qid（防止未标注查询混入均值） |
| run 同一 qid 内出现重复 docid | 报错并指明 qid/docid 与重复 rank（避免指标重复计分） |
| 查询无 rel>0 文档 | 各指标记 0，不产生 NaN |
| 解析格式错误（列数、非整数、rank<1、docid 重复等） | 报错并带行号 |

## 确定性

- `Report.Details` 按 qid 字典序排列；所有均值按 qid 排序累加（浮点求和顺序固定）；
- 同输入的两次 `Evaluate` 结果**深相等**（`reflect.DeepEqual` 成立，有测试强制）；
- `Report.WriteText` 输出恒定，可直接作为回归基线做 `diff`。

## 测试

```sh
# 手算小例（DCG/IDCG/nDCG 精确值、R_cap 分母行为、MRR 首命中、空输入/未知 qid）+ 冻结种子集冒烟
go test -race ./service/search/rpc/internal/evalseed/

# 种子集再生成校验（显式路径，testdata 不参与 ./... 通配）
go test -race ./testdata/index/seeds/scripts/
```
