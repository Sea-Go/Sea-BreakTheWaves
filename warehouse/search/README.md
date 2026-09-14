# 搜索相关性集市与冻结 Parquet

本目录是 WS07-D 的**搜索**生产者，独立于 `warehouse/models/marts/datasets/ds_recommendation_interaction.sql` 的推荐曝光样本。输入是版本化、逐条判定的 qrel 事件；`judgment_id` 与修订、query family、**查询**近重复簇、文档/块修订与内容 hash、query/content/judgment/available 时点全部保留。现已另有 RTW/DC/PG 的**测试判定子流**与 CH/dbt 同源验收，见 [SUBSTREAM_ACCEPTANCE.md](SUBSTREAM_ACCEPTANCE.md)；真人判断及完整判断范围未接入。`fixtures/`仍只提供可逐字重放的 synthetic 合同实例，不能视为线上相关性或模型效果。

```sh
RUNTIME=/private/tmp/sea-ws07c-ws08c-runtime-verified-20260914
OUTPUT=$(mktemp -d /private/tmp/sea-search-qrel.XXXXXX)
"$RUNTIME/.venv/bin/python" warehouse/search/acceptance.py \
  --runtime "$RUNTIME" --output "$OUTPUT/run" \
  --contracts "$PWD/contracts/jsonschema" --reader-root "$PWD"
```

脚本只连接新启动的 loopback ClickHouse、SeaweedFS S3 与 dbt。原始JSONL先按batch/连续source offset/规范payload SHA验证，逐批上传S3，再由ClickHouse `s3()`读入追加 `qrel_history`；dbt按冻结ingest cutoff构建ODS→DWD最新可见修订→pointwise qrel集市并跑质量测试。每个train/validation/test split由ClickHouse直接写独立Parquet到S3；生产者核物理Arrow列顺序、类型、nullable、row count、SHA/大小，全部分片完成后才写版本化manifest。`--reader-root`使WS09-A的独立`open_search_dataset`用PyArrow S3文件系统在**同一次运行**里读取仍在线的原对象并复核全部分片/行，不使用生产者内存里的行代替消费者。

目前两个固定generation：v1在offset13冻结13行（train5/validation4/test4）；v2含offset14撤回与15修订，在offset15冻结12行（4/4/4）。v2入仓后按v1 cutoff重建仍与原v1逐行一致，原v1清单/Parquet字节不变。负门禁拒绝源hash篡改、查询近重复簇跨split和同代S3对象覆盖。每份manifest明确`data_kind=synthetic`，reader只给`validated_synthetic_fixture`与`model_quality=null`；没有真实 judged qrel、训练收益、正式H04 lease/Outbox或生产部署。

独立读者schema/reader由`contracts/jsonschema/search-qrel*`与`training/src/sea_training/search_dataset.py`唯一维护。本目录只消费其列合同并发布清单实例，不复制第二份权威定义。完整连续交接报告见本轮`/private/tmp/sea-search-qrel-immutable-20260915/report.json`；该临时证据目录不是线上持久存储，正式共享环境仍须执行相同hash/时点/覆盖门禁。
