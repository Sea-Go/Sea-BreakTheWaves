# WS08-A 用户事实事件 worker 局部验收

日期：2026-09-14。隔离分支 `feat/usermodel-event-worker-20260914`，基线 `c282c22`。本记录只说明 `internal/app/fact_worker.go` 的局部交付，不代表 H09.a 或 WS08-A 整体接纳。

工作区域：[W0] 本隔离 BTW 工作树；[W1] `internal/app/fact_worker.go`、`fact_worker_test.go`、本记录；[R1] DC 生成 SDK、`internal/usermodel`、RTW 现有接口与 Docs H04/H09/OBS-r3；[D1] 当前解析的 tRPC-Agent-Go 核心 v1.8.1 与本地模块缓存；[G1] `internal/clients/datacenter/wire/eventing/contract.gen.go` 只读；[X1] 随机端口隔离 PG16 与本机 HTTP fixture；[N1] 原业务工作树、共享服务、生产与其他任务；[T1] 隔离 PG16 临时数据目录。主职责 [C2] 应用交付，交接 [C1] DC eventing、[C3/C4] 用户事实与 Outbox、[C5] 框架 Runner/Graph、[C8] 真实库与运行观测。

## 代码合同与提交点

- `FactWorker` 使用 DC 公开 SDK 的 `ReadEvents → EventReceipt → AcknowledgeEvents`。批次固定 consumer/producer/EventType/schema，逐条核对连续 offset、输入 hash 与 DC 不可变事件收据。`GET` 不推进游标；只在整批每一条都返回已提交的 `accepted_version` 后发送批 ACK，并校验 DC `delivered` 回执。后续条目失败或 ACK 丢失时游标未证实推进；已有事实以 `(SubjectRef,producer,event_id,normalized_hash)` 重放而不双计。
- `TrustedFactBinder` 是 **待 RTW 提供的接入合同**，必须取得 RTW 签发的完整 `SubjectRef`、固定 EventSpec 与来源语义。worker 不从 DC `Payload` 自行认定主体；它将 `producer/event_id`、DC receipt 的 `received_at/input_hash/offset` 固定为领域来源位置，不能由 Binder 改写。RTW 真实主体签发/事件适配器当前未实现，因此没有正式进程装配，也不能把测试 Binder 当生产实现。
- 已有 `FactGraphRuntime.Append` 是 **真实 tRPC-Agent-Go Runner → GraphAgent → `commit_fact` 节点**，节点在运行 Context 下调用用户事实 Store 的 PG 原子事务；Store 在接纳事实时同事务生成版本和 Outbox。worker 的 `Bundle.Begin` 只补应用交付 Span/JSON/有界指标，不构造第二棵 Agent 执行树。Graph/Runner 任一终态不完整时不给 DC ACK。
- `pending_dependency` 已持久但没有接纳版本与 Outbox，初次不 ACK。原 `Store.Append` 对它的重投保留初始 pending 收据；集成分支现要求注入权威 `Store.CurrentReceipt`，在每次Graph运行后按完整SubjectRef+EventKey读取**当前**状态、规范化hash和已提交Outbox。若迟到前驱已提升为accepted，同hash才能ACK该批；仍pending则继续不ACK，不能凭初始收据猜成功。真实RTW主体签发和DC正式来源仍未接。

## 本地验证结果

运行 `go test -mod=readonly -race -count=1 ./internal/app` 与 `go vet ./internal/app` 通过。额外以随机端口的本地 PG16、真实 `migrations/usermodel/001_facts.sql`、DC/RTW HTTP fixture 运行 `go test -mod=readonly -race -count=1 -v ./internal/app -run '^TestFactWorker'` 通过。测试未连接实际 RTW/DC 服务：RTW HTTP fixture 返回服务侧主体，DC fixture 提供固定批次/收据/ACK 失败，PG 是真实隔离实例。

集成分支可直接运行`internal/app/test-fact-worker.sh`复现独立PG16/race/vet。除原有ACK 503重投外，测试先让offset 2的correction停在pending且无ACK，再写入缺失前驱使它同事务提升并有Outbox；第二次同DC批重投Graph仍给初始pending收据，但`CurrentReceipt`读出accepted版本3和相同hash后仅ACK一次offset 2，Outbox合计3条、不双计。测试脚本通过；仍不是RTW真实签发SubjectRef/EventSpec或DataCenter正式服务联验。

| 场景 | 观察结果 |
| --- | --- |
| DC ACK 首次 503 | PG 事实/Outbox 已提交 1 版而 DC 游标未确认；重投同键只返回 replay，Outbox 仍 1 条，第二次 ACK 到 offset 1 |
| 恶意 payload 主体 | payload 中伪造的另一 tenant/subject 未写入；事实落在 fixture 服务返回的完整 `rtw.identity/tenant-a/issued-user-1` |
| 缺前驱 correction | 初次真Graph/PG保存`pending_dependency`且DC不ACK；前驱到达后权威当前收据显示accepted版本与Outbox，重投初始pending回执仍不变，但worker按同hash当前收据对同批ACK一次 |
| 传输与运行观测 | DC/RTW fixture 同一次事件 HTTP 请求携带相同 W3C TraceID；Trace 中 `usermodel.worker.event → runtime.run → invoke_agent usermodel_fact → workflow execute_graph usermodel_fact → workflow execute_function_node commit_fact → usermodel.fact.append` 是实际父子链；Agent/Graph/节点的 instrumentation scope 为 `trpc.agent.go`；单行 JSON 阶段终态及进程指标同时可见，无用户/事件 ID 指标标签 |

状态：worker组件`LOCAL_VERIFIED`；RTW签发主体与EventSpec、真实DC H04/H09.a端到端、正式进程生命周期、OTLP Collector→DC下钻仍`NOT_VERIFIED`。WS08-A/H08/H09/OBS完整任务保持`PARTIAL`，不得标`ACCEPTED`。
