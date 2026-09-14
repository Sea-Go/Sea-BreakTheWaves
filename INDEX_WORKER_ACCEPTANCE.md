# WS06-A 索引 Graph/Runner 与技术 worker 局部验收

状态：**PARTIAL / LOCAL_VERIFIED**。本分支交付 `content.build.v1` 的 IndexWorker、真实本地进程三路 exact 索引验收及可扫描的 RTW `AcceptBuild` 交接账本；RTW 目前只经 HTTP 协议替身测试，未做真实 go-zero+隔离 PG 交接，也未完成正式 BGE/Milvus 和 H06/OBS-r3 验收。详见 [`internal/app/INDEX_DISPATCH_ACCEPTANCE.md`](internal/app/INDEX_DISPATCH_ACCEPTANCE.md)。

## 固定合同和职责

- DC 技术任务 `content.build.v1` 的输入是 `build_id`、`release_id`、`generation`、`input_manifest_hash` 和可选 `resume_indexes`。DC 任务自己的 `operation_id` 用于技术调度与日志；已准备内容的稳定 `BuildInput.OperationID` 从 content PostgreSQL 读取，不被新的 DC job 覆盖。DC `InputHash` 仍是 Submit 信封哈希，不误当作 RTW ReleaseManifest 哈希。
- Worker 从 DC 领 `AttemptID/LeaseEpoch/CancelVersion/ExpiresAt`，校验 content 账本与 RTW 固定 build，再向 RTW 领取**同一** fence。只有 RTW 返回完全匹配的 build/lease 才执行 Graph。
- `content_index` GraphAgent 用公开 `agent.MergeRuntimeState` 注入每轮请求；函数节点借用 `IndexCoordinator.Index`，不自建 Runner/Graph/Tool 框架。现有 `runtime.New` 负责原生 Runner、Event 所有权和会话生命周期，进程安装的 `telemetry.Bundle` 负责同 Trace 的结构化 JSON、OTel Span、Prometheus 指标。业务节点、Framework Agent/Graph/节点 Span 在同一父 Trace 中，不以手写 `fmt` 充当观测链路。
- Graph 只在协调器返回本地 `READY` 和三路 `LaneRef` 后发出有界 `IndexGraphReceipt`。Worker 消费所有 Graph 与 Runner Event 到 EOF，任何终态错误、重复/缺少 completion 都拒绝成功。随后回读 PG 固定 `READY`、IndexManifest 工件、三路 Ref/Probe 标志与当前 RTW claim，再消费 PG READY outbox：先确认 RTW `AcceptBuild(READY)` 同 fence/同 Ref，后提交 DC 本技术 job 的 `succeeded + sha256 Ref`，最后在 PG 标记 outbox delivered。该交接**不切人工发布指针**。
- 已提交本地 READY 后若 RTW 或 DC 回执丢失，扫描器按持久 dispatch 记录回读两个权威状态并幂等补交；**同一 DC job** 的新 attempt 只读复核不可变 READY 与 RTW 已接纳的精确 Ref，可 ACK 新技术 attempt，不能重建三路或重写历史 READY。过期 RTW claim 不被旧 fence 补交；DC attempt 耗尽转人工处理。不同 DC job 的 epoch 重置不能覆盖 RTW build 级全局 fence，需权威调度/RTW 分配合法更高 fence 或新 build/generation。
- Graph 失败、取消或旧 fence 不产生 READY Graph 回执；租约仍有效且父请求未取消时，Worker 用同一 DC fence 报告失败，不捏造成功。投影失败返回的 `ResumeIndexes` 需由调度生产者另行持久化并重投；目前的 DC 技术 job 是不可变输入，本 worker 不声称已完成该恢复自动化。

## 已验证与未验证

| 证据 | 结果与边界 |
| --- | --- |
| `go test -mod=readonly -race -count=1 -run 'Test(IndexWorker\|IndexDispatch\|DecodeIndex)' ./internal/app` | 通过。RTW 回执丢失/重启扫描、旧 RTW claim/终态冲突、DC attempts 耗尽、保留的旧接纳标记再验真及 Graph/Runner 同 Trace 均由局部替身覆盖。 |
| `bash scripts/test-content.sh` | 通过。临时 PostgreSQL 16 下 001→002 迁移重复执行、并发 `SKIP LOCKED` 领取、30 秒 lease/claim_epoch 旧消费者拒收、新 DC epoch 接管且不可变 READY 不被改写；content/artifacts 全包 race/vet 通过。 |
| `bash cmd/worker/acceptance.sh` | 通过。真实 worker 子进程+隔离 PG16+Graph/Runner+exact 三路和 probe；RTW/DC 是同进程 HTTP 协议替身，验证 RTW 同 fence/Ref `READY` 在 DC 技术 ACK 之前，最终 outbox delivered。此项不代表真实 RTW go-zero 服务验收。 |
| `GOFLAGS='-run=TestIndexGraph' bash scripts/test-content.sh` | 通过。临时 PostgreSQL 16 下真实 `IndexCoordinator` 经框架 Graph/Runner 调三路测试索引器，固定同代/同 fence 的 PG READY 和 Result Ref 与 Graph receipt 相同；三路数值表示与查询探针仍是合成 fixture。 |
| `go test -mod=readonly -race -count=1 ./...`、`go vet ./...`、`go mod verify` | 本分支最后改动后均通过；含 `cmd/worker`、content、app、runtime、三路 retrieval 等根模块包。 |

当前缺口：`cmd/worker` 已装配 index job，但真实 DC BGE-M3 与独立 Dense/Sparse/Multi-vector 引擎尚未固定同代联调；RTW `AcceptBuild` 尚未与真实 go-zero+隔离 PG/共享工件联验，人工发布、真实 Collector→DataCenter 下钻、容量与成本也未验收。因此 WS06-A/H06/H12/OBS-r3 继续 **PARTIAL/NOT_VERIFIED**。下一交接先补真实 RTW 接收的同 fence/Ref、回执丢失、过期 claim、撤回反例，再补正式三路后端和发布链。
