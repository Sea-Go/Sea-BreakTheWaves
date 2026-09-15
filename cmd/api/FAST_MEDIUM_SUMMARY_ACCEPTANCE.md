# BTW 快搜·中智能·Summary 模型规划候选交接

## 工作区域与代码责任

- `[W0:ROOT]` 本任务专用的干净 BTW Git worktree，基点为搜索集成 `ad85f5a`；以 `git rev-parse --show-toplevel` 定位，公开交接不固定个人路径。
- `[W1:WRITE]` `cmd/api/{config.go,main.go,idempotent_model.go,*_test.go,本交接}`；`internal/search/{search.go,evidence.go,root_graph.go,root_session.go,medium_plan.go,相应测试}`；`internal/transport/http/search/{handler.go,handler_test.go}`。
- `[R1:READ_ONLY]` Sea-Docs《搜索系统设计方案》、D12 与旧并发验收，RTW 已签发 Summary 请求和既有 BTW 低档/Tools/引用合同。
- `[D1:DEPENDENCY]` 当前根 `go.mod` 锁定 tRPC-Agent-Go 核心 `v1.8.1`、Session/Postgres `v1.8.0`；依据本地 `go doc`/缓存源码复用 `llmagent.WithGenerationConfig`、`WithMaxLLMCalls`、`GraphAgent.AddAgentNode`、Runner 与 OpenAI Adapter，不升级或复制框架实现。
- `[G1:GENERATED]` 无生成源码；测试二进制和构建中间物仅入 Go 缓存。
- `[X1:EXTERNAL]` 本切片只写本地模型/检索/RTW回执夹具，未调用真实 DC Model/索引、RTW生产知识、Collector、浏览器或云端配置。
- `[N1:OUT_OF_SCOPE]` Web 默认选项、WhaleHall 桌宠、RTW 新 HMAC/历史写、Tools、中高详搜/高档、用户模型、qrel/训练和其它工作树。
- `[T1:TEMP]` 单次 Go 测试日志；不提交临时会话/密钥/对象工件。
- 主职责 `[C2:APPLICATION]` 的策略与领域查询校验；跨区 `[C5:AGENT_ADAPTER]` 原生规划Agent/Graph，`[C7:CONTRACT]` 签发Scope、版本及HTTP错误，`[C8:VERIFY]` 三路调用/OBS与默认回归。

## 源起点与实现合同

原正式 `cmd/api` 只有 `fast_low` JSON，单查询 `PlanFunc` 和一批三路本地exact检索。Summary 根 `RootSummarizer` 已在开始一次 Runner 前从 RTW 签发的完整 `SummaryRequest` 导出 `ModelInvocationRef`（权威主体、SearchID、AnswerID、快照SHA），再在 Graph `search_and_accept` 中向 RTW读同版原文并耐久接纳引用，最后无工具 `search_summary` LLMAgent。现有 `gatewayModel` 为每个精确 scope+模型请求提供稳定的 DataCenter OpenAI-compatible 幂等键。此前仅把配置的子查询预算从1调到3不会成为中智能；它仍只有一个原问题，也不能产生新召回。

新增 `fast_medium` 是**原政策JSON内可选对象**，必须有区别于顶层低档的 `version`、`max_batches=1`、`max_subqueries=3`、1–100的每路TopK、1–32的证据额度、1–30秒总wall-time及64–512的规划输出token上限。未出现该对象时，正式入口仍只构造原 Graph、原低档单查询与原 `btw-summary-*` 请求键；不推断默认中档。低档继续用顶层政策version，中档在结果中报告自己的version。配置缺必填模型/策略文件在启动前失败；无中档档位时，真实Handler在进入Runner前只读核实际 `Search.Service` 有效政策，返回503 `SEARCH_PROFILE_UNAVAILABLE`，模型、三路、原文、引用和历史均不调用。这个前置检查还解决锁定Runner把节点 `ErrUnavailable` 变为文本 `flow_error`、HTTP无法用 `errors.Is` 分类的真实错误链边界。

显式中档 Summary 在**同一个根 Runner/Graph**中先执行 `plan_fast_medium` 无工具 LLMAgent，按本档 `WithMaxLLMCalls(1)`、输出token上限和总wall-time调用既有 DataCenter 模型适配，物理调用使用 `btw-plan-*` 幂等键，不与原 Summary `btw-summary-*` 键相撞。Graph服务器节点 `validate_fast_medium_plan` 逐Token读返回JSON：顶层只准**一个字面量** `"queries"`，Unicode转义拼写/重复键/未知键/尾随值、0或3条、空/重复原问题/互重复/超512字节新查询都拒；提示词不是执行授权。成功后把校验过的1–2新查询只放本轮 Context，正式 `Planner` 返回**原问题+新查询**供现有 `Search.Service` 一批依次调用 Dense、学习型 Sparse 与 ColBERT Multi-vector；底层继续固定同一出版快照、三路索引Ref、有效修订、TopK、一个批次和最多三条查询。候选仍要RTW有效状态二次检查、同版原文与耐久引用接纳，Summary Agent只在固定 EvidencePack 后最多调用一次（原512输出token额度），两阶段模型均无Tool。

