# Wiki 质量完整来源证据聚合 v1

状态：**L1 typed 合同与 synthetic Golden**，固定 BTW 开发基点 `229638cf1d63a31882db9470d95cc73d47ce6337`。本子包仅写 `internal/evaluation/wiki_quality/sourceproof/`。RTW FactSet 源已在独立开发头`95ce8fc`固定静态v1 Event并合入其知识开发头`1510e1e`，但仍默认关闭；既有 `btw-warehouse-wiki-quality` 连续 ODS 对 `knowledge.wiki.fact-set.*` 明确 fail-closed，**没有真实目录Event进入当前消费者**、N条当前判断批次/管理员资格、实时UserCenter UID或生产启用证明。本包是独立BTW typed投影，没有把尚未接线的RTW原字节读口猜成正式Warehouse适配器，也不计算 D07 页面达标率。

## 唯一聚合输入和输出

`Freeze(ctx, Input{Catalog,Judgments,Prefix,Withdrawals}, PrefixAuthority)` 接一个**显式 FactSetRevisionID**及目标 `WikiRevisionID + SourceScopeRevision`。Catalog 固定完整获准 `SourceRevisionID+ContentSHA` 列表、按 RTW 已固定公式由 `SourceRevisionID\x00paragraph:N\x00quoteSHA` 得出的 FactID/required、已由 RTW 权威 Reader核原 payload JCS 字节/SHA，以及 Catalog 原 EventID/原发射字节 SHA/Event JCS SHA/真实 DC offset。每个 required FactID 必有独立 `JudgeRevisionID=HeadAtCutoffRevisionID`、同 Wiki 目标和 scope 的原判断 EventID/raw/JCS/offset；不能拿目录 Event 或一条判断替全部 Fact。Catalog/判断 Event 的原字节进入输入核 SHA/JCS，聚合 Receipt 只保留哈希及引用。

`Prefix` 的批次范围必须从 offset1 连续到 `through_offset`，每批携 JCS batch/hash 与 ODS receipt SHA；`selected` 只列目录及其判断 Event 的真实位置，逐一对应上述 EventID/JCS，不能独自冒充完整前缀。`cutoff_offset` 限定所有判断位置和目标/Source 撤回快照。真正的 `PrefixAuthority` 需要从**同一个** DC Producer、独立 Wiki ODS PG cursor 和已 ACK 前缀实际读回批次索引、提交/ACK watermark后返回同 JCS index SHA；nil、空洞、错 membership、未 ACK到through或证据SHA不一致均拒根。当前测试 verifier只是合成 seam，不能给正式系统资格。下一 Reader须在 cutoff 下重建 `(WikiRevisionID,FactID)` 每条当时当前 judge head，验证没有同截止内较新的漏入 Event；本包只能核输入所钉 head ID 与判断 ID相同，不能从活动 head猜历史版本。

`WithdrawalSnapshot` 对目标 Wiki 和完整 Catalog 来源表逐个保留 `available|withdrawn|unknown` 的**截止时点**状态；撤回/待定时聚合仍可留 source proof，但 `quality_state=not_evaluable`。`facts_complete_declared=true` 明确是 RTW 管理员 JWT+名单的目录**声明**，原 Source quote byte span/FactID/JCS和获准范围核对不能机械证明没有漏事实；`rtw_actor_id`保留原 JWT userId 字面，不冒称 UserCenter UID。任何成功 L1 fixture 输出也固定 `evidence_level=rtw_dc_source_proof_only,quality_state=not_evaluable,activation=none`，`DecodeReceipt(raw,sha)`拒重哈希自改 observed/未知 tenant 字段。

双来源、两个 required Fact/一个 optional Fact、Catalog offset5、两判断offset6/8、前缀1–10、cutoff8的合成根 SHA=`5e81c4a5bbacf398d1b3a91e930be36012fd6fdd821bc7a9259e7c15202723c0`。根SHA、目录 payload JCS SHA、各原 Event raw/JCS SHA 和 ODS batch/receipt SHA 分域；此数值仅锁合同字节。反例检验缺 required 标签、重复 Fact/Event、跨目标/scope、旧 head、错原 SHA/JCS、单选中事件假前缀/批次空洞、过早 cutoff、伪 watermark与撤回资格。

RTW 生产者的静态 FactSet Event/Worker 私有原字节 GET与现有AdminJWT名单声明已冻结在开发分支，**新目录事件正式派发仍默认关闭**。数仓 owner须先让**同一** `btw-warehouse-wiki-quality` consumer识别 FactSet家族，按整个共享 producer 连续位置核 RTW 原 Event raw/JCS/payloadSHA、PG 同事务落 Catalog/技术跳过并 ACK，之后才能开RTW派发。此后 RTW+DC 的实际 Authority Reader才能把目录 Event与 N 条判断 Event的独立 offset/原字节及截止 head投此包；现有评测 `wiki_quality.AuthorityReceipt` 的单Event SourceProvenance不足以代替这份聚合证据。即使这一链路真通过，D07人类事实真值仍需完整目录声明的审阅资格、逐 Fact 评阅一致性/质量样本和人工 Release边界另签。

本地仅跑 `GOMAXPROCS=2 go test -p 1 -mod=readonly -race -count=1 ./internal/evaluation/wiki_quality/sourceproof` 顶层0，日志SHA=`84798a2f361cb53033fbdf6b8a9ad469b560f3c0ad72929ecda5020103abc1d4`；`go vet`顶层0/空日志SHA=`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`，diff check退出0。未启PG/CH/Ollama/Next，也未接真RTW/DC Reader。
