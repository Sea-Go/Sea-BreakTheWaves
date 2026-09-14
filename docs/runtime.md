# 共享运行与提供者客户端

本实现承接 WS06-G 的运行、会话和 H03/H04/H05 客户端基础；完整 WS06-G 仍未验收。模型响应、技术任务完成、内容接纳和产品发布保持独立。

## 工作区域

- [W0:ROOT] `Sea-BreakTheWaves` 的 `feat/runtime-content-20260914` 隔离工作树，起点 `b4aa246`。
- [W1:WRITE] 根 `go.mod/go.sum`、`internal/runtime/`、`internal/clients/datacenter/`、`internal/clients/ridethewind/`、本文。
- [R1:READ_ONLY] Docs 任务契约、DC 提供者公开 contracts、RTW `api/knowledge.api`；content/corpus/artifacts 由集成 writer 维护，客户端集成测试通过公开入口消费。
- [D1:DEPENDENCY] 固定版本 tRPC-Agent-Go 核心与 PostgreSQL session 子模块源码。
- [G1:GENERATED] SDK 类型快照由提供者固定提交生成；不手改。
- [X1:EXTERNAL] 任务专用、回环地址隔离 PostgreSQL/HTTP 可写；验收后结束进程。生产不在范围。
- [N1:OUT_OF_SCOPE] 存量 recommendation、agent_v2/v3 和其他原工作树。
- [T1:TEMP] 系统任务临时目录，存放验收服务、数据库与日志。

主职责 [C6:INFRA]；跨区 [C5:AGENT_ADAPTER] tRPC Runner/Session/Model，[C7:CONTRACT] 提供者 HTTP 与固定生成类型，[C8:VERIFY] 运行、取消、恢复证据。客户端不维护第二套产品发布或领域接纳状态。

## 版本与装配

根模块为 `github.com/Sea-Go/Sea-BreakTheWaves`，Go `1.25.5`；复用存量 tRPC-Agent-Go `v1.8.1`。PostgreSQL session 独立模块没有 `v1.8.1` 标签，锁定同系列 `v1.8.0`，实际依赖的 storage/postgres 为 `v0.8.0`。核心 `Runner`、`Event.Clone`、`Plugin.OnEvent`、`model.Model` 和 PostgreSQL `NewService`/Options 均按锁定源码与 `go doc` 核对；未升级 latest，未复制框架 internal。

根模块是最终工程的装配入口；存量 recommendation、agent_v2/v3 子模块暂保留自身边界。普通领域包使用同一根模块，不额外创建子 module。

`runtime.New(app, agent, sessions, observed)` 借用 session.Service，强制接收已安装的进程观测 Bundle；`OpenPostgres(app, agent, cfg, observed)` 创建并拥有框架 PostgreSQL 服务，明确 DSN/schema/table prefix，采用同步事件持久化。`Initialize` 仅显式初始化框架表，schema 须提前建立；正常启动使用已初始化的 schema。DDL 的权威来源是固定版本官方 session/postgres 模块，不在 BTW 再维护一份 session SQL。

运行请求使用完整 `SubjectRef(authority_id, tenant_id, subject_id)`。框架 user key 是该结构的确定性哈希，session ID 与 run ID 显式传入。run ID 通过 `agent.WithRequestID` 传到事件；同一活跃 run ID 不接受第二次调用。

`Run` 使用真实 Runner，原样传递增量、完整模型响应、工具响应及终态；普通模型 Done 不结束整轮。持续消费到通道关闭，缺少 completion、重复 completion、启动失败、终态错误、会话写入失败均返回错误。Sink 错误和取消向下传播，并停止展示后继续排空框架通道，再释放运行资源。`Close` 取消本实例的活跃请求，等待消费者退出，再关闭自有服务；借用服务仍由调用者关闭。

会话恢复是读取已持久化历史继续新一轮，不是重放旧工具副作用，也不等于跨进程恢复到任意一条工具指令。业务写入幂等、任务重领和崩溃后的结果对账由领域合同负责；runtime 不提供第二份产品答案资源或持久化业务幂等表。

## 模型与错误边界

