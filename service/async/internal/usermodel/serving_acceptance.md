# WS08-D 用户编码与 ServingBundle 验收

日期：2026-09-14。结论：**固定基线编码的隔离跨存储子链 L2 INTEGRATED；WS08-D/H08.b/H11.b 整体 PARTIAL**。本文件记录的是当前源码与实际执行的验收，不把测试授权、合成事实或固定向量规则当成正式推荐模型发布。

## 工作区域与所有权

- [W0:ROOT] 独立工作树 `/Users/edy/Sea/.codex-worktrees/sea-btw-usermodel-serving-20260914`，起点为 BTW 集成分支 `caed957`。[W1:WRITE] 仅 `internal/usermodel/serving.go`、直接测试/本说明、`migrations/usermodel/004_serving.sql`，以及新增的跨域**消费者测试** `internal/warehouse/featurebaseline/serving_consumer_test.go`。原 WS07-C 提供端、原验收脚本和其测试不改。
- [R1:READ_ONLY] Sea-Docs H08.b/H11.b/WS08-D、现有 FeatureSnapshot 与 warehouse Runner、training/export 候选工件、DC 结构化 prediction 合同和 recommend 未来配对 owner。[D1:DEPENDENCY] 锁定 Go/pgx/tRPC 模块缓存只核对；本切片为确定性领域/事务代码，不新增 Agent、Runner、Tool 或模型适配。[G1:GENERATED] 无手改生成文件。
- [X1:EXTERNAL] 仅脚本创建的随机端口隔离 PostgreSQL 16、ClickHouse、SeaweedFS、dbt 实例；已停止，没有共享或生产服务写入。[N1:OUT_OF_SCOPE] 原始脏树、DC 模型加载、recommend pair/item 发布、正式 RTW/H09 事件、线上实验。[T1:TEMP] 隔离验收目录保留为证据。
- 主职责 [C4:PERSISTENCE]：不可变 Bundle 与按主体/配对的 CAS 指针。跨 [C3:DOMAIN] 配对/空间/快照兼容与失败语义，[C2:APPLICATION] 编码器及推荐批准接口，[C8:VERIFY] PG/race 和真实 DWS 消费测试。生产者为 usermodel FeatureSnapshot 和未来 DC 结构化预测；唯一配对批准者为 recommend；消费者为 recommend 请求链。

## 已实现合同

1. `BuildServingBundle` 只读取 `ReadyFeatureSnapshot`。`PairRef` 固定 pair、space、encoder、`FeatureSpec` 版本/hash、维度、metric 和 `fixed_baseline/model` 类型；输入快照的事实版本、Ontology、基线 generation/revision、逐来源水位与尾部由不可变 `feature_snapshot_id` 锚定并写入 Bundle。注入编码器返回完整身份和向量；错空间、错维度、非有限值、错误模型来源、缺 DC `model_call_id` 均拒收。当前没有生产 DC 编码器适配；测试只使用显式固定 `reading_count/recent_article` 规则。
2. 编码发生在 PG 事务外，落库前锁主体状态并再次核对当前特征 ID、事实版本、基线/Ontology 头及未来失效点；期间的新事实或新快照会拒绝过期计算。Bundle 内容经 SHA-256 得到 ID，PG 外键绑定同主体不可变 FeatureSnapshot，行级触发器拒绝 UPDATE/DELETE；同一输入重试得到同 ID。旧 Bundle 可凭完整 `SubjectRef + bundle_id` 回读，但这不表示它仍可服务。
3. `ActivateServingBundle` 先调用 recommend 所属 `PairAuthorizer`，要求回执的**完整 PairRef**、有效批准引用和单调 revision。随后在主体锁内检查 Bundle 与当前快照，按 `expected_pointer_version` CAS 切换本人的 pair 指针；`expected=0` 仅允许首次创建。候选 Bundle 不自动成为当前表示，旧任务不能覆盖新指针。当前只有测试 fixture 授权实现，**未接推荐域真实 EncoderPairRelease、item index 或 DC 装载/探针**；不能据此称 H11.b 已批准。
4. `ReadyServingBundle` 要求 recommend 提供当前 PairRef，在一致 PG 快照下核对指针 `active`、完整 pair/space/encoder/特征合同及当前特征 ID。缺指针 `ErrNotFound`；停用、事实/基线/Ontology/窗口过期 `ErrPending`；错误授权、旧 CAS、同维异空间、错版本 `ErrConflict`。停用只更新指针，留存不可变历史 ID。正式跨 recommend/usermodel 两库激活还需要推荐域的 release epoch、预构建 item 索引和读时批准检查；本地批准回执没有证明跨服务原子发布。

