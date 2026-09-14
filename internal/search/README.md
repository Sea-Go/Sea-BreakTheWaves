# WS06-E 搜索执行内核与 WS06-F 证据交付局部实现

工作区域：[W0:ROOT] 本隔离工作树；[W1:WRITE] `internal/search/`；[R1:READ_ONLY] `internal/retrieval/{dense,sparse,multivector}`、`internal/corpus`、`internal/telemetry`、Sea-Docs 与其他仓；[D1:DEPENDENCY] Go 模块缓存的 tRPC-Agent-Go v1.8.1；[G1:GENERATED] 无；[X1:EXTERNAL] 无实际写入；[N1:OUT_OF_SCOPE] 现有业务树、根依赖文件、正式 HTTP/worker 与发布指针；[T1:TEMP] Go 测试临时目录。主职责 [C2:APPLICATION]/[C3:DOMAIN]，跨 [C7:CONTRACT]/[C8:VERIFY]。本包不拥有客户端身份、RTW 发布、三路索引构建或模型服务。

`New` 借用三路只读 `Snapshot.Search`、查询 Planner 和有效修订 Checker。调用者从 RTW 权威发布状态取得同一 module/release/generation 的三个内容寻址索引，以及当前有效修订集合；本包固定拷贝后向三路分别传同一范围和期限。任何路返回错代、错索引、无效修订、重复/错位次、非有限分数、错片段，整路失败。空有效修订集合直接返回 `empty/no_evidence`，三路均标记 `requested + skipped`，不调用编码器。`Snapshot.PublicationRevision` 仅保留调用方固定指针的引用；本包未接 RTW 权威指针读取，不能由该字段自行证明发布或 READY。

融合键是 `source_kind + content_id + revision_id + chunk_id`。各路原始分数、从 1 开始的 rank 和 index ref 都保留；对同一查询使用三路等权 `1/(60+rank)`。详搜重复改写命中同一片段时保留来源列表，但每路仅取最佳 rank 的贡献，避免同义改写堆分。跨路同键而 chunk 内容不同则按合同冲突拒收。当前没有 rerank；`RRFScore` 是候选排序基线，不是相关性概率。

六档配置由调用方提供固定版本 `Policy`；构造时拷贝并验证每档为有界值，拒绝相邻等级完全相同的预算。`fast` 只能执行一批，批内可有有限子查询；`detailed` 可多批，每次规划前检查剩余子查询数，出现无新证据、无新查询、证据/批次上限或取消即停止。Planner 只能产出查询文本，不能改变快照、等级或预算。尚未冻结 D12 的真实容量值，测试中的六档数值仅是 fixture，不是线上产品默认配置。高等级的模型规划和覆盖判断尚未接入；真实高智能效果不能从预算差异推断。

不可用等级只有在 `AllowLowerIntelligence` 显式为真时才降到较低档，并返回 requested/effective 和原因；深度永不自动升级。单路失败只有 `AllowPartial` 为真且仍有可用路径时返回 `partial` 与独立 `lane_status`，三路全失败报错。取消优先返回 Context 错误。`complete` **只表示 fast 配置批次完成且有经有效状态检查的候选，不表示答案充分**；详搜未有覆盖判定，所以即使有候选也只返回 `partial`，不将 `no_new_evidence` 冒称充分完成。`empty` 是无有效候选，可能有降级原因；调用方须阅读 lane_status。

`VerifiedCandidate` 只通过当前有效状态的二次检查，并非 `EvidencePack`：`Candidates` 与 `VerifiedCandidate` 的 `Chunk.Text` 在公开返回前都被清空（包括错误返回），不能拿索引里的片段当原文。WS06-F 局部 `Delivery` 注入 RTW 同版原文 `SourceReader` 与引用 `CitationAcceptor`，严格核对修订、对象 hash、定位与 quote hash，并只在收据匹配固定 EvidencePack hash 后返回可引用证据；目前仅用假实现验收，不宣称已接正式 RTW 接口。执行内核只约束 wall time、批次、子查询、每路 TopK 和候选数；交付层额外约束阅读次数和 quote rune 数，模型调用数/token 及正式累计账本仍缺，不可认为完整 H07 预算已实现。

