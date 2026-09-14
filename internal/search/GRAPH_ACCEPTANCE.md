# WS06-E 框架执行局部验收

日期：2026-09-14。基线：BTW 集成分支 `1684d66`，根模块 tRPC-Agent-Go v1.8.1。新增 `graph.go` 把已有确定性 `Service.Execute` 放进真实 `GraphAgent` 函数节点，单次请求通过框架 `RunOption` 固定快照；调用方仍须负责权威快照取得、Runner/Session/遥测装配与最终事件消费。Graph completion 返回搜索元数据和无索引正文的候选结果，不是 RTW 同版原文 EvidencePack。

`TestSearchGraphRunnerUsesNativeSpansAndFixedSnapshot` 实际调用 `runner.NewRunner(...).Run`，分别观测一个 Graph completion、一个 Runner completion、三路合成检索器各一次、调用者修改输入后仍使用原快照；Span recorder 有 `trpc.agent.go` scope 的 `invoke_agent search_execute`、`workflow execute_graph search_execute`、`workflow execute_function_node execute_search`，各一次并形成调用者→Agent→Graph→函数节点的真实父子链。Graph状态未出现索引正文。`TestSearchGraphPropagatesPlannerFailureWithoutResult` 在隔离子进程装配共享Bundle与项目Runtime，验证规划失败产生终端错误、不出现Graph结果；坏输入、坏completion以及与固定请求不符的completion被拒。

执行 `go test -mod=readonly -race -count=1 ./internal/search`、`go vet ./internal/search`、`git diff --check` 及根模块 `go test -mod=readonly -race -count=1 ./...` 均通过。

验收边界：这只证明**搜索Graph组件实际使用框架且有原生Span**。尚无实际 `cmd/api`/worker 搜索入口调用此Graph、共享Bundle框架指标的本路径进程级抓取、同一真实模型/三索引/RTW状态联查、与WS06-F Summary/Tools的同根业务链、Collector/DC下钻；WS06-E仍`PARTIAL`，本路径完整 `observability_status=NOT_VERIFIED`，H07的12组合未接纳。应用自建外层Span或这一组件测试都不能代替后续OBS-03/04/06/07/08。