## 已执行的验收

| 层级 | 命令与证据 | 实际结果 |
| --- | --- | --- |
| PG 局部 | `USERMODEL_KEEP_EVIDENCE=1 bash internal/usermodel/test-postgres.sh`；日志 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T/sea-usermodel-acceptance.ELNZ5K/go-test.log` | PG16 + `-race -count=1` + vet 退出 0。两代 PG 基线 fixture、尾部/事实刷新、重启读、幂等/不可变、跨主体/租户、错配/无批准、失效/停用、编码期间新事实均有断言。 |
| 真实 DWS 消费 | `python3 warehouse/feature_baselines/acceptance.py --runtime /tmp/sea-ws07c-ws08c-runtime-verified-20260914`；`/private/tmp/sea-ws07c-ws08c-runtime-verified-20260914/feature_handoff_ar39erml/go-test.log` | 退出 0，两个 Go race 测试均实际运行。新消费者测试从接纳的 PG 事实经 CH/dbt、SeaweedFS S3 两代不可变清单回到 PG FeatureSnapshot，再固定编码、Bundle、CAS、本人/历史回读。第一代值 2 且尾部 1；第二代值 3 且尾部 0。来源断档补齐后旧 Bundle 被拒为当前。进程已停止。 |
| 代码/依赖 | `go vet ./internal/usermodel ./internal/warehouse/featurebaseline ./migrations/usermodel`；`go mod verify` | 两项退出 0；模块完整性通过。 |
| BTW 根模块 | `go test -mod=readonly -race -count=1 ./...`；日志 `/tmp/sea-ws08d-go-root-race-final-20260914.log` | 退出 0；此轮没有 PG/CH/S3 环境变量，根模块覆盖编译及非外部环境用例，外部存储证据以上两项专门验收为准。 |

跨存储证据中，第一代 `feature_snapshot_id=61e9fcf6544d67ee77c1756a75cf1811fc611f57641a1e1d39ea34cceebae214`、`bundle_id=f1130ce2af6d18f64810cfc6693c3ce2f9eb6e8c45125c9a2f008cc2a4abe308`；第二代分别为 `fadf4fe29024f5022f9cdf66bbdd86d5f156122f9b790a68d744e10c9c74cccf`、`b8a78a2fff268d55ed9870a10c62830842fc64cb9944fb7f547a2d508a74f4a1`。各代的 source/DWS SHA-256 与 dbt 运行记录在上述 `go-test.log` 及隔离证据目录中。业务事件仍是**合成验收输入**；跨存储组件确为真实进程，不等于线上流量或推荐质量。

## 待交接与验收边界

- WS08-F/recommend 提供不可变 `EncoderPairRelease`、同空间 item index generation、真实授权回执及跨服务 revision/fence；按请求所选 pair 校验本人 Bundle，保留实际使用的 `bundle_id/feature_snapshot_id`。预构建产物与当前生效引用分开。
- H05/DC 提供结构化用户塔 prediction 的装载、探针、`model_call_id`、输入 FeatureSpec hash 与实际 `pair/space/dimension` 响应；由正式 `UserEncoder` 适配调用。当前 candidate training/export 包只能作为待批准工件，不能被固定规则测试替代。
- H09 正式事件与数仓多分区来源、正式获批 FeatureSpec、生产进程的 tRPC-Agent-Go Graph/Runner、Outbox 的 `UserRepresentationReady`、Collector Trace 和线上推荐/质量实验均未在本切片验收。旧请求的精确回放需由调用方**当时保存** Bundle 与 FeatureSnapshot ID；不能由当前指针倒推。
