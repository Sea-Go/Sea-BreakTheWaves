# WS07-C → WS08-C：用户语义事实特征基线交接

此目录独立于推荐曝光标签 DWS：输入是 BTW `usermodel` 已领域接纳的语义事实，而不是仓内通用 `recommendation` 观察事件。提供端使用 PG `Current` 的同一 `REPEATABLE READ` 快照固定用户状态版本与逐 `(producer, source_partition)` 连续前缀；前缀有缺口时只覆盖缺口之前，未覆盖的活跃事实由 WS08-C 近线尾部计算。所有已覆盖活跃事实都进入可逆 `FeatureBaseline.Contributions`，即使本版 `FeatureSpec` 不选中该事实。

数据流：

```text
usermodel PG 已接纳事实/Outbox/连续水位
  → 不可变来源 JSONL + SHA-256 → SeaweedFS S3
  → ClickHouse s3() 原文入仓 → dbt dws_user_feature_values
  → FeatureBaseline + dbt invocation/输入/结果 hash 清单 → S3
  → 下载并复核 → usermodel.AcceptFeatureBaseline → PG 不可变基线/活动头/特征快照
```

`FeatureSpec` 的规范化与 hash 由 `usermodel.PrepareFeatureSpec` 一处决定。此 DWS 切片仅支持 `Source=fact` 的 `count/latest`、窗口、默认值和 OOV；`ontology_rule` 保持 WS08-B owner 的门禁，不会被仓库脚本假造。ClickHouse 输出数值与 missing/OOV 经 `AcceptFeatureBaseline` 对照 PG 原始贡献重新计算；缺贡献、错误 hash、退水位和旧代覆盖都拒收。S3 基线与清单使用内容 hash 对象名并读回核验。本机对象写入检查并非分布式 CAS，真正发布还需要 DC lease/fence 与领域 READY/Outbox。

已接纳但业务或观察时刻尚未到达的活跃事实会截断本次覆盖前缀；其后必须在快照到期时重新构建或从尾部纳入。WS08-C 的 `NextChangeAt` 同时考虑已接纳未来事实的首次可用时刻，以免 `ReadyFeatureSnapshot` 将旧值持续标作当前。这里没有把 `Outbox.created_at` 当提交时间或完整来源游标。

macOS ARM64 的隔离验收使用锁定版本 ClickHouse/SeaweedFS/dbt、任务独占 PG schema 与随机端口：

```sh
feature_runtime=$(mktemp -d -t sea-feature-handoff)
python3 warehouse/scripts/bootstrap.py --runtime "$feature_runtime"
python3 warehouse/feature_baselines/acceptance.py --runtime "$feature_runtime"
```

验收脚本保留 `feature_handoff_*` 下的 PG/CH/S3 数据目录和 Go 日志，停止进程后供复核。需要逐项核对：有缺口的来源只交付连续前缀；未覆盖活跃事实进入尾部且不双计；缺口补齐后新 generation 推进；两代特征值、贡献数、不可变快照及重复接纳；修改清单/hash 被拒。SQL 作业的 `manifest.json` 与 `run_results.json` 的 invocation/hash 在交接清单中固定。

验收等级应区分：隔离真实 PG/ClickHouse/SeaweedFS 的**合成事件 L2 跨存储联验**，不等于 H09 真实来源、DC 作业、生产对象存储或完整 WS07-C/H10.c 的 L3。输入仍是测试创建的用户事件，FeatureSpec 尚未获产品规则 owner 的正式批准；没有用户塔、推荐请求的实际特征引用或训练样本消费。`Outbox.created_at` 是事务内记录时间，不能当 PostgreSQL commit timestamp；需要严格历史回放的请求必须记录不可变 `feature_snapshot_id`。生产多来源高吞吐、乱序回填的全局可用时点、分布式 fencing 与多租户容量尚待独立验收。
