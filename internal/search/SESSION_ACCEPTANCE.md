# 搜索总结 Session 边界：局部验收

状态：**组件 LOCALLY VERIFIED；正式会话续跑与权威历史 NOT IMPLEMENTED**。本记录只针对锁定的根模块 `trpc-agent-go v1.8.1`、PostgreSQL Session 子模块 `v1.8.0`。现有 `RootSummarizer` 是框架原生单 Runner/Graph/LLMAgent 编排，但其 `Summarize` 返回失败或空答案，并不表示框架 Session 没有写入失败模型输出。

## 真实反例与边界

`TestRootSessionReadBoundary` 使用真实 `RootSummarizer`、GraphAgent、Runner 和固定模型，先让模型输出 `{"answer":"unsupported","citations":["invented"]}`。最终 `validate_answer` 拒绝错引用，调用方收到失败和空答案；随后通过框架公开的 `session.Service.GetSession(app="search_summary_root", user=SubjectRef.UserKey(), session=逻辑会话)`，在 **in-memory 与本机隔离 PostgreSQL 16** 两种后端均读到 `unsupported` 和 `invented`。这是已持久化/可读的事实，不是从返回值推测。锁定版 Runner 在最终图校验前写入用户及 Agent 事件，没有该业务校验的回滚。测试继续在同一 Session 运行成功的下一轮，并检查真实模型请求；由于当前子 Agent 配置 `WithSubgraphIsolatedMessages(true)`，本次模型请求**没有**读入已拒绝答案。因此目前确认的是直接 Session 读取与未来历史投影风险，不能说当前隔离子 Agent 已受污染。测试不会把 `GetSession` 里的原始事件作为公开历史发布。

新增 `RootSessionBoundary` 使用同一个 `RootSummarizer` 和它的真实框架 Runner/Graph、已安装的共享 telemetry，每次运行只给它全新的进程内私有 Session ID；读完整事件流和业务校验后即删除该私有 Session。失败、取消、校验拒绝或历史接纳失败均不会调用成功结果公开路径。成功或证据不足的终态被封装为 `AcceptedRootTurn`，只经 `AcceptedRootHistory.Commit` 交给产品会话的权威接纳方。`History` 只读该接口返回的已接纳记录，逐条校验主体、逻辑会话和引用结果，并复制嵌套数据；它**不是**框架 Agent 事件历史，不能拿单条产品记录伪称 User/Tool/Agent/Runner 的完整事件链。调用方不得再直接用裸 `RootSummarizer.Summarize` 接公开历史或正式搜索 API。

`AcceptedRootHistory` 是交接合同，不是本分支的耐久实现。权威实现应在 RTW/Conversation 边界按完整 SubjectRef、逻辑 SessionID、AnswerID 对单个已验证回合做原子耐久接纳；相同答案重试幂等，冲突拒绝；`List` 只返回该主体/会话按接纳顺序的记录。提交失败或结果不确定时不能公开答案，恢复应按 AnswerID 查询接纳结果。测试采用内存桩验证失败从未进入受控历史、成功后同一逻辑会话可再接纳下一轮、历史返回值与内部数据不共享切片，以及提交失败闭合；**没有 RTW 耐久实现**，也没有把受控历史注入当前 `WithSubgraphIsolatedMessages(true)` 子 Agent。因此该私有 Session 边界是正式接线前的污染隔离，不是最终的多轮上下文语义。

## 验证与后续交接

- `SEARCH_SESSION_CHILD=1 go test -mod=readonly -run '^TestRootSessionReadBoundary$' -count=1 -v ./internal/search`：in-memory 反例、当前模型隔离事实、受控历史成功/失败续跑通过。
- 启动本机隔离 PostgreSQL 16 后设置 `SEARCH_SESSION_TEST_POSTGRES_DSN`，同一命令：PostgreSQL Session 公开读取反例通过，下一轮模型请求未包含被拒答案；无该变量时此子测试明确 `SKIP`。
- `go test -mod=readonly -race -count=3 ./internal/search`、`go vet ./internal/search`：通过；原 `ROOT_GRAPH_ACCEPTANCE.md` 的原生 Trace/指标及单 Runner 测试仍在包级测试内。隔离路径的模型请求保留有效框架 Trace。

集成分支新增`internal/search/test-session-postgres.sh`：随机端口启动并停止独立PG16，执行上述根Session反例的`go test -mod=readonly -race -count=1 -v`及`go vet`。实跑输出明确列出`inmemory`、`postgres`、`accepted-boundary`三个子测试均PASS；两种框架后端均可读到被最终拒收的原始模型答案，而受控产品历史只交付通过校验的turn。该脚本不等于正式RTW权威历史或跨轮上下文验收。

正式 API 前仍须：实现 RTW/Conversation 权威 `AcceptedRootHistory` 和失败后 `AnswerID` 恢复；明确多实例幂等、提交不确定、会话排序；把受控已接纳历史按预算映射为下一轮子 Agent 输入并验证不混入原始 Graph StateDelta；公开历史只从权威 `List` 读取；在 DataCenter/RTW/Collector 实际链路上复验同根原生 Span 与业务提交点。上述任一项不能用这里的组件测试替代，H07、正式会话续跑及 OBS-r3 仍未整体接纳。
