# 收藏状态特征候选：真实 DWD 到 FeatureSnapshot/ServingBundle

这是一条 **default-off 候选**，不是已获批的线上 `FeatureSpec`。唯一规则为 `favorite_active_count`：`Source=fact`、`Kind=product_action`、`Predicate=favorite`、`Mode=count`、无时间窗口、默认值 `0`。其含义是同一完整 `SubjectRef` 当前有效的收藏事实数，不是曝光、点击、成熟负例、概率偏好或推荐收益。用户模型产品规则 owner 必须另行批准版本/hash；推荐域必须批准完整 PairRef 和 item 侧同空间索引，才能谈激活。固定规则编码仅用于隔离验收。

## 接缝与字段所有者

1. RTW Favorite 事务/Outbox 是 assert/retract 与 `target_revision` 的领域权威；DC 是全局 producer offset、EventSpec 哈希与投递收据权威。
2. `warehouse/favorite` 用自己的 `btw-warehouse-favorite` consumer 形成 PG ODS 与 CH/dbt `dwd_favorite_transition`，S3 固定 DWD JSONL 以 SHA-256 命名。DWD 保留 `producer,source_offset,event_id,SubjectRef,favorite_id,target_revision,operation,event_time,available_at,dc_received_at,source_event_hash`。DWD 两版可以按 1/2 连续前缀回放；撤回必须匹配先前同主体、同目标及原修订。
3. `BuildFavoriteCandidate` 从内容哈希 S3 DWD 核对指定连续前缀，并逐条查 `usermodel.CurrentReceipt` 与历史事实，将 DWD 活跃集合与 `usermodel.Current` 的 PG 事实、水位、来源哈希、目标修订、原始时点比对。只有完全一致才调用现有 H10 `Runner.Build`；产出按 SHA-256 写入 S3 的 `FavoriteCandidateManifest`，绑定 DWD 完整 hash、前缀 hash、FeatureSpec hash、generation/revision、来源水位与原 H10 基线清单。它**不**调用 `Accept`、`ActivateServingBundle` 或模型服务。
4. 隔离验收明确调用现有 `Runner.Accept`，由 PG `AcceptFeatureBaseline` 再核不可变贡献，得到两代 `FeatureSnapshot`；固定规则 `UserEncoder` 仅构建未激活 `ServingBundle`。r1 assert 为 `1`，r2 retract 为 `0`，旧快照/Bundle 可按 ID 回读，重放不双计；旧代不能覆盖新头。

## 全局 offset 与主体水位的已证限制

当前 `internal/app/fact_worker.go` 把 DC 全局 offset 写到 `Event.SourceSequence`，而 `usermodel` 的水位按完整 SubjectRef + producer + source_partition 逐号推进。交错 `[u1:1,u2:2,u1:3]` 时，u1 只有 contiguous=1/max=3，u2 为 contiguous=0/max=2；`Runner.Build` 对 u2 拒绝“no complete accepted source prefix”，对 u1 只能覆盖 1，3 留近线尾部。`TestGlobalOffsetIsNotSubjectSequence` 用真实隔离 PG/CH/S3 验证此结果；本候选适配器主动拒绝混合主体 DWD，绝不把全局 offset 伪称主体连续序号。

最小修复合同须由 H09 来源和 WS08-C 单写入者共同设计：**DC producer 全局覆盖水位** `W` 与 **每主体稀疏事件位置**分开。来源索引须对 `[1,W]` 全局连续、按 `(producer,global_offset)` 固定 SubjectRef/event_id/hash；对某主体 S 提供“该全局前缀里属于 S 的全部事件键与终态”可核证明。用户模型核对这些稀疏键均有已接纳回执或明确不可评状态后，才能把 `(S,W,来源收据 hash)` 声明为完整特征覆盖。不能只看该主体收到的最大 offset，也不能跳过未知缺口。替代方案是 RTW 在同事务为每主体分配独立连续序列、DC 继续另存全局投递 offset；两种方案选定并迁移前，混合主体线上基线与 Bundle 均保持 default-off。本分支没有改 FactWorker/Store 的既有来源语义。

## 验收与剩余工作

```sh
SEA_DC_PLATFORM_ROOT=/path/to/isolated/datacenter \
SEA_RTW_FAVORITE_ROOT=/path/to/isolated/rtw \
WAREHOUSE_FAVORITE_RUNTIME=/path/to/locked/warehouse-runtime \
warehouse/favorite_feature/acceptance.sh
```

脚本创建隔离 PG16/DC/RTW/ClickHouse/dbt/SeaweedFS，先跑数仓独立 consumer，再用真 DWD 冻结来源驱动 BTW Graph 接纳与 H10 两代基线/快照/候选 Bundle。产物在打印的证据目录，密钥只在子进程环境传递。测试源为真实隔离业务操作 fixture，等级 L2；不代表生产混合用户来源、DC 用户塔预测、正式训练/推荐质量、线上 PairRelease 或服务激活。RTW 测试源和对象存储服务关闭后，清单中的 localhost S3 URL 仅是隔离验证地址，字节保存在该证据目录的 SeaweedFS 数据文件中。
