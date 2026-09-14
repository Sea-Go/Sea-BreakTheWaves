# WS06-A 索引 Graph/Runner 与技术 worker 局部验收

状态：**PARTIAL / LOCAL_VERIFIED**。本分支交付 `content.build.v1` 的局部 IndexWorker 与真正的 tRPC-Agent-Go v1.8.1 GraphAgent/Runner 接线；不等于真实三路同代模型/后端联调、实际 `cmd/worker` 进程装配、RTW `AcceptBuild` 或 H06/OBS-r3 验收。

## 固定合同和职责

- DC 技术任务 `content.build.v1` 的输入是 `build_id`、`release_id`、`generation`、`input_manifest_hash` 和可选 `resume_indexes`。DC 任务自己的 `operation_id` 用于技术调度与日志；已准备内容的稳定 `BuildInput.OperationID` 从 content PostgreSQL 读取，不被新的 DC job 覆盖。DC `InputHash` 仍是 Submit 信封哈希，不误当作 RTW ReleaseManifest 哈希。
- Worker 从 DC 领 `AttemptID/LeaseEpoch/CancelVersion/ExpiresAt`，校验 content 账本与 RTW 固定 build，再向 RTW 领取**同一** fence。只有 RTW 返回完全匹配的 build/lease 才执行 Graph。
- `content_index` GraphAgent 用公开 `agent.MergeRuntimeState` 注入每轮请求；函数节点借用 `IndexCoordinator.Index`，不自建 Runner/Graph/Tool 框架。现有 `runtime.New` 负责原生 Runner、Event 所有权和会话生命周期，进程安装的 `telemetry.Bundle` 负责同 Trace 的结构化 JSON、OTel Span、Prometheus 指标。业务节点、Framework Agent/Graph/节点 Span 在同一父 Trace 中，不以手写 `fmt` 充当观测链路。
- Graph 只在协调器返回本地 `READY` 和三路 `LaneRef` 后发出有界 `IndexGraphReceipt`。Worker 消费所有 Graph 与 Runner Event 到 EOF，任何终态错误、重复/缺少 completion 都拒绝成功。随后回读 PG 固定 `READY`、IndexManifest 工件、三路 Ref/Probe 标志与当前 RTW claim，才向 DC 提交本技术 job 的 `succeeded + sha256 Ref`。该 ACK **不调用** RTW `AcceptBuild`，更不切人工发布指针。
- 已提交本地 READY 后若 DC 回执丢失，同 attempt 先读 DC 原 job 的确定回执；新的 DC attempt 只可复核已有不可变 READY，同一结果 Ref 可 ACK 新技术 job，不能重写历史 READY/outbox。跨 attempt 路径依赖集成树 `IndexCoordinator` 的权威修复；本分支只做 worker 的回读和不可变检查，不在此处改 content Store。
- Graph 失败、取消或旧 fence 不产生 READY Graph 回执；租约仍有效且父请求未取消时，Worker 用同一 DC fence 报告失败，不捏造成功。投影失败返回的 `ResumeIndexes` 需由调度生产者另行持久化并重投；目前的 DC 技术 job 是不可变输入，本 worker 不声称已完成该恢复自动化。

## 已验证与未验证

| 证据 | 结果与边界 |
| --- | --- |
| `go test -mod=readonly -race -count=1 -run 'Test(IndexGraph\|IndexWorker\|DecodeIndex)' ./internal/content ./internal/app` | 通过。Graph 正常/错误/取消、原生 Agent→Graph→节点同 Trace Span、原始 operation ID 分离、RTW 错 fence、Store 错代、缺少 Probe、技术失败不附 Ref、遗失 ACK、新 attempt 只读重放均由隔离替身覆盖；局部 worker 用同一 Bundle 验证框架指标、业务指标与终态 JSON 关联字段。 |
| `GOFLAGS='-run=TestIndexGraph' bash scripts/test-content.sh` | 通过。临时 PostgreSQL 16 下真实 `IndexCoordinator` 经框架 Graph/Runner 调三路测试索引器，固定同代/同 fence 的 PG READY 和 Result Ref 与 Graph receipt 相同；三路数值表示与查询探针仍是合成 fixture。 |
| `go test -mod=readonly -race -count=1 ./...`、`go vet ./...`、`go mod verify` | 均 exit 0；全包 race 包含 `cmd/worker`、content、app、三路 retrieval、runtime、search、usermodel 等本模块包。 |
| `bash scripts/test-content.sh` | exit 0；临时 PostgreSQL 16 上 content+artifacts 完整 race/vet 用例通过。 |

当前缺口：`cmd/worker` 仅装配 prepare job，不轮询 `content.build.v1`，真实 Dense/Sparse/Multi-vector Service 未在一个进程内与 DC BGE-M3、各自独立引擎完成固定代联调；独立 RTW `AcceptBuild`/人工发布、真实 Collector→DataCenter 下钻、容量和成本验收也未进行。因此 WS06-A/H06/H12/OBS-r3 继续 **PARTIAL/NOT_VERIFIED**。下一交接由入口 owner 在 `cmd/worker` 装配 IndexWorker 与三个真实 Service，随后用隔离 PG、真实 DC/RTW 进程和 Collector 完整验收。