`NewModel` 使用框架 OpenAI-compatible 模型，地址为给定 DC 服务根地址下的 `/v1`，model 字段使用 callpoint。Token 明确传入，关闭 SDK 自动重试；调用者决定可重试范围。强类型 Dense/Sparse/token-matrix 编码走独立 `datacenter.Client.Represent`，不塞进 Chat Completions。

锁定 SDK 对没有 finish_reason 的干净流 EOF 仍可能生成普通 final。最窄 `model.Model` 适配保留原始流和错误，仅将缺失终态原因的 final 标为 stream error；未重写 Agent/Tool 循环。

真实 `-race` 验收先发现两个框架对象共享问题，均通过公开边界解决：

1. `Runner.ensureErrorEventContent` 会就地补错误内容，同时 llmflow 仍可读取同一响应。Runner 级 `Plugin.OnEvent` 对错误事件执行公开 `Event.Clone`，让 Runner 拥有自己的修复对象。没有改写模型错误消息，也没有依赖特定 Agent 实现。
2. 下一轮 `ContentRequestProcessor.mergeFunctionResponseEvents` 会重排历史工具 Choices。session.Service 适配在 `AppendEvent` 前执行公开 `Event.Clone`，将会话持有对象与对外流分开；后续存储仍完全由官方 PostgreSQL 服务完成。

同一 session 仍不承诺多个并发写运行的业务顺序；产品入口应以它自己的会话/任务合同约束并发。框架会话并不承担产品版本 CAS。

## 提供者契约与 SDK

| 客户端 | 固定权威 | 消费范围 |
| --- | --- | --- |
| DataCenter | `317ce623904dc153ef36ad96a8a437fabb09a9f3` 的公开 `contracts/representation`、`contracts/eventing`、`contracts/jobs` | 三种表示、事件收据/固定批次/确认/水位、任务提交/读取/claim/续租/完成/取消/停机确认 |
| RideTheWind | `627c1c9bb63088f59fb9429160158ba6ba2bc46d` 的 `api/knowledge.api` 与 goctl 输出 | 内部 worker 的固定 revision/release/build/compile 读取及 claim/results |

DC `source.lock.json` 记录公开文件的 SHA256，生成类型及表示校验保留权威实现。RTW 生成器只选 worker DTO，path 字段生成 `json:"-"`，optional 标签转换为 Go JSON 的 omitempty。生产调用不 import 相邻仓库 internal，不借用其数据库。

从固定提交重生：

```bash
python3 internal/clients/datacenter/generate.py "$SEA_DC_ROOT" 317ce623
python3 internal/clients/ridethewind/generate.py "$SEA_RTW_ROOT" 627c1c9
```

生成器通过 `git show` 读取提交中的源文件，忽略提供者未提交改动。版本升级先重生、核对合同，再跑消费者测试；不得修改快照掩盖提供者差异。

HTTP 共享层保留 context、状态码及 Retry-After，不自动重试写操作；固定提供者不跟随重定向。响应有大小边界，204 明确映射为无任务，409 保留为冲突，错误不转成空成功。表示响应由公开 Validate 校验完整 configuration/space/role/model/shape。RTW claim/results 额外核对 identity、generation、attempt、lease epoch、cancel version、input hash、状态及产物引用；这些是接收回执对照，不是客户端领域状态机。

## 实际验收

基础提交：`7d05187`。2026-09-14 的 r4 验收使用 PostgreSQL 16、DataCenter `317ce623`、RideTheWind `52e0bd4e70e1db99e35fed15075d6bc4c9fa777f`。RTW 服务的新读面版本与固定 `627c1c9` worker SDK 兼容。

```bash
SEA_DC_ROOT=/path/to/fixed-datacenter \
SEA_RTW_ROOT=/path/to/fixed-ridethewind \
SEA_PG_BIN=/path/to/postgresql/bin \
bash internal/runtime/acceptance.sh
```

脚本创建独立 PostgreSQL cluster 与 dc/knowledge/sessions/content 数据库，启动两端实际入口，执行 `go test -race -count=1 -v ./internal/runtime/... ./internal/clients/...`、`go vet` 和 diff 检查，再停止任务进程；每次打印原始证据目录。

