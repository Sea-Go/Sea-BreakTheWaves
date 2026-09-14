# RTW Knowledge 混合事件到搜索 QREL 子流

日期：2026-09-15。状态：**显式合成夹具、本机只读源与 CH/dbt 验收通过；真实人工判定集市仍未启用**。

`internal/warehouse/searchsource.ExportSyntheticSubstream`从独立 consumer 已提交的 `ridethewind.knowledge` ODS 中，用 Repeatable Read 读取原 DC offset `1..N` 的完整前缀。每一 offset 都重新核 EventSpec JCS hash、DC receipt 身份/offset/时点；qrel 再核 RTW 原始事件字节 SHA、payload、前驱修订、grade 与状态。每个非 qrel offset 留在 `coverage.jsonl` 为 `technical_skip`。覆盖链逐 offset 含原 offset、event/receipt ID、DC input hash、RTW authority SHA（如适用）、独立 qrel ordinal 和 landing payload SHA；manifest 固定末根、coverage/landing 字节 SHA 与显式分组策略 SHA。下游 `source_sequence` 是 **qrel 子流 1..K 的独立序号**，其原 DC offset 在覆盖侧逐条对应，不能与原 producer offset 混淆。

RTW 事件不带 query family/近重复簇。`sea.search.qrel-grouping.v1` 策略必须逐 `search_id + query_text_sha256` 明确给两个组 ID、policy ID、`data_kind=synthetic` 和夹具来源。入口名称、CLI `--synthetic-fixture`、策略和 manifest `activation=none` 都把此版本限定为测试；原 RTW 合同即使写 `human_judgment`，**测试管理员产生的判定只映为 synthetic_fixture**。真实人工来源若接入，需要另写 observed 导出与来源证明，不能让这个入口悄悄改写它。撤回原事件 `grade=null` 到 landing 的 `status=retracted, relevance_grade=null, judged_mask=false`，不转成 0 分；`ds_search_qrels` 在最新可见修订后只取 active，并显式 `assumeNotNull(relevance_grade)`，所以最终 23 列 Arrow `relevance_grade` 仍是非空 `uint8`。

本次两条互补验收：

- **真实跨仓事件链、有限标注**：RTW 测试管理员 API→Outbox→真实 DataCenter `cmd/platform` 共享 producer→BTW 独立 ODS/ACK 已在同轮成功；只读 PG 连接读取其 8 个原 offset（6 skip、2 qrel 修订）。最终子流 coverage root `5307f0d4aecb01cb92d5ac990577ce6a9f1a67b2eb7a113e6d11440f701dcc3a`；[CH/dbt 报告](/private/tmp/sea-search-qrel-substream-real-r2-20260915/report.json) 记 ODS2、null-grade tombstone1、最新修订后可见0。因只有一个查询且已撤回，`dataset_manifest=not_produced_insufficient_qrels`；没有复制查询凑三份 split，也没有宣称真人标签。
- **独立多查询合成源、完整 23 列出口**：本包测试事件经真实 PG 事务得到 8 个原 offset（1 skip、7 qrel 修订），覆盖根 `bc524e5b519e699eaf9408faedfef32568449c913f45cff4214f0e21016e9db9`；[CH/dbt/S3 报告](/private/tmp/sea-search-qrel-substream-multi-r2-20260915/report.json) 记最终 train1、validation2、test2 共 5 条，23 列，SeaweedFS Parquet SHA 与独立 `open_search_dataset` 在服务仍在线时复核通过，manifest SHA `08da53fee22150132901187dc02a9bcc6357f4c02d00b1121d9c8354a482d6dc`，reader 状态 `validated_synthetic_fixture`、`model_quality=null`。正式 dataset manifest 的 recipe SHA 包含 coverage root、原 DC 终点和分组 SHA，并与外置 substream manifest/coverage 文件配套归档。

回归与负门禁：`bash internal/warehouse/searchsource/acceptance.sh`、`go test -mod=readonly -race -count=1 ./internal/warehouse/searchsource ./cmd/search-qrel-substream`、对应 `go vet` 通过；缺已提交原前缀、缺显式分组、篡改跳过覆盖、改 grade 后重算 landing 行/文件 hash 均拒绝。旧 [v1/v2 CH/dbt/S3 回归](/private/tmp/sea-search-qrel-baseline-substream-20260915/report.json) 仍为 13/12 行，历史 v1 在 v2 入仓后逐行相等，独立 reader 与原三项负门禁通过。上述都是测试数据；没有人工标注覆盖范围收据、Recall/MRR/nDCG 或模型收益结论。
