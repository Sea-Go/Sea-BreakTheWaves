# H10.b 搜索相关性训练集合同

权威定义是 `search-qrel-dataset-manifest.v1.schema.json` 与 `search-qrel.columns.v1.json`；`sea.search-qrel-dataset.v1` / `sea.search-qrel.v1` 独立于推荐的曝光和有效阅读合同。`training/src/sea_training/search_dataset.py` 是 WS09-A 的只读 Parquet 接纳模块。`training/search/search_training/dataset.py` 仍是标明 `experimental` 的合成 JSONL 配对夹具，不代替这份 H10.b 合同。

每行是一条**已经明确判断**的 pointwise qrel，逻辑键为 `(query_id,document_id,document_revision,chunk_id)`，`judgment_revision` 是该键的来源修订；一个冻结 generation 只允许该键的一条最新可见修订。`judgment_id` 在清单内唯一。`relevance_grade` 为 0–3，0 也必须有 `judged_mask=true` 和可追溯判断来源，未判断候选不能填 0 当负例。`near_duplicate_cluster_id` 专指**查询**近重复簇，连同 `query_family_id` 必须整体属于同一 split；并不把文档复用错误地定义成查询泄漏。`query_text_sha256` 与 `chunk_text_sha256` 是原字符串 UTF-8 字节的 SHA-256，不做隐式规范化。

可用时点严格为 `content_available_at <= query_time <= judged_at <= available_at <= source.ingest_cutoff <= created_at`；`query_time` 落在声明的 split `[start,end)`，`revoked_at <= ingest_cutoff` 的判断不得出现在可见分片。历史 manifest 不因后来撤回被原地修改，新修订由新的 generation/manifest 及 `parent_revision + parent_manifest_sha256` 链接。`data_kind=observed` 的行只允许 `judgment_source=human_judgment`；`synthetic` 只允许 `synthetic_fixture`，两者不能混称。Reader 核验行内来源 hash 的形状与文本 hash，外部判断记录及水位的真正权威覆盖需由 WS07 producer 和来源 owner 另交回执；一个自洽清单本身不能证明人工判断真实发生。

三个 split 必须依次声明 train、validation、test，每个非空，并以 `rows` 与 `shard_count` 固定完整分片范围。文件按 split/shard_index 升序列出，索引必须恰为 `0..N-1`；路径规范化且互不重复。WS09-A Reader 要求调用者提供 manifest 预期 SHA，先把**全部** Parquet 字节复制到临时快照并核文件大小/SHA、footer 行数、列顺序/Arrow 类型/nullability，然后逐行检查时点、撤回、文字 hash、逻辑键、查询身份、文档修订内容以及 family/近重复簇跨 split。所有分片验证完毕才返回可迭代的批次；缺片、空片或任意晚片错误不能让先前 train 行暴露。训练词表/数值变换只能从 train 拟合，此合同不把 validation/test 引入拟合；具体训练程序适配由 WS09-B 另做。

```python
from sea_training.search_dataset import open_search_dataset

with open_search_dataset("manifest.json", expected_manifest_sha256=known_hash) as snapshot:
    for batch in snapshot.iter_batches("train"):
        consume(batch)
```

本域单元测试以合成 qrel/Parquet 夹具验证接口及拒错，报告 `validated_synthetic_fixture`，`model_quality=null`。人工 qrel 来源、搜索模型训练收益和上线均未由这份合同本身验收。

WS07 独立生产端随后在真实 ClickHouse/dbt/SeaweedFS 上把**合成来源**导出为两代原始 Parquet，证据目录 `/private/tmp/sea-search-qrel-producer-reader-20260915`。本 reader 使用该目录 `report.json` 声明的预期 manifest SHA，直接读回未重写的 `dataset-v1/manifest.json`（SHA `21c49d2e5b3269fb8483fe9524933746ab6d1638e53b419eeb3c9cfa4bc2a19c`，13 行、train/validation/test=`5/4/4`）及 `dataset-v2/manifest.json`（SHA `fdc1eb43e9c6a07e84c34e88af289c741ac59acd4cbf22f7e8c0d11f0ea16d22`，12 行、`4/4/4`），均返回 `validated_synthetic_fixture`。`SEA_SEARCH_QREL_PRODUCER_EVIDENCE=<证据目录> training/.venv/bin/python -m pytest training/tests/test_search_dataset.py` 可复跑同一字节交接；完整训练包测试 81 例通过。此验收是 **L2 合成来源 + 真 CH/dbt/S3/Parquet reader**，不是人工 qrel 或线上模型质量。

更强的**同次父链**证据为 `/private/tmp/sea-search-qrel-live-reader-20260915/report.json`：WS07 在同一运行中用真实 ClickHouse/dbt 生成 Parquet，上传 SeaweedFS 后，WS09-A 使用 `pyarrow.fs.S3FileSystem` 从仍在线的隔离 S3 直接读取两代 manifest 和全部分片。该轮固定 v1 manifest SHA `462a7881b745f3617850a18741fd64dc4429fd01d008124ac06a833debd41324`、13 行 `5/4/4`；v2 SHA `988c56673cfa3e38438bf7cbe0dd7bd38b8221457f0233fe25d140caa6149ff9`、12 行 `4/4/4`。两代 Reader 报告均为 `validated_synthetic_fixture`、`model_quality=null`，旧 v1 重放不随 v2 更新，跨 split 查询近重复簇及来源 hash 反例被拒。此轮优于前述关闭 S3 后的本地副本回读，但仍只证明**合成 qrel**的真实引擎/存储/Reader 交接。

直接 `uv build --wheel` 已确认 wheel 包含 reader 和两份 schema。仓内既有 `training/pyproject.toml` 从上层 `../contracts/jsonschema` force-include；默认 `uv build` 先产 sdist 再从临时 sdist 构建 wheel 时，上层目录不在 sdist 中而失败。该打包布局缺口与本合同无关，需由 training 打包 owner 单独调整；本切片按约定写入范围未改 pyproject。
