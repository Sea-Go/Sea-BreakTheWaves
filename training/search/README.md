# 搜索训练：实验性 CPU 文本排序基线

本目录实现 WS09-B / OS09 的 CPU 词法 T1 候选训练。它使用 Python 3.12 和根 `training/uv.lock`，无需 GPU；输出只属于候选工件，不会改变 RTW 内容指针、BTW 索引或 DC 活动模型。原 `sea.search.experimental-qrel-pairs.v0` JSONL 路径仍是局部夹具；新增 `search_training.authoritative.open_authoritative_pairs` 则先委托独立 `sea_training.search_dataset.open_search_dataset` 按调用方预期 SHA-256 接纳 `sea.search-qrel-dataset.v1` 的完整 Parquet 清单与全部分片，再冻结本训练器自己的分级训练对。两个入口不互相冒充。

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

**H10.b Parquet 路径。** `open_authoritative_pairs`只使用 reader 已验证的固定 `train/validation/test` split；按每个 split 内同一 `query_id` 的显式已判断 qrel，将较高等级 2/3 与较低等级 0/1 稳定配对。原始 0、1、2、3 等级、judgment修订/来源、`judged_mask`、query family 与查询近重复簇原样留在 pair 内；等级1是**较低相关**，不会被改写为等级0的“不相关”。等级差决定 `sample_weight=gap/3`，没有已判断高低双方的 query 不产生训练对。pair policy 与全部 pair 的内容哈希连同 H10.b manifest SHA 进入 checkpoint，恢复前仍须重验全部 S3 分片。IDF/Adam 只在 train 拟合，validation/test 只用冻结权重评分。默认上限为每 split 20 万 pair；这仍是有界内存 T1 候选，不是大规模训练作业。

```sh
PYTHONPATH=training/search:training uv run --project training --locked --python 3.12 \
  python -m search_training.authoritative_cli <manifest.json> \
  --expected-sha256 <64位生产者清单SHA> --checkpoint <checkpoint.json> \
  --candidate <candidate.json> --epochs 3 --seed 31
```

隔离本机 SeaweedFS 时传 `--s3-endpoint http://127.0.0.1:<port>`，并把 manifest 参数写成 `bucket/search-qrel/<generation>/manifest.json`。`training/search/acceptance.py` 复用只读的 `warehouse/search/acceptance.py` 生产函数，在**同一次** ClickHouse/dbt→SeaweedFS 写入仍在线时用独立 PyArrow S3 reader 接纳 v1/v2，再做 train-only 训练、精确中断恢复和候选 hash 对照；来源是显式 `synthetic_fixture`，report 将训练 loss 标成优化诊断、`model_quality=null`、`business_improvement=null`。正式人工 qrel、真实检索效果、DC 装载和 Dense/Sparse/Multi-vector 权重训练仍是后续门禁。

H12 qrels 将来还需固定人工判定集 revision、query 与稳定 `source_kind/content_id/revision_id/chunk_id`、等级准则、争议状态和判断覆盖；行为标签必须另带曝光资格、观察截止点和 label revision，不能冒充人工 qrel。H10.b 当前 Reader 已覆盖正式 23 列、物理 Parquet/hash/时点与分组资格，但超出20万pair的作业、受认证的共享S3与多机 checkpoint 恢复仍待独立实施。H11.a 后续由 WS09-D 确定工件包装、推理一致性和 DC 加载，H12 由 WS09-E 用获准搜索 qrels、固定基准、切片、消融和实际索引评估。本次只有合成qrel在真实CH/dbt/S3上的输入与候选训练链，不能宣称业务收益；WS09-B/H11/H12整体仍`PARTIAL`。
