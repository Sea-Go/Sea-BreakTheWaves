# Wiki 质量评阅来源 ODS

RTW Knowledge 的 `producer=ridethewind.knowledge` 与 Wiki、搜索判定、发布事件共用 DataCenter 的连续 offset。本包使用自己的 `btw-warehouse-wiki-quality` consumer 与 PG 游标，完整保留每条 DataCenter EventSpec 的 JCS、输入哈希和技术收据；非质量事件显式标记 `technical_skip`，不会计作人审 Fact。未知 `knowledge.wiki.quality.*` 版本一律拒绝，不跳过。

当前仅冻结接纳基础设施。对于 `knowledge.wiki.quality.judged.v1`，必须同时配置 RTW 权威原始 Event JSON/原字节 SHA 回查与正式 `QualityVerifier`；先验证 RTW 原字节 SHA，再对照 RTW 与 DC 的 JCS EventSpec 和 DC input hash，最后由正式校验器验证事实来源、评阅修订和人工 0–3 rubric，才能标记 `quality_verified`。没有正式校验器时整个批次在 PG 和 DC ACK 之前拒绝。RTW 评阅 payload/API 仍由 RTW Knowledge owner 冻结，不能以测试夹具的 `syntheticVerifier` 或 DC payload 自报的身份当成真实人审。

ODS 游标、原始 Event 字节和所有技术位置在同一 PG 事务接纳，提交之后才 ACK DataCenter。ACK 丢失后的相同 batch 会核每条历史 EventID/hash/JCS/receipt/RTW 原字节后重试 ACK，改变过的收据或来源不能靠重放覆盖旧标签。`HTTPAuthority` 按 RTW API 候选的私有 Worker 路径读取原 Event 字节、原 SHA 与 JCS SHA，不能从重定向或未知响应推断来源；其 HTTP 测试仍是夹具。`bash internal/warehouse/wikiqualitysource/acceptance.sh` 使用独立 PG16 测试这条事务与拒绝路径；真正 RTW 管理 HTTP、RTW Outbox 原字节 worker 读口和真实 DC Eventing 的跨进程验收，需要在 RTW EventSpec/Authority 接口冻结后由生产者与消费者同父签署。默认不开启运行命令和生产质量数仓接纳。
