# SubjectRef v2 阶段一：BTW usermodel 只读预检

本切片固定 BTW 开发集成提交 `f51723e0a1db3b5029342ef8207fb2255be79cec`。它审核旧 usermodel 事务库在删除兼容 `tenant_id` 前是否满足迁移前提，不创建 v2 表、不写 v2 行、不修改旧事件、收据、数仓对象或 hash。逐表真实列、PK、FK、唯一键见 [物理结构清单](SCHEMA_INVENTORY.md)。该清单以七份旧 SQL 在隔离 PG16 上部署后的 `pg_catalog` 为证据，不以文本命中数为证据。

工作区域声明：`[W0:ROOT]` 本独立 BTW worktree；`[W1:WRITE]` 仅 `cmd/usermodel-subjectref-preflight/` 与 `internal/usermodel/preflight/` 新文件；`[R1:READ_ONLY]` 旧 `migrations/usermodel/001..006`、现有事实/投影实现及相邻 RTW/DC 契约；`[D1:DEPENDENCY]` 锁定 `pgx/v5 v5.8.0` 只读核对；`[G1:GENERATED]` `contract.json` 和清单由隔离 PG16/generator 生成，不手改；`[X1:EXTERNAL]` 仅测试脚本创建的临时 PG16，未连接生产；`[N1:OUT_OF_SCOPE]` 主 checkout 脏改动、RTW、搜索 HTTP、旧工件与哈希；`[T1:TEMP]` `/tmp/sea-srpf-*` 的脚本专属临时 PG 和 CLI 文件。主职责区 `[C8:VERIFY]`，跨区读取 `[C4:PERSISTENCE]` 的物理 schema 与版本/指针不变量；它不改变生产者或消费者合同。

`--run` 与 `--dsn-env` 必须同时显式指定；默认启动不读取连接环境，也不连接数据库。连接值只从指定环境变量取，命令行、结果和结构日志均不回显 DSN。CLI 会把会话默认事务设为 read-only，再核 `SHOW transaction_read_only` 和 `SHOW transaction_isolation`；所有 catalog 与行计数都在同一笔 `REPEATABLE READ READ ONLY` 事务执行。`--schema` 指向旧表所在 schema，默认 `public`。每条 SQL 限时 30 秒，整体限时 5 分钟；超时或查询失败会标成审计未完成，不产生可冒充通过的报告。

```bash
# 由操作者在受控环境中预先提供 BTW_PREFLIGHT_PG_DSN；此命令没有写入动作。
GOMAXPROCS=2 go run -mod=readonly -p=2 ./cmd/usermodel-subjectref-preflight \
  --run --dsn-env BTW_PREFLIGHT_PG_DSN --schema public
```

stdout 是单个 JSON 业务报告，含规则/表名/命中计数、23 张 scoped 表各自的扫描行数、总行数和 SHA；`row_audit_complete=false` 表示列缺失后已在 catalog 阶段阻断，不能把零行数误读成完成扫描。stderr 是 CLI 独立 `slog` 单行 JSON 阶段日志，含开始、catalog 完成和终态，走注入 observer，不在审核函数中用 `fmt` 输出阶段信息。没有实际 Trace 时不伪造 trace/span。退出码 0 表示 scoped L1/L2 均为零，1 表示发现阻断，2 表示未完成或配置拒绝。`--mode catalog` 只供隔离 PG16 schema oracle 生成与结构复核使用；生产报告应保持 `--mode audit`。

`report_sha256` 精确表示将该报告的 `report_sha256` 字段置空后，经 Go `json.Marshal` 得到的紧凑 JSON 内容的 SHA-256。CLI stdout 文件包含已填写的字段和结尾换行，因此实际文件字节 SHA 不同；验收记录分别列这两种 SHA。表行数虽没有 UID，也可能揭示小规模主体群体，报告与 SHA 应按受限运行证据管理，**不是安全匿名化或生产身份认证**。

规则级别固定如下：

| 级别 | 迁移判定 | 规则 |
| --- | --- | --- |
| L1 | 阻断 | 旧列/约束/唯一键缺失或漂移；任一旧主体不满足 `rtw.identity/platform/规范正 int64 UID`（包括已绑定 unmapped 的 UID）；按 issuer、UID 和真实 PK/唯一业务键去掉 tenant 后碰撞。|
| L2 | 阻断 | 预期 FK 的逻辑 orphan；没有物理 FK 的 outbox、水位、binding/已绑定未映射事件主体缺失或同外部键指向不同 UID；Outbox 必须从版本 1 至状态 V 连续；coverage_event 必须引用已接纳且版本一致的事件；有来源序号的事件必须有整条 watermark；现有水位行内部不一致；未被已接纳后继覆盖的非撤回事件必须存在 active_fact；投影、特征、历史候选版本越界；pointer 与 bundle 的 pair 不一致。|
| L3 | 需复核 | Ontology 活动投影落后于当前定义/状态，或已有活动 head 与 subject state 却缺 projection，或 scoped 表出现额外结构键。这些是重建/确认候选，不单独证明数据身份可迁。|

水位 `contiguous_sequence < max_seen_sequence` 本身不是断档错误：待依赖事件、迟到序号可以使尾部未连续；只审缺整行及已存行的 max/contiguous/已接纳前缀关系。活动事实反查也只要求没有**已接纳** successor 的非 retract 事件；已接纳纠正/撤回链的旧前驱不需要仍留在 active 表。

旧测试中的非 platform、非数字主体是早期 synthetic 样例。负例将它保留为旧行并报告 L1；审核不会把它推断成 RTW 正式 UID，也不会归并到其他用户。成功运行只证明被审核库在该快照的**这些结构和聚合计数规则**没有发现阻断。旧事件 normalized hash、原 JSON/bytea、外部 object manifest、Recommend 配对批准真实性和不可变触发器行为不是本切片的内容验证，不能凭此报告切换写者或 serving。

`bash internal/usermodel/preflight/test-postgres.sh` 启动隔离 PG16，只在随机 schema/临时进程写测试 fixture，做七份迁移结构一致性、空库扫描、synthetic 非规范主体、正 int64 边界、去槽碰撞、缺失 FK/orphan、Outbox 版本空洞和 Serving pair 错 owner 的原负例。新增六个独立旧库可插负例分别由冻结 `1150534` CLI 实测旧 L1/L2 为零，再由新版命中五条 P1 规则；另审合法 pending gap、已接纳后继/撤回链和缺 Ontology 投影。测试启用 `-race`，仅跑本包与 CLI，`-p=2` 限制磁盘/构建并行；随后验证 CLI 默认关闭、结构化 stderr 与只读结果。`generate-contract.sh` 和 `render-inventory.py` 只在冻结 migration 源与隔离 PG16 一致时生成本切片 oracle/清单；源 SHA 改动会使隔离测试失败，不能用生产库刷新 oracle 掩盖漂移。

本分支的本地 PG16 证据是 `LOCAL_VERIFIED`。真实 BTW 生产表、线上 RTW 签发数据、Collector→DC 回查和完整 OBS-r3 路径均未审核，分别标为 `NOT_VERIFIED`；本 CLI 的受控 JSON 阶段日志只证明维护入口的局部输出合同。
