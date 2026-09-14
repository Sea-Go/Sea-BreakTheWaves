# WS08-F 配对、item 索引与排序局部验收

日期：2026-09-14。独立工作树 `feat/recommend-pair-release-20260914` 从 BTW 集成远端 `7beacf6` 建立；该基线已含 WS08-D `ServingBundle` 与 WS08-E `PoolRelease`，未吸收无关 H06 worker 的未推提交。`[W0:ROOT]` 本独立 BTW 工作树；`[W1:WRITE]` `internal/recommend/{pair_release,pair_store,ranking,pair_store_test,pools_test}.go`、`migrations/recommend/{002_pairs.sql,migrations.go}` 与本说明；`[R1:READ_ONLY]` Sea-Docs WS08-F/H08.b/H11.b、RTW人工发布、BTW usermodel/PoolRelease、training/export候选和DC客户端；`[D1:DEPENDENCY]` 根模块当前pgx与tRPC-Agent-Go锁定版本；`[G1:GENERATED]` 无；`[X1:EXTERNAL]` 只在脚本创建的隔离PG16写入，无共享/生产操作；`[N1:OUT_OF_SCOPE]` root集成树、原脏树、Docs、H04/H06 worker；`[T1:TEMP]` 隔离PG证据目录。主职责 `[C4:PERSISTENCE]`，跨 `[C3:DOMAIN]` pair/space/feature/index兼容与CAS、`[C2:APPLICATION]` usermodel授权和计划、`[C7:CONTRACT]` 模型/索引/批准探针、`[C8:VERIFY]`。

## 已实现的确定性合同

`PairProposal` 不可变绑定同模块 RTW 活动 `PoolRelease`、其 item metadata FeatureHash、用户 FeatureSpec version/hash、PairRef、user/item 权重内容hash、相同space/dimension/metric、训练候选清单、ranker种类/权重与策略版本。`ItemIndexGeneration` 是另一个不可变预构建声明，必须覆盖该 PoolRelease 的**每个** item ID、revision ID、content hash；错空间、错维度、错修订、旧发布或漏项拒绝。候选和索引登记都不会改变活动指针；没有验证器时仍可准备它们，`Approve` 及服务请求明确 `Pending`。

`Approve` 要求独立 `PairVerifier` 返回匹配权重/空间/维度/metric 的模型探针、匹配索引hash与全覆盖数的 item index 探针、ranker规则/权重探针和批准回执。模型/索引/ranker探针在批准及激活时必须处于五分钟有效窗；同 proposal/index/approval 重试返回唯一不可变 `EncoderPairRelease`。`Activate(release_id, expected_pointer_version)` 在事务内再次核对 RTW 内容池与所有探针，按模块锁与批准修订单调 CAS 活动指针。旧代保留；人工回退必须选仍兼容当前 RTW 内容的已批准 release 并传当前 CAS 版本。

`PairStore.AuthorizePair` 实现 usermodel `PairAuthorizer`，只有活动模块中能再次核对当前 RTW PoolRelease、完整 PairRef、索引和批准的配对才回 `Active=true`。接口不带 module ID，因用户向量按全局 pair 构建；真实推荐请求必须再调用模块级 `Active(module)`。`Plan` 依模块活动 PairRelease 选择当前 usermodel `ReadyServingBundle` 与内容池，拒旧/错space、主体错配及变化的Bundle或pair指针；交付前再核两侧指针。`fixed_baseline` 且全部批准时仅返回明确 `rule_baseline/freshness_rule` 的位置列表，无概率分数；model pair 缺正式评分执行器时只返回 `pending(ranker_unavailable)` 且没有任何排序项。没有配对或DC/index探针时返回 `pending(pair_unavailable)`。本计划不是公开 Slate、展示收据或H09阶段事实。

跨 RTW/BTW/usermodel 无全局事务。`Plan` 在最后一次读取前后比较Pair与Bundle完整指针，只证明该请求的最后核对点一致；随后发布/撤回发生时，外部产品派发还应按响应携带的release/pointer版本执行读时失效门禁。不能把请求时冻结解释成跨系统无限期锁定。

`Activate` 的PG `Commit`错误或提交后的RTW/探针复核失败返回 `ErrActivationUnknown`，并携带预期提交的 `PairPointer`；调用方必须用只读 `CurrentPointer(module)` 回查实际版本/ID，再用 `Active(module)` 核当前资格。错误返回不等于无副作用。后置资格失败时活动行可以存在，但 `Active`/`AuthorizePair`/`Plan`仍不交付可服务结果；探针恢复后按同一指针恢复，无需重造release。跨服务同步撤回仍没有全局锁，正式产品派发需做版本门禁。

## 执行与证据

既有 `internal/app/TestRTWRealItemPoolHandoff` 可选真实RTW父夹具已扩展：同次将已发布真实item/revision/content-hash全集写入配对proposal与索引预构建代，并断言没有DC/index/批准验证器时仍 `Pending`、不产生Slate。该子测试当前在无 `SEA_RTW_ITEM_POOL_FIXTURE` 时只编译并跳过；**这不是已执行的真实RTW→Pair跨仓验收**，固定权重和索引对象也不等于真实DC工件。RTW父测试复跑后才能提升该子链状态。

最终 `RECOMMEND_KEEP_EVIDENCE=1 bash internal/recommend/test-postgres.sh` 退出0；日志 `/tmp/sea-btw-ws08f-pg-final-20260914.log`，隔离PG证据 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T/sea-recommend-acceptance.V2qZh9`。`TestRealPostgresPairActivationUnknownAfterCommittedPointer` 在真实PG里使前两次探针通过、提交后第三次失败，核`CurrentPointer`可见已提交版本而`Active`与`Plan`拒绝服务；探针恢复后同一指针恢复。

隔离PG16脚本以 `-race -count=1` 跑推荐包和app，局部vet退出0；根 `go test -mod=readonly -race -count=1 ./...`、`go vet ./...`、`go mod verify` 均退出0。测试证明：proposal/index不自动授权，缺验证器与ranker探针拒批准，错space/缺item覆盖拒收，同批准重试ID稳定，撤销后同键批准回放拒绝，并发两个相同CAS只有一个激活，未激活release不给usermodel授权，活动后DC探针失效或RTW内容换代即不给授权/列表，错Bundle不给列表，计划期间活动pair切换不给旧规则列表，model pair没有真实评分器时保持Pending；触发器拒改不可变release。

这里的 user/item/ranker权重、模型调用、item索引和批准证明**全部来自固定测试夹具**。`training/export`只交候选包及本地数值对照；BTW当前DC客户端没有推荐双塔/精排实际装载与probe适配，也没有item索引Builder/Reader/正式批准者。故当前状态 **`LOCAL_VERIFIED/PARTIAL`**，绝不称真实H11.b活动模型、线上推荐或实际收益。H08.b真实本人Bundle跨store链、同次RTW真发布到pair/索引、正式H09.c请求/候选/展示事件、实验分桶/反馈与Collector下钻仍待对口owner联验。

部署先显式应用 `migrations/recommend/001_pools.sql`，再应用 `002_pairs.sql`；`NewStore/NewPairStore` 不迁移数据库。真正激活前，H05/DC应提供带权重/输入/空间/版本/调用ID的真实模型和ranker探针，WS08 item_index提供固定内容全集/对象hash/查询探针，推荐批准者提供可撤销的批准证明，WS08-D在同一PairRef下预构建并验证用户Bundle，WS09/H12提供对照报告；所有这些提供者完成后才可装配真实 `PairVerifier` 并执行 CAS。
