# RTW 人工搜索判定的独立 ODS 消费接缝

RTW Knowledge `producer=ridethewind.knowledge` 与内容发布等事件共用 DataCenter 的连续 offset。`btw-warehouse-search-qrel` 使用自己的 consumer/cursor；每条 batch 和 DC receipt 先核技术位置/哈希，qrel 两种修订事件再用 RTW Worker 私有 `GET /internal/v1/knowledge/search-judgments/events/:event_id` 回查原始 Outbox Event 字节及 SHA，并对照两侧 JCS EventSpec、query/chunk 文本哈希、发布身份、grade 0–3 或撤回的 null、前驱修订与时点。非 qrel 事件以 `technical_skip` 保留 offset/receipt，绝不当作人工判断。PG 将整个 batch 与独立游标同事务接纳，只在提交后 ACK；丢 ACK 后按 event ID 与 hash 重读，不重复生成 qrel。

运行本包的隔离 PG16/HTTP 夹具测试：`bash internal/warehouse/searchsource/acceptance.sh`。单独运行时，它用真实 PostgreSQL 事务、合成 EventSpec/私有 HTTP 回执验证首判、非 qrel 跳过、撤回和丢 ACK 后重放，不启动 RTW/DC。设置 `SEA_RTW_REAL_QREL_FIXTURE` 后，RTW 父测试同轮提供实际管理员 HTTP/PG 判断和撤回、Outbox、真 DC `cmd/platform` 的共享 producer 与 RTW 私有 HTTP；本包的 `TestRTWRealSearchJudgmentSource` 经正式 Consumer 向独立 PG ODS 接纳、ACK。2026-09-15 两个 RTW 场景各验 8 条事件（2 条判断修订、6 条技术跳过）与 ACK offset 8，见 RTW `service/knowledge/docs/acceptance/real-search-judgment-source.md`。RTW 判定功能默认关闭；这两条管理操作由测试夹具产生，不是实际人工标注。

当前已实现**显式合成夹具**的 `ExportSyntheticSubstream`：从已验完整 producer 前缀派生独立 qrel ordinal，同时逐原 DC offset 保留非 qrel 跳过、receipt 与内容哈希根，要求版本化 query family/近重复簇映射。详细验收见 [`warehouse/search/SUBSTREAM_ACCEPTANCE.md`](../../../warehouse/search/SUBSTREAM_ACCEPTANCE.md)。此入口不提供 observed 人工标签导出；不能把稀疏原 DC offset 改写成连续假来源，也不能凭自洽 manifest 宣称候选池已全判。正式 Recall/MRR/nDCG 仍不可评。
