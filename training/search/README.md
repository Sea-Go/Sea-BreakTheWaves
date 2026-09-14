# 搜索训练：实验性 CPU 文本排序基线

本目录实现 WS09-B / OS09 的首个 T1 训练程序。它使用 Python 3.12 和根 `training/uv.lock`，无需 GPU；输出只属于候选工件，不会改变 RTW 内容指针、BTW 索引或 DC 活动模型。`training/src/sea_training/dataset.py` 目前仅接纳推荐互动 22 列，故这里的 `sea.search.experimental-qrel-pairs.v0` 是**局部合成夹具合同**，不是 H10.b 的权威 `SearchDatasetManifest`，也不接受 `observed` 冒名数据。

输入目录含固定 `manifest.json` 及 `train.jsonl`、`validation.jsonl`、`test.jsonl`，启动必须给清单 SHA-256。清单声明 `contract`、`experimental:true`、`data_kind:synthetic_fixture`、`dataset_id`、正整数 `revision`，以及顺序固定的三个文件各自的 `split/path/sha256/size_bytes/rows`。读取器先核对所有字节与行数，再核对每对的同 query 高/低相关判定、判断掩码、负例来源、唯一 pair、query/rewrite-family 切分；没有判断的候选无法成为负例。每行固定 `pair_id/query_id/query_family_id/query_text/judgment_set_id/label_source/negative_source/positive/negative/pair_mask/sample_weight`，候选固定 `chunk_key/text/grade/judgment_id/judged_mask`。本版只接纳 grade 2 正例和 grade 0 明确负例，正负 `judged_mask` 与 `pair_mask` 均为 true，来源须为 `synthetic_fixture` / `explicit_judged_qrel`。

运行示例（在 `training/search` 目录）：

```sh
uv run --project .. --locked python -m pytest -q tests
uv run --project .. --locked python -m search_training path/to/manifest.json \
  --expected-sha256 <64位清单hash> --checkpoint path/to/checkpoint.json \
  --candidate path/to/candidate.json --epochs 8 --seed 31
```

中断后用完全相同参数增加 `--resume`；可用 `--max-steps N` 在累计第 N 次更新后主动中断。断点保留清单/版本/训练配置、train-only IDF 词表、Adam 一二阶状态、随机生成器状态、当前 epoch 内的洗牌顺序、reader 游标、权重与逐步 loss；单文件原子替换并带 payload SHA-256。恢复前重新验全部清单/分片，失配拒绝。词表只从 train 产生，验证/test 的 OOV 以固定 IDF=1 处理。候选清单包含权重、特征顺序、IDF、配置和 checkpoint hash，`active:false`。这是词法 T1 基线，不是 Dense/Sparse/Multi-vector 训练权重，不能直接送 DC 模型管理加载。

优化目标为同 query 显式正负对的 `weight * log(1+exp(-(score_pos-score_neg)))`，四个固定文本特征为词频重叠、train IDF 重叠、连续词组匹配和文档长度项；Adam 学习率、L2、seed、epoch 都写入 checkpoint。当前夹具只核实数值下降和精确恢复，不声称真实检索收益。三路表示的空间、目标、编码器/词表、候选索引和需要冻结的参数独立列在 [`experiment-plan.v0.json`](experiment-plan.v0.json)；三项均标为 `NOT_IMPLEMENTED`。快搜/详搜的实际系统仍使用固定表示工件，不以本训练程序替代。

**给 WS09-A、WS07-D/E 的交接提案。** H10.b 应由数仓生产者固定 SQL/dbt revision、DIM/内容 generation、来源水位、训练/验证/test 时间窗、列 schema、Parquet ref/hash/行数、缺片/迟到/撤回资格，并提供可被同一 query/rewrite family 与近重复内容簇分组的键。H12 qrels 应固定人工判定集 revision、query 与稳定 `source_kind/content_id/revision_id/chunk_id`、等级准则、争议状态、判断覆盖及来源；行为标签应另带曝光资格、观察截止点、label revision 与采样概率，不能冒充人工 qrel。训练对需记录 `label_source/negative_source/sampling_version/teacher_ref/judgment_revision/observation_cutoff` 及正负 mask，在冻结的搜索行合同中验修订、查询时点、去重和切分。WS09-A 完成正式 schema/reader 与真实 CH/S3 回读后，再把本 trainer 的数值层接到权威快照；需要单独验证大分片流式/游标与恢复的语义。本地 JSONL 一次载入内存，仅适于微型夹具。

H11.a 后续由 WS09-D 确定工件包装、推理一致性和 DC 加载，H12 由 WS09-E 用获准搜索 qrels、固定基准、切片、消融和实际索引评估。当前没有这些输入或正式训练效果证据；本目录验收等级为 `LOCAL_VERIFIED`。
