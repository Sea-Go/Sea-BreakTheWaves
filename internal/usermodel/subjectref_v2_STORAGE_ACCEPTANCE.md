# SubjectRef v2：事实账本存储扩展候选

状态：**阶段三的局部候选**，不是 23 张表的迁移完成，也不是生产库已应用。对外主体固定为 `{issuer:"rtw.identity",subject_id:"<RTW 正 int64 UID>"}`；旧 `tenant_id="platform"` 仅是 v1 存储兼容槽，不代表租户。

## 工作边界与依赖

`[W0:ROOT]` 本独立 BTW 开发 worktree；`[W1:WRITE]` `migrations/usermodel/007_subjectref_v2_projection.sql`、嵌入声明、`internal/usermodel/facts.go` 的显式 Option、`internal/usermodel/subjectref_v2*` 与本页；`[R1:READ_ONLY]` Sea-Docs 六阶段设计、原 001..006、原 preflight、RTW/DC/warehouse/transport 消费者；`[D1:DEPENDENCY]` `pgx/v5 v5.8.0` 与框架锁定 `tRPC-Agent-Go v1.8.1` 的模块缓存；`[G1:GENERATED]` 原 preflight `contract.json`/结构清单继续由冻结 001..006 生成，不手改；`[X1:EXTERNAL]` 本任务脚本创建并停止的随机端口隔离 PG16，生产库及远端状态只读且未访问；`[N1:OUT_OF_SCOPE]` 主 checkout 脏改动、其他仓、旧对象、Serving 活动指针；`[T1:TEMP]` 脚本专属 `sea-btw-usermodel-v2-storage.*` 临时 PG与日志。主职责 `[C4:PERSISTENCE]`；跨区 `[C3:DOMAIN]` 严格身份、`[C7:CONTRACT]` v1/v2 适配、`[C8:VERIFY]` PG/race。没有新增 Agent、Tool、Graph 或 telemetry 管线；事实存储复用现有事务和 OBS Bundle，正式入口仍应使用既有 tRPC-Agent-Go Runner/Tool 装配。

## 候选契约

1. 运行**旧 schema** 的 `cmd/usermodel-subjectref-preflight --run` 并处置全部 L1/L2、对真实 UID 与去槽业务键碰撞签收，再由部署 owner 显式执行 001..006 → **007**。`NewStore` 永不应用 DDL；007 不回填，也不更新任何旧事实、`event_body`、`normalized_hash`、Outbox payload、原收据或活动 Serving 指针。
2. 007 建 `usermodel_subjectref_v2_projection`：旧三元组主键、`(issuer,subject_id)` 唯一键、指向旧 `subject_state` 的有效 FK、固定 issuer/platform 与正 int64 UID 的 CHECK，以及不可更新/删除触发器。身份比较固定 `C` collation，不由数据库默认排序规则猜测 UID。空 sidecar 的 FK 在创建时已有效，不扫描旧 23 表。四个 `*_subjectref_v2` 只读视图让 state/event/active/outbox 以 v2 键查询，原业务行只被 JOIN 一次；没有投影的旧行不会冒充新空主体。仓内 `SubjectRefV2.UnmarshalJSON` 只收 `issuer/subject_id` 两字段，未知/重复/缺失字段、错误兼容槽和非规范 UID 在 DB 副作用前拒绝。
3. 应用默认 `NewStore(pool,bundle)` 仍只走 v1。部署显式选择 `WithSubjectRefV2Candidate()` 后，旧 `Append` 接受且只接受规范 `rtw.identity/platform/UID`；同一事务先复用原事实状态/Outbox提交，再插入 v2 映射。映射拒绝、唯一冲突或事务失败会使**整笔事实写回滚**。`ProjectExistingSubjectV2` 只为已有旧主体插入映射且同映射可重试，不批量猜测或重写旧记录。已验真 v2 来源可调用 `AppendV2`，它核对事件中未被签发者覆盖的旧主体、固定补旧兼容槽，然后调用同一账本事务；同 EventKey/hash 的 v1/v2 重放只有一行、一个版本和一份 Outbox，异文冲突。
4. 显式 `CurrentV2`、`HistoryAfterV2` 和 `OutboxAfterV2` 先按不可变投影解析**内部**旧键，再复用原读面。返回的 `Projection/Fact/OutboxRecord` 暂仍是内部 v1 类型并含旧主体，不能作为新的对外 v2 wire；四视图为后续领域 owner 的正式 v2 读面准备。旧 Session/coverage、feature、bundle、dataset 的新合同不由本切片暗中开启。
5. 回滚只把应用 Option 关闭，旧键读取和原事实写仍有效；sidecar 保持闲置以便复查，旧约束与活动指针不删除。投影行不可更改；有异常时停在预检/候选，不通过删除 slot 把两个旧行归并。

