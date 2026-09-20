# WS06-E/F 单根搜索总结 Graph 组件验收

日期：2026-09-14。固定基线：BTW `ac3cc4c`，根模块 `trpc-agent-go v1.8.1`。本次只新增 `root_graph.go`、窄测试与本记录，不修改既有搜索内核、交付、Summary、工具、Runtime、telemetry、正式 API 或 Sea-Docs。

工作边界：`[W0]` 本独立 BTW 工作树；`[W1]` 上述 3 个文件；`[R1]` 同仓搜索/Runtime/telemetry、Sea-Docs OBS-r3/C17；`[D1]` 锁定版框架源码；`[G1]` 无；`[X1]` 隔离进程中的模型、RTW 原文与引用接纳替身，无真实外部写入；`[N1]` 原脏树、其他工作树、生产；`[T1]` Go 测试临时进程。主职责 `[C5:AGENT_ADAPTER]`，跨 `[C2]` Delivery、`[C7]` EvidencePack/CitationReceipt、`[C8]` 原生观测与失败验证。

组件把固定 `SummaryRequest` 经 `agent.MergeRuntimeState` 送进共享 Runtime 的**一个**真实 Runner。Runner 执行 `search_summary_root GraphAgent`：`search_and_accept` 函数节点调用原 `Delivery.Search`，必须先从 RTW 同版原文取得证据并接纳引用；无证据经条件边直接到 `validate_answer`；有证据才把固定 EvidencePack 作为当前轮输入交给真实 `search_summary LLMAgent` 子节点。子节点无搜索 Tool，并用框架的隔离消息配置；最后函数节点校验模型 JSON、非空答案、引用 ID 必须来自固定证据且不能重复。原始模型事件不公开，调用方只在 Graph completion 合同通过、Runner completion 且事件流读到 EOF 后收到答案。

锁定版 API 已用根 `go.mod`、`go doc` 与 `@v1.8.1` 源码核对：`runner.NewRunner` 由现有 Runtime 持有；`graph.StateGraph.AddAgentNode`、`AddConditionalEdges`、`WithSubgraphInputFromLastResponse`、`WithSubgraphIsolatedMessages`、`WithSubgraphOutputMapper`、`graphagent.WithSubAgents` 均在本版本可用。这里没有创建第二个 Runner、手写 Agent 事件循环或复刻观测管线。`NewSearchGraphAgent` 既有单独组件未作为根图子图装配；根图的函数节点由框架执行，调用的检索内核仍是普通 Go `Service`，不能据此把三路真实索引/模型/RTW 接线称作完成。

`TestRootSummarySingleRunnerNativeGraphAndChat` 在隔离子进程装配项目 Bundle、框架 Logger/Tracer/Meter 和真实 Runner，用合成三路候选、原文与接纳方及合成模型实际执行成功路径。固定 Trace 中 `runtime.run → invoke_agent search_summary_root → workflow execute_graph search_summary_root → workflow execute_function_node search_and_accept / search_summary / validate_answer` 各一次；`invoke_agent search_summary` 是 Agent 节点的子 Span，`chat summary-fixture` 是该子 Agent 的子 Span，均为框架 `trpc.agent.go` scope。进程 `/metrics` 出现原生 Agent 与 Chat 指标，无 Tool Span；模型收到的是引用接纳后的固定 EvidencePack，且无搜索 Tool。现有 Runtime 已验证完整事件流、单次 Runner completion 和 EOF；本组件再要求恰好一个有效 Graph completion 后才返回公开对象。

同一隔离测试验证：非法 Subject/查询在 Runner 前拒绝；引用接纳失败、RTW 原文读取失败、接纳后取消都不会调用模型或公开答案；无证据直接返回 `insufficient`；模型引用了证据包外的 ID 时 Graph 终端错误且公开答案为空。输入 Snapshot 的 map/slice 在 RunOption 构造时克隆。接纳后取消时原 `Delivery.Search` 已能返回耐久 receipt 和错误，**Graph 错误路径不把该 receipt 透出**；正式 RTW 引用读取/按 `search_id` 恢复接口及 API 错误映射必须在入口接入前另行交接，不能把取消当作成功。

验证命令：`go test -mod=readonly -race -count=1 ./internal/search`、`go vet ./internal/search`、`go test -mod=readonly -race -count=1 ./...`、`go vet ./...`、`go mod verify`、`git diff --check` 均通过。

结论：单根框架编排和原生观测为 **组件 LOCAL_VERIFIED**；搜索正式 API/worker、真实 DC 模型与三路同代索引、真实 RTW/Collector→DC 下钻、12 组合 H07、失败后的耐久引用恢复未验，WS06-E/F 与 OBS-r3 路径最终状态仍 **PARTIAL / NOT_VERIFIED**。正式入口需借用同一已安装 Bundle、Session 与 `RootSummarizer`，按权威发布指针取得固定 Snapshot，走实际 DC/RTW 客户端，再验证公开协议终态和跨服务 Trace。**框架Session另有未验边界**：本方法对外只交付通过校验的答案，但Graph State和LLMAgent中间事件可能在最终引用拒收前由Runner写入Session；目前没有检查失败答案是否会被下一轮读入或由会话历史接口曝光。正式会话复用/公开历史接入前须做真实Session读取反例与受控投影，不能把方法返回为空等同于Session里没有原始答案。
