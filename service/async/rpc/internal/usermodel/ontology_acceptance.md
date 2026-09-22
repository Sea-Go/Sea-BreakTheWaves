# WS08-B 用户 Ontology 局部验收

工作区域：[W0:ROOT] 本独立 BreakTheWaves 工作树；[W1:WRITE] `internal/usermodel/ontology*.go`、本说明、`migrations/usermodel/002_ontology.sql`；[R1:READ_ONLY] 现有 `facts.go`、`projection.go`、`graph.go`、WS07/WS08 文档和其他项目；[D1:DEPENDENCY] 锁定 Go 模块；[G1:GENERATED] 无；[X1:EXTERNAL] 测试脚本创建的临时 PostgreSQL 16，仅用于隔离验收；[N1:OUT_OF_SCOPE] 内容塔、推荐策略、目标、计划、日历及其他工作树；[T1:TEMP] 脚本随机临时数据目录，完成后删除。主职责区为 [C3:DOMAIN]，跨 [C4:PERSISTENCE] 版本头与可重建投影、[C8:VERIFY] 真 PG/race 验收；输入是 WS08-A 已接纳活跃事实的固定 `SubjectRef/state_version`，输出是图关系/证据/派生计数和交给 WS08-C 的受影响范围。

## 已实现的窄合同

- `OntologyDefinition` 以 `(authority_id, tenant_id, version)` 不可变存储。对象定义有属性类型与可空性；本切片的关系固定从 `subject` 向外，按 `semantic_kind + predicate` 绑定已接纳事实，用 `value_ref` 作为目标对象 ID，声明一对一/一对多与秒级窗口。这里未宣称任意对象间图遍历或模型推断属性已实现。
- 派生规则只支持整数计数/求和：输入是 `relation.NAME` 的去重目标数，或其他 `rule.NAME` 的整数值；缺失输入为零。同版本按规范化定义 hash 重投是幂等；异内容冲突。发布前拒绝错类型、未定义字段/关系、重复声明、循环、非用户动作和超过支持范围的窗口。激活头仅能从版本 `n` 原子前进到 `n+1`，失败时保留旧版本。需要回滚规则时应复制旧定义为新版本并发布，不篡改历史定义。
- `BuildOntologyProjection` 只读固定 `Current` 活跃事实和显式 `as_of`，输出关系目标、每条证据的 `producer/event_id/evidence_ref/hash/accepted_version/occurred_at`、派生值及最早下一次时间变化。它不会写入源事实或执行目标/日历/策略。`RebuildOntology` 把相同内容写入 PG 可重建投影，保存事实版本与定义版本；时间回退拒绝。`ReadyOntologyProjection` 核对事实/定义版本及时间边界，旧投影返回 `ErrPending`，避免 WS08-C 读到伪新结果。`PendingOntologySubjects` 按完整身份范围和游标列出来源版本、定义版本或时间边界变化的主体，进程重启后仍可重新列举；它是待重建查询，不是已经送达的任务 ACK。
- 重建比较旧新关系及证据，即使派生数值相同但证据变了，也列出受影响的 `subject` 和目标对象；派生规则变化列出规则名。交接目标严格只有 `user_state`、`user_features`。撤回后先由 WS08-A 使源事实失效，再由本切片重建并返回移除对象；晚到事实按来源时间参与结果，不靠接收顺序。WS08-C 尚需按这份范围更新真实特征并给出自己的回执。
- 显式部署顺序为 `001_facts.sql` 后 `002_ontology.sql`；Store 构造器不迁移。定义、事实和投影都在 PostgreSQL；Agent Session/Memory 不是事实权威。观测沿现有进程注入 `telemetry.Bundle` 走 `usermodel.ontology.*` stage；本切片没有另建 Logger、Tracer、Registry、Runner。

## 已核验和未核验

`bash internal/usermodel/test-postgres.sh`：随机端口与 schema 的隔离 PostgreSQL 16，`go test -race -count=1 -v ./internal/usermodel ./migrations/usermodel` 和 `go vet` 通过。测试覆盖规范化与并发同定义重投/异文冲突、错误类型/循环拒收、事实与关系证据、撤回及晚到影响、计数不变时的证据修正、主体与租户隔离、单连接池、时间窗口到期、重启读取与删除投影后从源事实重建。

状态为 **WS08-B 组件 LOCAL_VERIFIED**。尚无真实图后端、WS07-C 历史影响清单、WS08-C 特征消费/回执、RTW→DC→BTW 真事件接入或 Collector 下钻；因此 H08.b/H10.c/H12 整体均未验收。时间变化由 `PendingOntologySubjects` 被周期性查询才会推进，正式 worker 的调度、租约和交付 ACK 尚未实现。