结构化局部观测沿用注入的 `telemetry.Bundle`：`usermodel.subjectref.v2.{append,project,resolve}` 输出 JSON 阶段日志、Span与仅按有界事件/结果聚合的指标，标签为 `subject_ref_version=v2`；UID不作为 Prometheus标签。它们不替代 tRPC-Agent-Go 原生 Runner/Tool spans，也不证明 Collector 或线上跨服务 Trace。

## 隔离验收与交接

`bash internal/usermodel/subjectref_v2_test_postgres.sh` 在随机端口 PG16/race 下验旧历史先无映射后按主体投影、v1/v2 Current/History/Outbox 对账、旧 PG `event_body::text`/normalized hash/Outbox `payload::text` 前后字节相同、逻辑零序号存 PG NULL/无水位、正序号有连续水位、16 并发 v1/v2 同事件只提交一行、异文CONFLICT、严格 v2 JSON、错误兼容槽/UID/发行者拒绝、DB CHECK/FK/不可变触发器、默认关闭与 Option 回退。PG 的 `jsonb` 是原 v1 **已存规范 JSON**，不是原来源网络字节；本切片不会把它伪称未存的原始事件信封。脚本保留仅本任务创建的日志和已停止 PG 数据以便审查；此记录只能标 `LOCAL_VERIFIED`。

最终本分支隔离验收使用 `USERMODEL_V2_TEST_FILTER='.' bash internal/usermodel/subjectref_v2_test_postgres.sh`，串行 `-p=1` 与 `-race`：37 个顶层测试 PASS、4 个依赖当前不存在的真实 DC/RTW 服务或既有归档工件路径的测试 SKIP、0 FAIL；`test.log` 文件 SHA-256 为 `f55b75aa3f2def818df3d4a594d2de0e57623920c6fccd9edccc5bf610ad0a0c`。跳过项依次是 `TestCoverageVerifierRealDCAndRTW`、`TestPublishedCoverageArtifactInterop`、`TestAcceptCombinedPublisherAndH10OriginalArtifact`、`TestCoveredSnapshotFromOriginalH10Bytes`；它们不是本 v2 sidecar 测试。专用隔离 PG16 的 `pg_ctl status` 在脚本结束后退出 3，确认无服务运行。聚焦 vet、`go mod verify`、`git diff --check` 和全仓 `GOMAXPROCS=2 go test -mod=readonly -p=1 -run '^$' ./...` 编译均退出 0。全仓编译不代表全仓业务测试或生产跨服务链路已通过。

交给后续 owner 的仍是**剩余实施面**：19 张完整三元组表的正式 issuer 索引与依赖顺序、未映射外部主体、Ontology namespace 两表、无主体 coverage_prefix、有效/待验 FK、全量迁移水位；BTW warehouse 的 PG/ClickHouse/dbt/Parquet/manifest 双版读取；RTW 签发/真实 v2 producer；旧 Session 回读、coverage/feature/bundle/dataset 新合同和 Serving CAS；生产表真实异常行预检、全量逐表对账、恢复演练。把本候选合并或把它的局部 PG 测试通过都不能将整体阶段三或 47 项工程标为完成。