未签发 `ModelInvocationRef` 的规划调用在 OpenAI出口之前失败。无有效发布修订时跳过规划模型，直接返回无证据；规划模型失败/JSON坏输出不得调用三路或总结。Tools 路由没有根 Graph 提供的规划 Context，故同档调用返回明确不可用，不把本切片改称 Tools 中档。详搜、高档和“高请求降到中”也不由该候选提供。共享模型预算为**一次规划调用+最多一次最终总结调用**及各自输出token上限；实际 Provider 输入token/成本需要 DC 真调用和用量回执签收，代码额度不冒充已测费用。

## 本地验收与剩余交接

源码夹具把原问题 `where` 设为三路无命中，规划模型输出两条查询，其中 Dense 为第一条找到一个修订，Sparse与Multi-vector为第二条找到另一个修订；同轮实际 `DenseReader.Search/SparseReader.Search/MultiVectorReader.Search` 各收到按序三条不同查询、固定快照/Ref，经RTW来源/引用夹具接受两证据后才有 Summary。框架原生 `plan_fast_medium` Agent/Chat、规划校验节点、检索节点及原 Summary Agent/Chat Span须在 exporter关闭冲刷后核对；单看外层HTTP span不满足OBS。低档相同原问题仍一批单查询、无规划模型调用且policy version保持原低档；坏模型重复键/Unicode别名/重复原问题不得产生新lane或Summary调用。另由实际 Handler夹具证明没有中档策略时HTTP503且模型/三路/引用/历史零副作用。以上只可记**L1本地原生Graph、HTTP和三路接口夹具**，不能签真RTW Scope/HMAC→DC真实规划模型→BGE三路索引→RTW来源/引用/历史的 L3 产品链。

模型改写仍未形成可靠的覆盖缺口判断：`EvidencePack.coverage_status=not_assessed` 明确保留，不能声称 D12 中档最终效果完成。真实 DC 模型与输入/输出token/幂等收据、RTW真实签发及引用、发布索引、Collector检索、人工qrel/S09-S12评测、生产 latency/成本、Web medium 默认文案和Tools共享预算仍需各 owner 分别签收。此切片是 **fast/medium/summary 一格候选**，十二格矩阵与 H02 整体继续 `PARTIAL`。

## 2026-09-15 验收结果与红轮

初轮正业务结果和两证据回执实际成功，但测试在 Batch Span exporter `Shutdown` 前检查原生Span，得到空列表并退出1；修复为结束 Runner/Session 后由同一 Provider 冲刷，再核 `InstrumentationScope=trpc.agent.go` 的规划 Agent/Chat/校验节点，不把首轮失败算OBS通过。第二轮缺中档策略时搜索服务先返回 `ErrUnavailable`，框架 Graph/Runner 把错误包装为不保留`errors.Is`的 `flow_error`，原HTTP只能变通用502；现由 `RootSummarizer` 在Runner前只读有效Profile，实际Handler夹具签收503 `SEARCH_PROFILE_UNAVAILABLE`、合法OBS `outcome=failed`、0模型/三路/原文/引用/历史副作用。源审阅还指出普通 `json.Decoder.DisallowUnknownFields` 不拒重复key或Unicode转义别名；逐Token+原始key字节校验后，两类坏模型JSON和重复原问题均在检索前被拒，正模型两查询仍通过。以上修复均由最终源码再测，旧失败轮不冒充最终通过。

最终本 worktree 上 `go test -p 1 -mod=readonly -count=1 ./...` 退出0（43包，含无测试包），完整日志 SHA-256=`b2aa65b3a5447a4548e45e29c1cec63c12b6d548a4fb8a9921a082c5c3dd820d`；受影响 `internal/search`、`cmd/api`、HTTP search 三包 `-race` 退出0，日志SHA-256=`63b961c5b2e48e0af009b58a48fd17864859cc185628147bbf44c2f2dd5586d6`。单独原生Graph正/坏模型/私有历史/一秒阻塞Commit场景退出0，日志SHA-256=`af0c583bd4b05400a549e4463fab08209f1a9afb89eaf41fd0c6474f5c8e926b`。`go vet -p 1 ./...`、`go mod verify`、`git diff --check` 均通过；没有变更模块版本或运行生产依赖。

这些结果证明本地tRPC-Agent-Go v1.8.1 **原生规划Agent→固定三路Reader接口→权威来源/引用夹具→无Tool Summary→私有产品历史夹具**及故障拒绝，等级仅 **L1 LOCAL_VERIFIED**。HTTP缺档测试的ScopeResolver是受控可信夹具，正常签名/HMAC回执使用既有独立合同而非本轮真实RTW签发；三路Reader返回人为固定命中而非真实DC BGE编码/发布索引，模型返回固定JSON而非真实规划能力。正式 `cmd/api` 中档HTTP与 RTW真Scope、DC实际规划Provider/模型token、真实三路同代索引、RTW Citation/History持久PG、Collector和qrel/覆盖质量的一次同轮验收均为 `NOT_VERIFIED`，不能把本地候选升为产品 `ACCEPTED`。
