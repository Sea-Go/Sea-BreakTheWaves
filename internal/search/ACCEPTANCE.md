# WS06-E 局部验收记录

日期：2026-09-14。输入：Sea-Docs《搜索系统设计方案》《任务交接契约与验收清单》，BTW 根模块基线 `418840f`，tRPC-Agent-Go v1.8.1。结果：**确定性搜索执行合同 LOCAL_VERIFIED；WS06-E 整体 PARTIAL；H07 未接纳**。

| 验证点 | 实际证据 | 结论 |
| --- | --- | --- |
| 三路独立召回、固定版本与有效集合 | `TestThreeIndependentLanesAndRRF`、`TestRejectWrongLaneVersionAndInvalidSnapshotRef`：分别传独立 index，错 release/ref/revision 不进融合 | 通过，合成检索器 |
| 片段 RRF | Dense/Sparse/Multi-vector 原始分数与 rank 留存；`2/61` 和 `1/62+1/61` 手算对照，同名不同修订及来源键不合并 | 通过，合成检索器 |
| 六种执行配置 | `TestSixProfileLimitsChangeActualExecution`：fast low/medium/high 的每路调用分别 1/2/3 次，均只一批；detailed low/medium/high 分别 2/3/4 批，TopK 亦按配置实际传递 | 通过，测试策略非线上 D12 值 |
| 降级、部分/无结果、取消、预算 | 缺档拒绝，只有显式允许才降级；单路失败需部分授权；无有效修订不查询；取消不调用；超出子查询预算在三路前拒绝 | 通过，合成检索器 |
| 有效状态与引用 | Checker 入选及返回前再次检查；正常及错误返回的 Candidate/VerifiedCandidate 均清空索引文本；未连 RTW 权威状态/同版原文和 quote hash | 局部通过，非 EvidencePack |
| tRPC-Agent-Go 搜索流程 | 已核对 v1.8.1 API；未装配正式 Search GraphAgent/Runner | 未验收 C17 搜索路径 |
| OBS-2026-09-14-r2 | 无本包共享 Bundle 实际日志、Trace/metrics、Collector 查询 | `NOT_IMPLEMENTED` |
| H07 12 组合 | WS06-F 已做固定 fixture 的两种交付；真实三路+RTW联调未跑 | 未验收 |

本包不修改根 `go.mod/go.sum`。在此隔离工作树实际运行 `go test -race ./internal/search -count=1`（`ok`）、`go vet ./internal/search`（退出码 0）、`git diff --check`（退出码 0）。外部引擎和真实模型的单路验收属于各 `internal/retrieval/*/ACCEPTANCE.md`，不能据此推断这条组合路径已实联。

## WS06-F 证据交付局部验收

本切片基于 `1684d6665dfbf5669dced90aba01310a416f1f71` 独立工作树，仅新增 `internal/search/{evidence,summary,tools}.go`、`delivery_test.go` 并更新本目录说明。对照 Sea-Docs《搜索系统设计方案》第 4.1、4.2、5.7–5.9 节与 H07/WS06-F：**局部 fixture 验证通过；WS06-F/H07 整体 PARTIAL，C17 正式搜索运行链未接纳**。

| 验证点 | 实际证据 | 结论 |
| --- | --- | --- |
| 同版原文与引用前置 | `TestEvidenceRequiresSameRevisionAndDurableReceipt`：RTW 替身返回的修订、对象 hash、locator、quote hash 逐项核对；错版/错定位/错原文/错对象均在收据前拒绝；收据 search_id/pack_hash/durable_ref 不匹配时无 EvidencePack 外泄 | fixture 通过，正式 RTW API 未接 |
| Graph 接入防漂移 | `TestDeliveryRejectsGraphAdapterScopeDriftBeforeRTWRead`：注入的执行器即使返回新 release、索引文本或失效修订，也在 RTW 读取前被拒绝 | fixture 通过，正式 Search GraphAgent 仍未接 |
| 无证据、局部不可读、取消和失败成本 | `TestEvidenceEmptyPartialAndCancellation`、`TestFailedSourceReadsStillSpendReadBudget`：无候选不调用接纳；显式允许部分时保留可读证据和 gap；取消不交付引用；失败读取仍计次且达到上限停下 | fixture 通过 |
| Tools、预算与续读 | `TestTypedToolsShareSnapshotAndCumulativeBudget`、`TestToolReservationRefundsActualUsageForFollowupRead`：三个原生 typed FunctionTool 的名字与结构化结果、同版续读、调用方改写返回值不影响保存证据、累计预算不刷新；成功搜索按实际读/quote 用量退还预留，后续读仍有界；错 search_id 和未实现的继续详搜显式拒绝 | fixture 通过；失败调用耗完整预留，仍非完整 H07 账本 |
| summary 与收据时序 | `TestSummaryRunsNativeLLMAgentRunnerAfterReceipt`：真正运行 tRPC-Agent-Go v1.8.1 `LLMAgent→Runner`；模型请求 `Tools` 空，收据先于模型调用；只在完整终态和 citation ID 校验后返回答案；错引用失败仍保留搜索结果；无证据不调用模型 | fixture 通过；正式 DC 模型、RTW answer_id/历史未接 |
| 调用方 Tool 框架路径 | 同一测试再运行调用方 `Runner→LLMAgent→search_fast FunctionTool`，模型第二次调用收到结构化证据 Tool 消息；真实框架 `invoke_agent search_caller`、`execute_tool search_fast` 共用 TraceID，`chat` 同一调用下，Prometheus 有框架原生 Agent/Chat/Tool 指标 | fixture 通过，不等于正式 Search GraphAgent |
| 12 组合 | 同一测试逐个运行 fast/detailed × low/medium/high 的 summary 与 typed Tool 各六个 fixture；每个核对请求/实际 profile、引用收据与状态 | 局部 12/12；模型规划、高智能语义、真实三路与客户端未验收 |
| 反馈、词典 | 尚无真正展示/负反馈事实收据，也没有 WS07 查询统计/词典候选审核数据 | `NOT_IMPLEMENTED`；未点击候选不得作负例，共点击不得自动作同义词 |
| OBS-2026-09-14-r2 / C17 | 框架原生 Summary Agent、调用方 Tool Trace/指标已测；证据读取/接纳和确定性搜索无共享业务 stage 观测，搜索+总结尚未同根 GraphAgent，Collector→DataCenter 下钻缺失 | `PARTIAL`；C17 未接纳 |

当前运行 `go test -race -count=1 ./internal/search`（通过）、`go vet ./internal/search`（通过）、`go test ./...`（根模块全包通过）和 `git diff --check`（通过）。这些命令证明本地 fixture 与现有根模块未回归；RTW `SourceReader/CitationAcceptor` 仍是假实现，不表示 RTW 已耐久保存引用或公开流可恢复。
