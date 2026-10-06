# 搜索 Benchmark 实验记录

实验日期：2026-09-24。所有数据来自本机隔离服务；无生产流量。三个实验分别测量全文检索、表示通道和内容图，另以一个真实接纳物品做三档在线耗时冒烟测试。模型实验发生在 BTW 改为单 DataCenter 地址之前，新网关尚未做同集物理压测；**这些数值不能互相拼成当前 DataCenter 路径的一条生产请求预计延迟**。

## 数据、环境与可复现口径

- 使用 [BEIR 官方 SciFact 数据集说明](https://github.com/beir-cellar/beir/wiki/Datasets-available)提供的 `scifact.zip`；下载文件 MD5 为 `5f7d1de60b170fc8027bb7898e2efca1`。测试集 5,183 篇文档、300 条带相关性标注的查询；文档内容是数据集给出的标题与摘要，并非论文全文。
- 机器为 Apple M3、16 GiB 内存；Go 1.26.2／darwin-arm64。隔离的单机 Elasticsearch 9.5.4、Neo4j Community 2026.09.0；真实 BGE-M3 由 FlagEmbedding 1.4.2 在 CPU 上运行。历史三档实验由隔离测试宿主提供真实生成模型实例；BTW 当前运行配置不再选择该模型或保存其凭据。Neo4j 的临时 Java 26 运行时会给出非推荐版本警告。一次初始运行中 Elasticsearch 自动使用 8 GiB 堆，叠加本机磁盘超过 90% 水位，创建新索引超时；该失败未纳入延迟分位。最终全文与三档运行将堆限制为 512 MiB，隔离测试将磁盘水位改为 95%／97%／99%，因此不据此推断生产写入稳定性。
- 所有检索按物品 ID 评价；Recall@10、NDCG@10、MRR@10 使用 SciFact `qrels/test.tsv` 中正相关文档作为分母。没有用测试标注训练或调整模型。耗时均在单客户端顺序请求下测量，p95／p99 是该批样本的观测分位值，不是并发 SLA。

## 实验 A：完整语料的全文检索

BTW 的 Elasticsearch 适配器将全部 5,183 篇标题与摘要建索引，刷新后执行 300 条测试查询。数据范围过滤与物品折叠启用；查询耗时包含本机 HTTP 往返和结果解析。建索引时间只计文档写入及刷新，不含索引 schema 创建。

| 指标 | 实测 |
| --- | ---: |
| Recall@10 | **0.6065** |
| NDCG@10 | **0.4687** |
| MRR@10 | **0.4321** |
| 查询 p50 / p95 / p99 | **5.04 / 9.65 / 18.87 ms** |
| 顺序查询吞吐 | **171.75 次／秒** |
| 建索引 | **41.51 秒；约 124.86 篇／秒** |
| 数据集标题＋摘要字节 | **7.41 MiB** |
| 主分片索引存储 | **12.80 MiB** |

这是 fast 的**全文通道**结果，不包含 Neo4j、PostgreSQL 可见性核对和原文回读。它不是三档端到端质量分。原始输出：[全文实验 JSON](benchmark-scifact-lexical.json)。

## 实验 B：真实 BGE-M3 的三种表示

为了在本机完成 ColBERT 精确数值对照，固定选取字典序靠前的 8 条测试查询，纳入这 8 条查询全部 7 篇正例，再以固定 SHA256 顺序加入干扰文档，组成 **24 篇闭集**。所有文档仍使用数据集提供的完整标题与摘要。这种“正例必入”抽样会显著抬高绝对分数，**不能与 BEIR 官方全语料成绩比较，也不能用来证明 balanced 或 deep 比 fast 更好**。

| 通道／融合 | Recall@10 | NDCG@10 | MRR@10 | 数值参考检索 p50 |
| --- | ---: | ---: | ---: | ---: |
| Dense | 1.0000 | **0.9539** | 0.9375 | 0.048 ms |
| 学习型 Sparse | 1.0000 | 0.9111 | 0.8875 | 0.016 ms |
| 多向量 MaxSim | 1.0000 | 0.9288 | 0.9063 | **210.66 ms** |
| Dense＋Sparse RRF | 1.0000 | 0.9288 | 0.9063 | 未单独计时 |
| 三通道 RRF | 1.0000 | 0.9375 | 0.9167 | 未单独计时 |

balanced 的选择性查询编码 p50 为 **143.94 ms**；带多向量的查询编码 p50 为 **164.73 ms**。24 篇产生 **9,362 条子向量，平均约 390 条／篇**；仅 1024 维 float32 原始子向量约 **36.57 MiB**。建好本机 Exact 索引后 Go heap 快照约 **104.43 MiB**，还不含真实 Milvus 索引、副本和构建峰值。

多向量耗时来自对所有 24 篇执行全矩阵 Exact MaxSim；线上设计使用独立 ANN 召回和有限窗口核查，不能把 210.66 ms 当成 Milvus 的实测延迟。这个固定小样本里，两种融合的 NDCG 都低于 Dense 单路；目前没有证据支持“增加通道就一定提高相关性”。原始输出：[表示对照 JSON](benchmark-scifact-representations.json)。

## 实验 C：内容图的有界查询成本

用 SciFact 前 1,000 篇标题构造**合成图拓扑**：每个来源有根物品、两个语义对象和两个概念节点，共 5,000 个节点。标题派生概念及示意性原文位置只用于查询成本，不是人工或模型验证的语义关系。建立后每种跳数预热 5 次，再将 100 个种子的 1／2／3 跳查询交错执行。

| 跳数 | 平均候选 | p50 | p95 | p99 |
| --- | ---: | ---: | ---: | ---: |
| 1 跳 | 1 | 0.98 ms | 2.64 ms | 3.17 ms |
| 2 跳 | 2 | 0.91 ms | 2.64 ms | 3.11 ms |
| 3 跳 | 3 | 0.87 ms | 2.62 ms | 3.11 ms |

本数据拓扑非常规则且可缓存，跳数之间的微小差异不能解释为深图检索更快；它只证明当前受限 Cypher 在这组 5,000 节点上有界执行。原始输出：[内容图 JSON](benchmark-scifact-graph.json)。

## 实验 D：三档实际在线路径

在同一个已接纳的中文短文章上，使用真实 PostgreSQL、Elasticsearch、Neo4j、BGE-M3 和已配置的生成模型，向量索引采用 BTW 的 Exact 数值参考。预热和原文／引用回读测试完成后，直接调用进程内搜索能力顺序重复：fast 20 次、balanced 5 次、deep 3 次；**不包括 go-zero HTTP 解析和结果编码**。这一组在 Elasticsearch 512 MiB 堆的隔离环境运行，统计实际生成模型调用与检索表达数。

| 档位 | 样本数 | p50 | p95（小样本） | 生成调用／检索表达 | 完成状态 |
| --- | ---: | ---: | ---: | ---: | --- |
| fast | 20 | 13.32 ms | 25.76 ms | 0／20 | 20 次 `ok` |
| balanced | 5 | 1,362.16 ms | 3,092.81 ms | 5／15 | 5 次 `ok` |
| deep | 3 | 8,295.45 ms | 9,989.52 ms | 6／15 | 2 次 `ok`、1 次 `degraded` |

这组单物品耗时体现模型规划与证据判断成本，**不代表真实语料的召回效果、并发能力或尾延迟目标**。本次 deep 降级的直接原因有迹可查：证据判断模型返回了未闭合的结构化 JSON，解析报 `unexpected EOF`，结果保留已检索内容并标为降级，没有伪称完成取证。三个档位本次最多执行一轮；没有测到真实多轮耗时。[三档调用记录](benchmark-current-tiers.txt)。

在前一组不同临时资源条件下，deep 连续 10 次的 p50 为 **6,671.81 ms**、p95 为 **7,515.18 ms**，全部 `ok`；另一次先行探索的 3 次有 2 次 `degraded`，当时未保留逐次警告。这些运行不能合并成同一延迟分布，但都提示需要扩大样本并评估结构化模型输出的稳定性。[deep 重复记录](benchmark-current-deep.txt)。

## 复现与下一组实验

```sh
curl -fL -o /tmp/btw-scifact.zip https://public.ukp.informatik.tu-darmstadt.de/thakur/BEIR/datasets/scifact.zip
unzip -q /tmp/btw-scifact.zip -d /tmp/btw-bench-dataset

go run ./service/async/rpc/cmd/capacity -mode beir-lexical -beir-dir /tmp/btw-bench-dataset/scifact -elasticsearch-url "$ISOLATED_ES_URL" -queries 300
DATACENTER_WORKLOAD_TOKEN_FILE="$ISOLATED_DATACENTER_TOKEN_FILE" go run ./service/async/rpc/cmd/capacity -mode beir-representations -beir-dir /tmp/btw-bench-dataset/scifact -datacenter-url "$ISOLATED_DATACENTER_URL" -sample-docs 24 -queries 8
BTW_TEST_NEO4J_URI="$ISOLATED_NEO4J_URI" go run ./service/async/rpc/cmd/capacity -mode beir-graph -beir-dir /tmp/btw-bench-dataset/scifact -items 1000 -queries 100
```

三档实际路径的可选重复实验由 `BTW_BENCHMARK=1` 的真实模型集成测试执行，环境变量与隔离条件见 [验证记录](validation.md)。下一步需要在**同一完整语料和同一批标注查询**上接入物理 Milvus、真实抽取的内容关系与真实排序模型，分别固定候选集合看排序、固定排序看召回；再做并发负载、更新／删除和深档失败率实验。当前结果尚不能推出亿级容量或生产 SLA。