| 范围 | 已检查的行为 | 结果 |
| --- | --- | --- |
| 真实 Runner + HTTP 模型 fixture | 普通 SSE 增量、完整响应、API 429、不完整 EOF、取消、Sink 错误、错误后 completion、重复/缺失终态 | 通过；增量未吞掉或与完整消息重复拼接，API 错误未自动重试 |
| PostgreSQL 会话 | 两个独立 OS 进程分别建立框架服务，第二进程收到已保存的第一轮用户消息、工具结果与答案 | 通过；6 条事件持久化，FunctionTool 的 fixture 副作用总计 1 次 |
| 持久化失败 | 用户输入写入成功后，后续 Assistant 事件 AppendEvent 失败 | 通过；运行返回保存失败，没有用 completion 覆盖错误 |
| DC 实际平台 HTTP | 事件接纳/重放/冲突/读取/批次确认/连续水位；任务 submit/claim/renew/complete 重放/无任务/cancel/ack | 通过；精确大值按提供者规则编码为字符串 |
| RTW 实际 worker HTTP | 固定正文/发布输入/构建/编制读取、执行权与失败结果回执、错误 lease 拒收 | 通过；fixture 明确以 no-model 失败结束，不虚构索引就绪 |
| DC → RTW → content | DC 真实 claim 的 attempt/epoch/expiry传入 RTW，Preparer 消费实际固定源和对象，分块写 content PG，同输入重放 | 通过；1 个 chunk，固定清单 hash 可回读，状态仍 BUILDING |
| H05 三种表示 | 消费 DC 权威 fixture，固定模型/空间/角色；错空间拒收 | 通过，仅 HTTP fixture 消费；未宣称真实模型效果或 DC 完整模型配置链验证 |
| SDK 故障边界 | 错回执的 generation/attempt/lease/cancel/hash/state/expiry，HTTP 409、取消、重定向、超大响应 | 通过 |

原始 r1 失败与 r2–r4 修正记录保存在任务证据目录；最终结构化摘要见 [`acceptance-r4.json`](../internal/runtime/acceptance-r4.json)。初次 fixture 的 DC 精确大整数需 string、RTW 创建响应仅 metadata 与 worker 读取正文的差异均已按提供者修正，没有修改权威服务。

## 未完成项

- 经真实 DataCenter 模型配置、真实 Dense/Sparse/Multi-vector 或聊天 Provider 的完整调用和效果验收；当前模型与表示数值是明确 fixture。
- RTW 首个公开引用前的持久收据、产品 SSE 恢复接口、首引到完整流的跨服务 trace。
- 多实例运行取消、任意中断点继续执行与业务副作用对账；当前证明的是完成一轮后 PostgreSQL 历史跨进程延续。
- 实际三路索引构建与 READY、答案资源、产品发布及生产部署。这些仍由各领域推进，不能从本 SDK 或会话验收外推。

H05兼容更新：提供者4f5abf5显式增加mean_maxsim；SDK已从此提交重生，新增mean fixture也经过真实HTTP消费者测试。旧sum_maxsim不换算，contract/space保持独立。此前r4跨进程平台验收仍是原固定提交的历史证据，新增数学枚举按受影响的表示消费范围复验。

## OBS-2026-09-14-r2：框架原生链路与局部验收边界

新根模块 `internal/telemetry` 提供单进程Bundle：明确服务/环境/构建版本/实例、同步JSON writer、OTel exporter及固定采样率、独立Prometheus registry；缺少配置时拒绝装配。`InstallGlobals` 必须先于Runner或worker协程调用一次，使用tRPC-Agent-Go v1.8.1和A2A公开Logger入口适配同一sink，并覆盖框架默认忽略Context的函数。未携带Context的框架日志仅为进程级记录，不伪造trace字段。

