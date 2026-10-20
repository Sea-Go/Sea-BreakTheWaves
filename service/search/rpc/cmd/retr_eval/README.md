# retr_eval —— M3 检索评测入口（dev 形态）

`service/search/rpc/cmd/retr_eval` 把 M3 检索内核跑成端到端评测：加载
冻结种子集（[testdata/index/seeds](../../../../../testdata/index/seeds/)，
冻结声明见该目录 README）→ 用与 cmd/indexer **相同口径**的假编码把 20 篇
编码并经工件量化落位（M2 工件格式）→ `retrieval.Load` 装载 → 对 50 查询
按 gold 生成确定性查询表示 → 三档（fast/balanced/deep）各跑一遍 →
`evalseed.Evaluate` 输出报告（nDCG@10 / Recall@100 / R_cap@100 / MRR@10
聚合 + 按 qid 前后对比）。

## 用法

```sh
GOCACHE=/tmp/gocache-m3 go run ./service/search/rpc/cmd/retr_eval [--seeds testdata/index/seeds]
```

## 样例输出（2026-10-06，integration/llm-wiki-20261006 @ eb2c9a9）

```
retr_eval dev: seeds=testdata/index/seeds docs=20 queries=50 encoder=fake-encoder.v1 manifest_id=e6c948f0031769afc4e0c8c3bce293d6
tier      ndcg_at10 recall_at100 capped_recall_at100 mrr_at10
fast      0.887326 1.000000 1.000000 1.000000
balanced  0.959294 1.000000 1.000000 1.000000
deep      0.959294 1.000000 1.000000 1.000000
qid       ndcg_at10(fast balanced deep)  mrr_at10(fast balanced deep)
q-00      0.972121 0.936040 0.936040        1.000000 1.000000 1.000000
...（50 行按 qid 对比）
```

解读（dev 口径，数字本身不是真实检索质量）：

- fast < balanced：multi 路加入后排序变好（探针视角与检索内核 README 的
  档位矩阵一致）；
- deep = balanced：种子集 20 篇 < TopN（50），deep 的双倍候选截断不生效，
  属预期（deep 放宽是模拟，见 retrieval README）；
- MRR@10 = 1.0：查询 dense 是 gold 均值，主文档几乎必然首位——这是
  "查询表示来自 gold"的口径产物，真实查询编码接入后需重跑。

## 流程

```
corpus/*.md ──internal/devseed.LoadCorpus──▶ Store（含结构树/源文本）
   │   loadCorpusDocs：source, revision=sha256[:16], structure_ref
   │   fakerepr.Encode（镜像 cmd/indexer/fake.go：dense 64 维 / 词频 hash / multi 8×64）
   │   QuantizeF32/EncodeImpact/QuantizeMulti ──▶ sink{manifestID/<内容寻址ref>, manifest.v1.json}
   │   retrieval.Load（解码三路）+ AttachSource（DeriveTree：markdown→结构树，RTW structure.Derive 的 dev 替身）
   ▼
Searcher ── buildRequest（gold→查询表示三件套，确定性；本包 queries.go）──▶ 三档 Search
   ──▶ evalseed.Run ──▶ evalseed.Evaluate ──▶ 报告
```

## dev 口径（近似声明）

| 环节 | dev 替身 | 真实化 |
| --- | --- | --- |
| 文档编码 | 假编码（doc_key/structure_ref/revision_id 种子，镜像 cmd/indexer） | DC representation（真实编码器接缝） |
| 查询表示 | gold 文档：dense 均值 / 种子文本词频 hash / 主文档 multi 前 8 行 | 真实查询编码器（同 encoder_id） |
| 结构树 | `deriveTree`（空行分块；标题/导读/分隔线/尾注规则与种子集构造口径一致） | RTW structure.Derive（冻结修订派生） |
| 命中定位 | 文档首段整段 | 按各路分数选最高分段 |
| deep 档 | 双倍路内候选 | 真实放宽召回 + rerank |

确定性：全链路无时间/随机/网络依赖；同输入两次评测 `evalseed.Report`
深相等（`TestRetrEvalSmoke` 强制）。

## 同步义务

- 语料装载与结构树派生（loadCorpus/buildSink/deriveTree）已上提
  [internal/devseed](../../internal/devseed/)、假编码上提
  [internal/fakerepr](../../internal/fakerepr/)（与 cmd/search_demo、
  internal/pipeline 共享；后者镜像 `service/async/rpc/cmd/indexer` 的
  fake.go——两侧假编码口径必须一致，否则评测与 M2 索引不可对照）；
- 工件量化/键约定经 `retrieval` 包的 `artifactmirror.go` 镜像
  artifact 域（详见该包 README 的镜像声明）。

## 验收

```sh
GOCACHE=/tmp/gocache-m3 go test -race -count=1 ./service/search/rpc/cmd/retr_eval/...
GOCACHE=/tmp/gocache-m3 go vet ./service/search/rpc/cmd/retr_eval/...
```
