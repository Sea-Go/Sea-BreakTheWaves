# WS06-E 局部验收记录

日期：2026-09-14。输入：Sea-Docs《搜索系统设计方案》《任务交接契约与验收清单》，BTW 根模块基线 `418840f`，tRPC-Agent-Go v1.8.1。结果：**确定性搜索执行合同 LOCAL_VERIFIED；WS06-E 整体 PARTIAL；H07 未接纳**。

| 验证点 | 实际证据 | 结论 |
| --- | --- | --- |
| 三路独立召回、固定版本与有效集合 | `TestThreeIndependentLanesAndRRF`、`TestRejectWrongLaneVersionAndInvalidSnapshotRef`：分别传独立 index，错 release/ref/revision 不进融合 | 通过，合成检索器 |
| 片段 RRF | Dense/Sparse/Multi-vector 原始分数与 rank 留存；`2/61` 和 `1/62+1/61` 手算对照，同名不同修订及来源键不合并 | 通过，合成检索器 |
| 六种执行配置 | `TestSixProfileLimitsChangeActualExecution`：fast low/medium/high 的每路调用分别 1/2/3 次，均只一批；detailed low/medium/high 分别 2/3/4 批，TopK 亦按配置实际传递 | 通过，测试策略非线上 D12 值 |
| 降级、部分/无结果、取消、预算 | 缺档拒绝，只有显式允许才降级；单路失败需部分授权；无有效修订不查询；取消不调用；超出子查询预算在三路前拒绝 | 通过，合成检索器 |
| 有效状态与引用 | Checker 入选及返回前再次检查；未连 RTW 权威状态/同版原文和 quote hash | 局部通过，非 EvidencePack |
| tRPC-Agent-Go 搜索流程 | 已核对 v1.8.1 API；未装配正式 Search GraphAgent/Runner | 未验收 C17 搜索路径 |
| OBS-2026-09-14-r2 | 无本包共享 Bundle 实际日志、Trace/metrics、Collector 查询 | `NOT_IMPLEMENTED` |
| H07 12 组合 | WS06-F summary/tools 未完成；真实三路+RTW联调未跑 | 未验收 |

本包不修改根 `go.mod/go.sum`。在此隔离工作树实际运行 `go test -race ./internal/search -count=1`（`ok`）、`go vet ./internal/search`（退出码 0）、`git diff --check`（退出码 0）。外部引擎和真实模型的单路验收属于各 `internal/retrieval/*/ACCEPTANCE.md`，不能据此推断这条组合路径已实联。