本轮审计发现此前仅调用 `otel.SetTracerProvider`，框架 v1.8.1 的 `telemetry/trace.Tracer` 仍停留在独立的默认 no-op；因此原 `runtime.run` 是应用自建外层 Span，不能证明 Agent/Tool 原生观测。现通过框架公开 `telemetry/trace.TracerProvider`、`Tracer` 复用同一 Bundle Provider，不调用框架 `trace.Start` 再创建第二套Exporter。框架 `telemetry/metric` 原先也保留默认 no-op，现通过其公开 `InitMeterProvider` 在Runner运行前装配同一个 OTel MeterProvider，使用官方兼容的 Prometheus exporter v0.56.0 把原生 Agent/Model/Tool 指标送到Bundle既有 `/metrics`。SDK View 按框架meter scope分开同名仪表，仅保留框架固定operation、token type和stream布尔值；模型响应名、Agent/Tool名、用户、会话及Agent实例ID均不进入时间序列。关闭时Bundle同时关闭Trace和Meter Provider。

`Runtime.New`/`OpenPostgres` 现在强制接收已安装Bundle。一次Run产生带run/session ID的 `runtime.run.started/finished`、真实Span及请求级成功/失败/取消/超时计数和耗时；OOM或panic不会被写成成功。关闭时Bundle拒绝新阶段并等待在途阶段，再有界关闭Exporter；写日志失败增加有界指标，不递归打印。调用者仍须按Runner→Bundle顺序关闭；Domain content的Preparer/Reconciler同样强制接收Bundle，在真实PG提交结果之后写含build/attempt/epoch及固定工件hash的终态。

此前局部证据：`go test -race -count=1 ./internal/telemetry ./internal/runtime ./internal/content` 用实际JSON writer、有效trace/span ID、Exporter接收、应用Prometheus `/metrics`、写入失败、关闭竞争、panic拒收及PG知识构建测试核验；任务脚本 `scripts/test-content.sh` 与 `internal/runtime/acceptance.sh` 已分别通过随机端口隔离PG，后者仍只证明原有DC/RTW业务交接和Bundle调用兼容。测试中的内存Exporter/Discard模式不构成Collector或跨仓Trace验收。

新增框架证据：`internal/runtime/framework_trace_test.go` 在独立子进程实际执行 `Runner→LLMAgent→typed FunctionTool`，内存Exporter收到 `runtime.run` 及同Trace父子链上的框架 `invoke_agent`、`chat`、`execute_tool` Span，后三者 instrumentation scope 为 `trpc.agent.go`。同一次调用抓取真实 `/metrics`，同时看到应用与框架三个meter的指标，返回HTTP 200且没有用户/会话/Agent实例ID标签；普通与race测试均通过。此测试证明**隔离Runtime组件**使用了框架原生观测，未证明内容、搜索、推荐生产入口已采用框架编排。

固定源码提交 `7f5d6b9cad5cd0c85dbca2024fe5ce3fa6b33b5f` 复验：根模块 `go test -mod=readonly -race -count=1 ./...`、`go vet ./...`、`go mod verify` 均通过。专门的真实框架测试记录原生 Agent/Chat/Tool Span 数分别为1/2/1，且为同一Trace；同一抓取中的三类框架请求计数分别为1/2/1。上游模型响应名在fixture两次调用中故意不同，`/metrics` 不出现它们或其他请求级ID。原始测试输出位于任务交付区 `/Users/edy/Sea/Deliverables/2026-09-14/BTW框架原生观测验收.log`，SHA-256 为 `3785ea849b6fe5c6311d40ce0eb9f5eb6091dc3a7798b314ab673b6f9406928c`；该输出是隔离Runtime证明，不是生产Collector或业务worker日志。

当前 `observability_status=LOCAL_VERIFIED` 只授予公共日志桥、隔离Runtime的框架Agent/Tool调用树及内容**业务用例**各自的本地切片；不授予BTW整体。OBS-01尚无完整worker/API进程入口，OBS-03没有内容/搜索/推荐实际GraphAgent与Tool装配，OBS-04缺DC/RTW实际traceparent与异步Link，OBS-07缺Collector/DC下钻。内容Preparer/Reconciler仍由普通Go用例直接调用，自建Bundle Stage只表达领域提交阶段，不能代替框架GraphAgent；三路检索lane、BGE serving和数仓/训练也尚未接此设施。C17云端框架业务链仍为NOT_IMPLEMENTED/NOT_VERIFIED，本结果不能标WS06-A/G或整体OBS为ACCEPTED。
