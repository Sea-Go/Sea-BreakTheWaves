# WS08-A 用户事实账本与运行投影

本包是 BreakTheWaves 用户建模域的**确定性事务边界**。云端入口仍应由 tRPC-Agent-Go Runner/GraphAgent 承载，通过带主体上下文的 typed FunctionTool 调用本包；本包不实现另一套 Agent/任务调度器。现阶段未接入正式 Runner 入口。

## 已落实的契约

- `SubjectRef` 必须含 `authority_id、tenant_id、subject_id`，每张主体表和查询都使用完整三元组。`producer` 由上游接入适配器绑定，不能让模型或客户端自选。`EventKey = (producer,event_id)` 在主体内唯一；对规范化类型载荷计算 SHA-256。同键同 hash 回最初收据，同键异 hash 拒绝，来源位置冲突也拒绝。
- `Event` 必须带来源证据引用及 hash、发生/观察时间、来源分区与可选正序列。`assert/correct/retract` 是明确的状态动作；修正/撤回引用同主体前驱事件。未到前驱保留 `pending_dependency`，前驱到达后自动提升，可用 `ReconcilePending` 有界恢复；会闭合依赖环的迟到前驱直接拒收，不产生假接受版本。旧事件的 `event_body` 不更新；当前有效事实放在可重建 `usermodel_active_facts`。
- `Append` 的同键重投永远返回**初始**收据；一个起初 pending 的事件即使后来被前驱提升，该重投仍是 pending。事件批次技术ACK前须用 `CurrentReceipt(SubjectRef, EventKey)` 读取**当前**状态，核对本次不可变输入的 `normalized_hash`；该方法仅在同一PG快照看到`accepted_version>0`及对应已提交Outbox时返回`accepted`，缺失/跨主体不授予ACK。生产事件接入还须以RTW签发主体和生产者合同绑定输入，不能让payload自报。
- `ParkUnmapped` 只保存有来源范围的待映射信封，不生成 UserFact、用户状态版本或训练样本。`BindUnmapped` 要求 RTW 已验证的完整主体；同一外部主体别名不能跨事件绑定到两个主体。最初待映射收据重投保持不变。
- 阅读、真实展示及产品行为是不同的 `SemanticKind`。阅读不能自带 `impression_id` 冒充展示；同主体同时有效的展示 ID 唯一。`LinkImpression` 仅给真实阅读/产品行为建立关联，要求同主体、同内容、非空且相同的请求 ID、展示实际发生时间不晚于行为，以及已接纳且仍有效的展示证据；乱序到达可以回补，但未来发生的展示不能解释过去行为。展示撤回会在同一事务中失效已有归因；未归因阅读可保留为事实，不能进入需要真实曝光的训练口径。
- 每个已接纳事实或归因变化在**同一 PG 事务**更新当前状态版本/投影并写 `usermodel_outbox`。`Current` 在 repeatable-read 快照下交付 `projection_version`、活跃事实、逐来源连续水位与待关联数；`HistoryAfter` 以 `(occurred_at,producer,event_id)` 游标遍历不可变历史及证据；`OutboxAfter` 按主体版本交付 B/C/WS07 的来源引用。`unattributed_reading_or_action` 是“尚无展示归因”的数量，含可能无需展示的直接访问，不能自动算成坏数据或曝光。

水位从位置 1 开始确认连续性。若真实来源起点并非 1，当前结果会保持缺口而**不会**声称完整；与 DC 交换来源起点/分区合同后，再增加受版本约束的起点登记。序列缺失事件可接纳，但不能由事件时间推导完整来源水位。跨来源重放选择当前可用最早时间的来源头，同来源先守住序列；特征团队必须固定截止点和来源水位，不能把当前投影当成历史窗口明细。

## 交接与验收边界

部署者显式运行 `migrations/usermodel/001_facts.sql`。构造 `NewStore(pool, processTelemetryBundle)` 不迁移数据库、不创建 logger/Tracer/Registry；正式入口需注入进程唯一的 Bundle，并由 Runner/Graph Tool 传递真实 Context。隔离包测试验证了 domain stage 的单行 JSON、Span 与指标入口，**没有证明**正式 Runner 框架原生 Span、Collector 下钻或实际 RTW→DC→BTW 投递。

WS08-B/C 消费 `Current` 与 `OutboxAfter`，按固定 `projection_version` 和逐来源水位重算图/特征。WS07-B 消费 `History` 及 outbox 的证据引用，将历史事实和缺口与相同源事件在仓内对账；本包不将 PG `current` 用作不可变历史训练输入。真实 H09.a/H04 适配器还需要绑定 RTW 签发主体、producer、EventSpec 与来源位置，不能直接反序列化不可信 payload 后调用 `Append`。

局部真 PG 验收：`internal/usermodel/test-postgres.sh`。脚本使用随机端口、临时 PG16 数据目录和随机 schema，运行包级 race 测试与 vet；测试结束停止数据库。详细结果见 [验收结果](验收结果.md)。
