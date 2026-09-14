# WS06-A 同代三路索引协调器验收

状态：**PARTIAL**。本次只交付 `internal/content` 的确定性 `IndexCoordinator` 和隔离 PostgreSQL 契约测试；未交付真实三路模型/检索引擎在同一代的集成运行、`content.build.v1` 的 DC 技术作业 worker/GraphAgent/Runner 装配、RTW `AcceptBuild` 回执与人工发布链。因此 **H06、WS06-A 整体及 OBS-r2 仍未通过**。

## 输入、提交点与恢复

- 入口只收已存在的 `build_id`、DC/RTW 当前 `Fence` 与可选 `ResumeIndexes`。稳定 `BuildInput` 从 content PostgreSQL 读取，保持最初的 `OperationID`；后续独立技术 job 的 operation ID 不写入原始输入。
- 在外部建索引前核对 RTW 固定 build 的 module/release/generation/input hash/attempt/epoch/cancel version/lease expiry，以及固定 release 对象中恰好三路 profile、已准备 chunk manifest 的身份。`Store.Claim` 在新 epoch 下接管本地执行；已登记的 lane Ref 不重建、不能由冲突 resume hint 替换。
- Dense、Sparse、Multi-vector 由各自真实 Service 的 `Build` 签名与 `VerifyAndProbe` 签名接入；协调器只协调调用，不重写向量算法。成功 lane 的索引 Ref 先回读内容寻址对象，校验索引正文与本代/本 build/本 chunk/profile 后才 `RecordLane`。所有 Ref 均属于同一个 `BuildID` 和 `Generation`。
- 投影失败时完整编码索引可能以 `ResumeIndexes` 返回，但**不登记为成功 lane**；调用方必须先持久保存该提示，下一次尝试再传回。未持久化的提示丢失时重算，不伪称已恢复。已有 lane 仍可从本地账本恢复。
- 三路都登记后，再次核对 RTW claim，并交既有 `Reconciler.Ready`：它回读固定原文、覆盖、分片、数值表示并调用三路独立 `VerifyAndProbe`，最后由 PostgreSQL fence 事务写本地 `READY`、结果 Ref 与 outbox。单 lane、单 DC job 成功或生产者的探针标志均不产生 READY。RTW build 接纳与人工发布仍是独立后续动作。
- 整个 content 操作使用已安装的统一 telemetry Bundle 创建 `content.index_build` span/结构化终态及结果标签；当前并未给索引阶段装配 tRPC-Agent-Go Graph/Runner，不能称为完整框架观测链。

## 已执行验证

基线：`feat/content-index-coordinator-20260914` 从 `ac3cc4ca7b17af04f73c7dbea9c1db5eae79f61c` 建立，Go 模块锁定 `trpc.group/trpc-go/trpc-agent-go v1.8.1`；测试使用 `scripts/test-content.sh` 临时创建的独立 PostgreSQL 16 实例与每例独立 schema，不使用共享或生产数据库。

| 命令 | 结果 | 覆盖 |
| --- | --- | --- |
| `GOFLAGS='-run=TestIndexCoordinator' bash scripts/test-content.sh` | 通过，`go test -race -count=1 -v` 与 `go vet` 均 exit 0 | 三路 READY/重放、局部失败重试、投影 ResumeIndex、旧 lease/new epoch、伪造 generation、独立 probe 失败、未知/冲突 resume、缺失 indexer |
| `GOFLAGS='-skip=TestPrepareGraph' bash scripts/test-content.sh` | 通过，`go test -race -count=1 -v` 与 `go vet` 均 exit 0 | content 非 Graph 用例与 artifacts 全部用例，包括真实 PG ledger/reconcile |
| `bash scripts/test-content.sh` | **失败，整包 race 不通过** | 已有 Graph 取消测试后，下一用例初始化全局 metric provider 时出现竞态，见下方 |

整包 race 栈定位：写方为 tRPC-Agent-Go v1.8.1 `telemetry/metric/metric.go:225` 的 `initInvokeAgentMetrics`，经 `InitMeterProvider` → 本仓 `internal/telemetry/framework.go:41` 的 `Bundle.InstallGlobals` → `internal/content/telemetry_test.go:31` 的测试初始化；并发读方是此前 `TestPrepareGraphCancellationReachesPreparer` 触发的框架 Graph goroutine，位于 `agent/graphagent/graph_agent.go:213` → `internal/telemetry/metric_invoke_agent.go:154`。这是**本次整包验收的未解决失败**，没有跨 W1 修改既有 Graph/测试生命周期代码。局部 race 通过不能抵消该失败。

## 下一次交接与最终验收

1. 在 worker 装配真实 `dense.Service`、`sparse.Service`、`multivector.Service`、同一内容寻址存储与当前 DC/RTW fence；用 tRPC-Agent-Go v1.8.1 GraphAgent/Runner 消费完整运行事件及错误，提交技术 job 回执时只声明其自身阶段。
2. 运行真实 DC BGE-M3 与三套独立索引后端，在一个固定 Release/Generation 上回读 `IndexManifest`，核对三路 Profile/Artifact/Probe/coverage；再走 RTW `AcceptBuild`，确认本地 READY、RTW 接纳与人工发布指针各自独立。
3. 修复既有 Graph 取消/全局 metrics 初始化 race 后重跑**完整** `bash scripts/test-content.sh`；再跑真实 Collector 下钻、成本、容量、回滚与异常注入。未完成前维持 PARTIAL。