框架选择：项目根 `go.mod` 固定 tRPC-Agent-Go v1.8.1，已核对公开 `graph.NewStateGraph`、`graphagent.New`、`runner.NewRunner`，以及框架 Knowledge VectorStore 不能表达学习型 Sparse 与 token MaxSim 的固定工件边界。三路表示继续使用已有领域包，确定性执行内核独立可测；正式 Search GraphAgent/Runner、模型 Planner、typed Tools 与 summary 均未在本切片装配。本包的 `for` 是当前确定性预算循环，不冒称符合 Docs 的最终 StateGraph 条件环或 C17 搜索运行时验收。

观测状态为 `PARTIAL`（OBS-2026-09-14-r2）：WS06-F fixture 的 Summary `LLMAgent→Runner` 与调用方 `Runner→LLMAgent→FunctionTool` 使用已安装的共享 Bundle，并验证框架原生 Agent/Chat/Tool Span 和指标，同一调用方 Agent/Tool TraceID 相同。WS06-E 确定性检索、WS06-F 原文/引用接纳阶段未注入业务 stage 日志/Span/指标，也没有 Collector→DataCenter 下钻；返回状态/耗时字段和测试输出不算 OBS 接纳。最终集成须在 Search GraphAgent/Runner 外层及每个 stage 注入共享 Bundle，保留框架原生 Agent/Tool/模型 Span，按失败、部分结果、预算、取消、发布/引用提交点核对。

验收见 [ACCEPTANCE.md](ACCEPTANCE.md)。WS06-F 的 summary 与 tools 已分别有真实框架 Agent/Tool 局部调用，但仍待正式 Search GraphAgent、RTW/DC、真实模型与三路索引接纳；12 组合只做固定 fixture，不算 H07 真实验收。

## WS06-F 证据与两种交付

`Delivery.Search` 借用 `SearchExecutor`、有效状态 Checker、RTW 同版 `SourceReader` 和耐久引用 `CitationAcceptor`。`SearchExecutor` 是可替换的同步执行边界，允许集成方提供 Search GraphAgent/Runner completion 适配器。原文必须与索引片段的 `source_kind/content_id/revision_id/chunk_id`、`Original` 内容寻址、`Location` 和 `TextHash` 一致；返回给 `CitationAcceptor` 的包包含固定快照、来源和原文。`CitationReceipt` 的 search_id、pack_hash、durable_ref 必须匹配；无有效证据不调用接纳。RTW 接口必须真正写入并提交引用映射后再返回该收据，HTTP 200 或 DC 任务技术收据不能代替它。正式适配器还需在接纳时重新核对发布指针。

`Summarizer` 在证据和收据固定后才运行共享 telemetry 下的 tRPC-Agent-Go `LLMAgent→Runner`，给模型的唯一问题输入是固定 EvidencePack，装配零个搜索 Tool。完整 Runner 终态后解析 `answer/citations` JSON，并检查 citation ID 来自本包；失败时保留搜索状态、不返回答案。当前只提供完整答案，不提供符合产品流协议的公开 SSE 增量、答案历史或 RTW answer_id 映射接纳。搜索阶段现在还是 `SearchExecutor` 普通服务实现；尚未把搜索与总结装在**单个根 GraphAgent**，不能据 Summary Runner 的 Span 宣称 C17 正式搜索链已满足。

`ToolSession` 每个已验证主体/调用创建一次，固定 CorpusSnapshot、operation_id 与剩余额度；`search_fast`、`search_detailed`、`read_evidence` 是 tRPC-Agent-Go v1.8.1 原生 typed FunctionTool。模型 JSON 只能指定 query、等级或本次返回的 evidence ID，不能改发布范围、索引或总预算。Tool 只返回 SearchResult、EvidencePack、收据和剩余额度，不产生搜索方最终答案。阅读重新向 RTW 请求同版原文并复核当前有效状态。预算在调用前按最大允许读取量**保守预留**，成功后按 `source_read_attempts/quote_runes` 退还未使用的额度，失败和重试保留预留消耗；ToolSession 当前进程内存储，跨进程恢复、游标/上下文续读、同 search_id 继续详搜与模型 token 账本未实现，`continue_search_id` 显式返回 `ErrUnavailable`。

WS06-F 反馈事实、负例资格和词典准入仍为 `NOT_IMPLEMENTED`。不得从未点击候选推导负反馈，也不能把共点击自动当成同义词；这两部分需与 WS07 数据产品、RTW/客户端真实展示收据及 WS09 评测合同对齐后单独实现和验收。
