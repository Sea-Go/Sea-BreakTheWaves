# WS06-E 搜索执行内核

工作区域：[W0:ROOT] 本隔离工作树；[W1:WRITE] `internal/search/`；[R1:READ_ONLY] `internal/retrieval/{dense,sparse,multivector}`、`internal/corpus`、`internal/telemetry`、Sea-Docs 与其他仓；[D1:DEPENDENCY] Go 模块缓存的 tRPC-Agent-Go v1.8.1；[G1:GENERATED] 无；[X1:EXTERNAL] 无实际写入；[N1:OUT_OF_SCOPE] 现有业务树、根依赖文件、正式 HTTP/worker、发布指针及 WS06-F；[T1:TEMP] Go 测试临时目录。主职责 [C2:APPLICATION]/[C3:DOMAIN]，跨 [C7:CONTRACT]/[C8:VERIFY]。本包不拥有客户端身份、RTW 发布、三路索引构建或模型服务。

`New` 借用三路只读 `Snapshot.Search`、查询 Planner 和有效修订 Checker。调用者从 RTW 权威发布状态取得同一 module/release/generation 的三个内容寻址索引，以及当前有效修订集合；本包固定拷贝后向三路分别传同一范围和期限。任何路返回错代、错索引、无效修订、重复/错位次、非有限分数、错片段，整路失败。空有效修订集合直接返回 `empty/no_evidence`，三路均标记 `requested + skipped`，不调用编码器。`Snapshot.PublicationRevision` 仅保留调用方固定指针的引用；本包未接 RTW 权威指针读取，不能由该字段自行证明发布或 READY。

融合键是 `source_kind + content_id + revision_id + chunk_id`。各路原始分数、从 1 开始的 rank 和 index ref 都保留；对同一查询使用三路等权 `1/(60+rank)`。详搜重复改写命中同一片段时保留来源列表，但每路仅取最佳 rank 的贡献，避免同义改写堆分。跨路同键而 chunk 内容不同则按合同冲突拒收。当前没有 rerank；`RRFScore` 是候选排序基线，不是相关性概率。

六档配置由调用方提供固定版本 `Policy`；构造时拷贝并验证每档为有界值，拒绝相邻等级完全相同的预算。`fast` 只能执行一批，批内可有有限子查询；`detailed` 可多批，每次规划前检查剩余子查询数，出现无新证据、无新查询、证据/批次上限或取消即停止。Planner 只能产出查询文本，不能改变快照、等级或预算。尚未冻结 D12 的真实容量值，测试中的六档数值仅是 fixture，不是线上产品默认配置。高等级的模型规划和覆盖判断尚未接入；真实高智能效果不能从预算差异推断。

不可用等级只有在 `AllowLowerIntelligence` 显式为真时才降到较低档，并返回 requested/effective 和原因；深度永不自动升级。单路失败只有 `AllowPartial` 为真且仍有可用路径时返回 `partial` 与独立 `lane_status`，三路全失败报错。取消优先返回 Context 错误。`complete` **只表示 fast 配置批次完成且有经有效状态检查的候选，不表示答案充分**；详搜未有覆盖判定，所以即使有候选也只返回 `partial`，不将 `no_new_evidence` 冒称充分完成。`empty` 是无有效候选，可能有降级原因；调用方须阅读 lane_status。

`VerifiedCandidate` 只通过当前有效状态的二次检查，并非 `EvidencePack`：其 `Chunk.Text` 被清空，不能拿索引里的片段当原文；还没有从 RTW 读取同版原文、校验 quote hash、定位和可引用性。WS06-F/RTW 接收方须在最终交付前执行同版原文读取与引用固定；不得将这里的 `verified_candidates` 直接传给 Summary LLMAgent 当成证据。当前只约束 wall time、批次、子查询、每路 TopK 和候选数；模型调用数/token、累计 Tools 账本、阅读/最终证据长度尚未接入，不可认为完整 H07 预算已实现。

框架选择：项目根 `go.mod` 固定 tRPC-Agent-Go v1.8.1，已核对公开 `graph.NewStateGraph`、`graphagent.New`、`runner.NewRunner`，以及框架 Knowledge VectorStore 不能表达学习型 Sparse 与 token MaxSim 的固定工件边界。三路表示继续使用已有领域包，确定性执行内核独立可测；正式 Search GraphAgent/Runner、模型 Planner、typed Tools 与 summary 均未在本切片装配。本包的 `for` 是当前确定性预算循环，不冒称符合 Docs 的最终 StateGraph 条件环或 C17 搜索运行时验收。

观测状态 `NOT_IMPLEMENTED`（OBS-2026-09-14-r2）：未注入进程唯一 Bundle、未产生本包统一结构日志/Span/指标，亦无 Collector→DataCenter 下钻。返回状态/耗时字段和测试输出不算 OBS 接纳。最终集成须在 Search GraphAgent/Runner 外层及每个 stage 注入共享 telemetry Bundle，保留框架原生 Agent/Tool/模型 Span，按失败、部分结果、预算、取消、发布/引用提交点核对。

验收见 [ACCEPTANCE.md](ACCEPTANCE.md)。本切片仅覆盖 WS06-E 的**确定性局部执行**；H07 的 `2 深度 × 2 交付 × 3 等级 = 12` 组合等待 WS06-F summary/tools 与实际 RTW/DC/三路索引接纳。
