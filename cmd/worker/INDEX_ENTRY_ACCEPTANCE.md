# WS06-A：`content.build.v1` 本地索引 Worker 入口验收

状态：**PARTIAL，本地进程入口切片通过**。本次在独立开发工作树接通 `cmd/worker` 的显式索引任务模式。它装配既有 `IndexWorker`、项目 Runtime、tRPC-Agent-Go v1.8.1 `GraphAgent`、`IndexCoordinator`、Dense/Sparse/Multi-vector 三路**实际 local-exact Service**、DataCenter typed representation 客户端、RTW 固定版本客户端与本地 PostgreSQL/内容寻址工件。默认准备模式保持兼容。索引技术任务的 DC 成功回执不等于 RTW `AcceptBuild` 或发布指针变更。

## 固定契约与生命周期

- `BTW_JOB_TYPE=content.build.v1` 必须同时指定 `BTW_INDEX_BACKEND=exact` 和 `BTW_INDEX_CONFIG_FILE`。三路配置在启动时严格解码、分别由 lane 构造器验证；运行时协调器再按 RTW 不可变 Release 对象核对每路 profile。未知任务或未装配的 Milvus 模式启动即拒绝。
- 进程只 Claim 指定的一种 DC job。`IndexWorker` 对同一 build 验证 DC 请求、内容账本、RTW Build 及 attempt/epoch/cancel/lease，Graph 使用 `agent.MergeRuntimeState` 传固定请求；Runner 消费到完成事件，回读本地 PG `READY` 与索引工件后才回 DC 技术 ACK。RTW 的正式 `READY` 仍由其 `AcceptBuild` 单独决定。
- 三路索引在各自 `Build` 中实际调用 DataCenter typed representation；`Reconciler.Ready` 回读不可变原文与 chunk、检查三路覆盖，并调用各路 `VerifyAndProbe` 后用 fenced PG 事务提交本地 READY。失败、租约过期或探针缺失不会由进程装配补造成功结果。
- 父进程拥有 Runner/框架 PostgreSQL Session、内容 pool、HTTP transports、Metrics server 与 telemetry Bundle，SIGTERM 后按依赖反序关闭。框架 Agent/Graph 原生 Span 和指标来自安装后的 Bundle；HTTP 子调用传递 W3C `traceparent`，业务终态由结构化日志记录。

## 已执行验证

| 门禁 | 结果与直接证据 |
| --- | --- |
| `bash cmd/worker/acceptance.sh` | **PASS**。脚本建立专属临时 PostgreSQL 16 content/sessions 数据库，用 `go test -race -count=1 -v ./cmd/worker` 构建并运行准备、索引两个真实子进程，随后 `go vet ./cmd/worker`、`git diff --check` 通过。 |
| `TestActualIndexWorkerBuildsThreeLanes` | **PASS**。从固定 RTW Release、已准备 chunk 与旧本地 fence 出发，索引任务换新 DC/RTW attempt；三路真实 exact `Build` 与独立探针共调用 DC typed representation 9 次。写出三路同 build/generation/profile 的数值工件及 `IndexManifest`，本地 PG `READY` 与 DC 技术 ACK 引用同一 SHA256。RTW fixture 仍为 `BUILDING`，没有 `AcceptBuild` 调用。 |
| tRPC 原生与跨边界观测 | **PASS，隔离 OTLP fixture**。子进程导出 `trpc.agent.go:invoke_agent content_index`，与 RTW HTTP `traceparent` 的 TraceID 一致；`/metrics` 有 `trpc_agent_go_agent_` 系列，业务 JSON 有 `content.worker.index.finished`、`content.index_build.finished`、停止事件。 |
| `TestReadIndexSettingsRejectsUnknownAndTrailingFields`、`TestBuildJobNeedsExplicitRealLaneSettings` | **PASS**。未知/尾随/过大 JSON、缺失模式配置、未支持的 Milvus 模式被拒绝；准备模式原测试继续通过。 |
| `go test -race ./...` | **PASS**，根 Go 模块所有包；不代表独立 Go 子模块或真实外部后端已测。 |

HTTP DataCenter/RTW 与 OTLP 接收端在进程测试中是**隔离协议 fixture**，其中模型返回两段文本的确定性数值，用于证明真实 lane 编码、持久化和探针调用链，不能称为真实 BGE-M3 推理或真实服务联调。PG 和两个 worker 进程是真实实例；固定 Release 和准备好的 chunk 由测试显式种入独立库，未声称在同一测试内经过 RTW `AcceptBuild`。

## 仍待交接与 H06 门禁

1. 索引进程目前只装配本地 exact 后端。三路正式 Milvus/HNSW/倒排/token-row 实例的配置、生命周期与真实 DC BGE-M3 同代联验，需要独立补齐，不能把本地 exact 结果作为生产检索吞吐或质量证明。
2. RTW `AcceptBuild`、发布指针、失效状态传播、真实 RTW/DC HTTP 服务、Collector/Loki/Tempo 查询、规模/故障恢复和生产对象存储未在本切片完成。RTW 回执由产品权威服务负责，不能在 DC worker 中用一次技术 ACK 代签。
3. `IndexCoordinator` 失败返回的完整 `ResumeIndexes` 提示目前没有跨 job 持久交接通道；失败重试仍可从已登记的成功 lane 恢复，但投影失败的未登记编码可能重算。后续任务需让 DC job 输入或持久账本承载该提示并核对新 fence。

因此 **WS06-A 的本地 Worker 接线达到 LOCAL_VERIFIED，H06 仍为 PARTIAL，OBS-r3 不因本进程测试被宣告通过**。脚本在退出时打印可复核的本地 Evidence directory；该目录是隔离临时产物，不纳入 Git。
