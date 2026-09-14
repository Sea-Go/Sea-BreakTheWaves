# WS08-C 特征基线与尾部局部验收

工作区域：[W0:ROOT] 本独立 BreakTheWaves 工作树；[W1:WRITE] 新增 `internal/usermodel/features*.go`、`internal/usermodel/testdata/features_baseline.sql`、本说明和 `migrations/usermodel/003_features.sql`；[R1:READ_ONLY] 原有 facts/ontology/graph、数仓与其他仓库；[D1:DEPENDENCY] 锁定的 Go 模块；[G1:GENERATED] 无；[X1:EXTERNAL] 脚本创建的隔离 PostgreSQL 16 实例；[N1:OUT_OF_SCOPE] 训练、用户塔、推荐策略、其他工作树；[T1:TEMP] 随机端口的临时 PG 数据目录，脚本退出即删除。主职责区是 [C3:DOMAIN] 的资格与合并规则，跨 [C4:PERSISTENCE] 的不可变 generation、活动头、快照版本和 [C8:VERIFY] 的固定 SQL/PG/race 验证。复用原有 `Store` 和注入的 `telemetry.Bundle` stage，没有新建 Logger、Tracer 或 Runner。

## 已实现的局部合同

- `FeatureSpec` 固定版本、规范化 hash、特征顺序、fact 的 `count/latest` 资格、时间窗口、默认值、missing mask 和词表 OOV；`ontology_rule` 只读通过既有 `ReadyOntologyProjection` 版本门禁的派生整数。当前只支持这几种确定性特征，不代表所有目标特征或用户塔模型已实现。
- `FeatureBaseline` 的 `revision/generation/spec_hash/as_of/available_at` 不可变，按 `(producer,source_partition)` 声明**已完整覆盖**的连续水位。每个已覆盖且在基线时点仍活跃的接纳事实，都必须交 `producer/event_id/normalized_hash/accepted_version/source_sequence` 及固定事件内容；单独的聚合值不足以对基线内修订做撤回。接纳会对照源事实与特征值，漏贡献返回 `ErrPending`，同版本异内容、回退水位或时间返回冲突。
- 合并从冻结基线和当前时点的接纳历史重选事实。水位后的活跃事实只合成一次；基线内事实被撤回时删除其贡献，乱序待前驱的撤回在真正接纳之前不进入特征。`Outbox` 与事实同事务，按接纳版本重放修订；不可用或未来事实不进入本次输入。无基线时显式输出 `baseline_state=absent`，保留全部合格尾部和 missing mask，不假装仓内已接纳。
- PG 将不可变基线、活动头、当前特征快照和不可变 `feature_snapshot_id` 版本分开。基线推进与组合快照写入同一事务；提交前锁主体事实版本并比较基线头，过期任务不能覆盖新状态。`RefreshFeatureSnapshot` 供撤回或重启恢复；`ReadyFeatureSnapshot` 拒绝事实/基线/Ontology 版本或窗口到期的旧活动快照。`FeatureSnapshotByID` 供未来请求记录和训练样本回读**实际使用**的历史输入；此切片没有自行写推荐请求。

## 已执行验证与边界

`bash internal/usermodel/test-postgres.sh` 已在随机端口的真实隔离 PostgreSQL 16 上通过 `go test -race -count=1 -v ./internal/usermodel ./migrations/usermodel` 与 `go vet`。新增用例覆盖固定 SQL fixture 与基线贡献/值对照、连续两代基线推进后不双计、旧 `feature_snapshot_id` 重启回读、迟到尾部、基线内撤回、乱序待前驱撤回、未来事实/撤回、无基线降级、缺贡献拒收、事实版本 CAS、默认值/OOV/窗口以及 Ontology 头变化后旧快照失效。

状态为 **WS08-C 组件 LOCAL_VERIFIED**。`testdata/features_baseline.sql` 是本包固定对照，不是 WS07-C 已发布的真实 DWS generation。WS07-C/H10.c 交接前提是同版 FeatureSpec、不可变 generation/SQL 运行清单、逐来源连续水位、每个被覆盖活跃事实的事件键/不可变 hash/接纳修订及可逆贡献、聚合值与可用时点；缺一不可接纳。真实 DWS、用户塔 D、推荐请求 F 和训练样本消费尚未联调，H08.b/H10.c/H12 整体未验收。

`Outbox.created_at` 是同事务记录时间而非 PostgreSQL commit timestamp，不能仅凭它对跨越未提交事务的任意旧时刻做严格 point-in-time 重建。请求发生时须固定并保留本切片生成的 `feature_snapshot_id`；历史样本应优先读该不可变快照。来源没有可验证 `source_sequence` 的事实始终留在尾部，不能由猜测的全局事件时间吸收到基线。单主体回放上限为 100000 条接纳事件；超限返回 `ErrPending`，后续需由真实数仓交接和有界 Outbox 游标替代全历史回放。

## 2026-09-14：与独立数仓 DWS 的局部联验

`warehouse/feature_baselines` 已用隔离 PostgreSQL、ClickHouse/dbt 与 SeaweedFS S3 执行两代合成事实的实际存储交接，并调用本包 `AcceptFeatureBaseline` 完成消费者接纳。来源序号 1、3 的断档只覆盖 1；补入 2 后覆盖 3；已接纳但未来生效的 4 留在水位外。S3 来源、DWS 值、基线、dbt 运行工件和清单均有内容 SHA-256；老代回退及变更清单拒收。新增 `NextChangeAt` 的未来事实失效点，防止已接纳但未来生效的事实在旧快照中永久不可见。跨存储 Go race 测试退出码 0；`USERMODEL_KEEP_EVIDENCE=1 bash internal/usermodel/test-postgres.sh` 的本包与 migration 真实 PG/race/vet 验证也退出码 0。详见 `warehouse/feature_baselines/验收结果.md`。

状态升级仅限 **WS07-C→WS08-C 的合成事实 L2 跨存储子链**；原先的 `LOCAL_VERIFIED` 记录保留其当时范围。正式 H09 来源、DC 分布式执行权、FeatureSpec 产品批准、在线请求/训练消费者及严格历史 commit 时点仍未验收，H10.c 整体保持 `PARTIAL`。
