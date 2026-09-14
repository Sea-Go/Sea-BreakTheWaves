# WS08-A 用户事实 GraphAgent/Runner 局部验收

日期：2026-09-14。隔离分支 `feat/usermodel-graph-20260914`，固定集成基线 `ac3cc4c`。本次只新增 `internal/usermodel/graph.go`、窄测试和本记录；不改事实 Store、迁移、生产入口或其他工作树。

工作区域：[W0] 本隔离 BreakTheWaves 工作树；[W1] 上述三个文件；[R1] `internal/usermodel/facts.go`、`internal/runtime/`、`internal/telemetry/`、Sea-Docs 交接契约；[D1] 本模块解析的 `trpc-agent-go v1.8.1` 与 PG Session `v1.8.0` 模块缓存；[G1] 无；[X1] 仅随机端口、本地临时 PG16 测试库写入；[N1] 原业务树、其他服务、共享/生产状态；[T1] `internal/usermodel/test-postgres.sh` 生成并清理的随机临时目录。主职责 [C5] Agent/Graph 适配，交接 [C4] 已有 PG 事务/Outbox、[C2] Runner 调用、[C8] 真库与 Span 验证。

## 入口与提交语义

`NewFactGraphRuntime` 组合项目 `runtime.Runtime` 和锁定版本的原生 `GraphAgent`，要求事实 Store 与 Runner **借用同一个已安装的** `telemetry.Bundle`。Graph 用 `agent.MergeRuntimeState` 在每次 Run 固定已由可信接入适配器绑定的完整 `SubjectRef`、`producer` 和源事件，函数节点调用权威 `Store.Append`；模型文本不参与事实写入。调用者拥有并关闭 Graph Runtime，之后才关闭借用的 Session、PG Pool 和 Bundle。

`Store.Append` 在一个 PG 事务里写事实、当前状态版本与 Outbox，并在 `Commit` 成功后返回。Graph 收据同时核对完整主体、事件键、规范化 hash；仅 `status=accepted` 且版本正数时填写 `accepted_version`。缺前驱返回 `pending_dependency`，接受版本固定为 0；重复投递保留原始 accepted/pending 收据。`FactGraphRuntime.Append` 经真正 Runner 消费流至 EOF，要求恰好一个 Graph completion、Runner completion、无运行错误，才向上游返回收据。取消、缺 completion、冲突或 Outbox 失败一律不给 ACK；若 PG 在调用方失联前已经提交，上游以相同事件键/hash 幂等重试取得原收据，不从 Runner completion 推断新版本。

这仍是**局部业务入口组件**：RTW 签发主体与 producer、DC H04/H09.a 来源序列/证据接入、正式进程装配、Outbox 的 WS08-B/C 与数仓消费、Collector 跨服务下钻尚未完成。不能据此将 H08/H09 或 OBS-07/08 标成 `ACCEPTED`。

## 本地证据

运行 `internal/usermodel/test-postgres.sh`：隔离 PG16、`go test -race -count=1 -v ./internal/usermodel ./migrations/usermodel` 和 `go vet` 通过。脚本的随机 schema 用真实迁移建表，退出后停止 PostgreSQL。全根模块 `go test -mod=readonly -race -count=1 ./...`、`go vet ./...`、`go mod verify` 均通过；根模块普通测试未注入 PG DSN，真库结论以隔离脚本为准。`TestFactGraphPostgresRunnerNativeTrace` 在全新子进程安装唯一 Bundle，实际执行 GraphAgent/Runner/PG：

| 核对项 | 结果 |
| --- | --- |
| 首次接受与重投 | 首次版本 1、同主体事件键的 Outbox 版本 1；重投仍为版本 1，`replay=true`。**LOCAL_VERIFIED** |
| 待前驱、冲突、Outbox 回滚、取消 | 待前驱没有接受版本或新增 accepted Outbox；异 hash 和故意删除 Outbox 表均无 Graph ACK；Outbox 故障无事实写入；预取消无事实写入。**LOCAL_VERIFIED** |
| 框架原生链路 | 同一真实 Trace 内有 `trusted_adapter → runtime.run → invoke_agent usermodel_fact → workflow execute_graph usermodel_fact → workflow execute_function_node commit_fact → usermodel.fact.append`；Agent/Graph/节点 Span 的 instrumentation scope 为 `trpc.agent.go`。**LOCAL_VERIFIED** |
| 统一观测 | 成功事实 JSON 日志带同一 Trace ID；冲突日志为领域 `rejected/FACT_CONFLICT`；`/metrics` 同时含 bounded 应用与原生框架系列，并排除请求/主体 ID 维度。集成Runtime修复后，Sink拒收触发内部取消但本次终态保留`failed/RUN_SINK_FAILED`。**局部 LOCAL_VERIFIED** |

## 锁定框架的错误事件竞争

首次用节点原生 `return error` 测试失败路径时，`-race` 在 `trpc-agent-go v1.8.1` 的 `runner.ensureErrorEventContent` 写 `Event.Response.Choices` 与 `graphagent.runWithBarrier` 调用 `internal/telemetry.TraceAfterInvokeAgent` 读同一 Event 之间报数据竞争。模块版本来自当前根 `go.mod`/`go list -m all`，两处调用点在该版本模块缓存的 `runner/runner.go` 与 `agent/graphagent/graph_agent.go`；本分支不修改框架或 go.mod。

为保留框架原生 Trace 且避免此路径，节点把 `Store.Append` 的业务拒绝与存储故障转换为**仅含稳定 `error_code` 的 Graph 结果状态**，不返回接受收据；Runtime sink 识别它、拒绝整次运行并返回 Go error。权威 Store stage 记录精确错误与拒绝/失败结果。集成提交`8a754cb`已修正项目Runtime内部取消的错误分类：冲突/Outbox故障的Runtime终态为`failed/RUN_SINK_FAILED`，而主动取消仍为`cancelled`；集成分支隔离PG16脚本已实际重跑，14项测试含原生Span/指标和Runtime JSON终态均通过。框架原生 Graph 节点 Span 在这种受控失败下仍可能标 `OK`，不能只看原生Graph Span判断业务结果；框架修复/升级后应重新验证原生错误路径。真实调用返回无 ACK；取消沿 Context 传播。
