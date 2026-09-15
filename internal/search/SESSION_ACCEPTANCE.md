# 搜索总结 Session 边界：局部验收

状态：**失败模型事件隔离、RTW 已接纳历史接线与本机产品联验 LOCALLY VERIFIED；已接纳历史进入下一轮模型上下文 NOT IMPLEMENTED**。框架根模块锁定 `trpc-agent-go v1.8.1`，PostgreSQL Session 子模块锁定 `v1.8.0`。裸 `RootSummarizer` 仍可在业务 `validate_answer` 拒绝引用前写入框架 Session；正式 `cmd/api` 和搜索 HTTP Handler 现只接 `RootSessionBoundary`，不把裸 Runner Session 当产品历史。

## 真实反例与边界

`TestRootSessionReadBoundary` 使用真实 `RootSummarizer`、GraphAgent、Runner 和固定模型，先让模型输出 `{"answer":"unsupported","citations":["invented"]}`。最终 `validate_answer` 拒绝错引用，调用方收到失败和空答案；随后通过框架公开的 `session.Service.GetSession(app="search_summary_root", user=SubjectRef.UserKey(), session=逻辑会话)`，在 **in-memory 与本机隔离 PostgreSQL 16** 两种后端均读到 `unsupported` 和 `invented`。这是已持久化/可读的事实，不是从返回值推测。锁定版 Runner 在最终图校验前写入用户及 Agent 事件，没有该业务校验的回滚。测试继续在同一 Session 运行成功的下一轮，并检查真实模型请求；由于当前子 Agent 配置 `WithSubgraphIsolatedMessages(true)`，本次模型请求**没有**读入已拒绝答案。因此目前确认的是直接 Session 读取与未来历史投影风险，不能说当前隔离子 Agent 已受污染。测试不会把 `GetSession` 里的原始事件作为公开历史发布。

新增 `RootSessionBoundary` 使用同一个 `RootSummarizer` 和它的真实框架 Runner/Graph、已安装的共享 telemetry，每次运行只给它全新的进程内私有 Session ID；读完整事件流和业务校验后即删除该私有 Session。失败、取消、校验拒绝或历史接纳失败均不会调用成功结果公开路径。成功或证据不足的终态被封装为 `AcceptedRootTurn`，只经 `AcceptedRootHistory.Commit` 交给产品会话的权威接纳方。`History` 只读该接口返回的已接纳记录，逐条校验主体、逻辑会话和引用结果，并复制嵌套数据；它**不是**框架 Agent 事件历史，不能拿单条产品记录伪称 User/Tool/Agent/Runner 的完整事件链。调用方不得再直接用裸 `RootSummarizer.Summarize` 接公开历史或正式搜索 API。

`AcceptedRootHistory` 的权威实现现在由 RTW `knowledge_accepted_answers`/引用行和 BTW `RTWAcceptedRootHistory` 适配器承接；RTW 以完整 SubjectRef、逻辑 SessionID 和 AnswerID 原子接纳、按接纳顺序查询，并核固定产品 operation 的请求/发布快照和引用状态。BTW `cmd/api` 使用该适配器。RTW 产品 façade 在同键重试时先从自己的 PG 验已接纳答案，避免重复调用 BTW；收到 BTW 不确定响应时做限时回查。相同 AnswerID 的不同输入不能覆盖旧答案。现有模型子 Agent 配置 `WithSubgraphIsolatedMessages(true)`，**已接纳历史尚未进入下一轮模型上下文**，所以产品历史存在并不等于多轮语义已完成。

2026-09-15 的补充修正：`RootSessionBoundary.Close` 现取消并等待整个在途总结，包括 Runner EOF 后仍进行的 RTW 历史提交；关闭后新请求收到 `runtime.ErrClosed`，晚到 Commit 不会让该 HTTP 请求返回成功。RTW 适配器还实现可选 `AcceptedRootLookup.Get`：在 Agent 前按完整主体、会话、AnswerID 读取已有终态。只有 RTW 的 scoped HTTP 404 表示未接纳；503、损坏回执或未知状态直接失败。已接纳 Turn 必须与当前固定 `SummaryRequest` 整体一致，方可复制并返回；同键异输入在 SourceReader/Agent 前拒绝。没有这个可选回查能力的历史实现保留原有 Commit/List 行为，正式 RTW 接线已具备回查。上述回查不能替代 RTW 产品 façade 对不可变 operation hash、发布版本及当前引用状态的复核。

## 验证与后续交接

本轮区域：`[W0:ROOT]` 本 BTW 独立 Git worktree；`[W1:WRITE]` `internal/search/root_session*`、本说明及 `internal/app/search_rtw_history*`；`[R1:READ_ONLY]` RTW 410652b 开发树和 Sea-Docs；`[D1:DEPENDENCY]` 锁定的两项 tRPC-Agent-Go 模块缓存；`[G1:GENERATED]` RTW 客户端生成 DTO 不改；`[X1:EXTERNAL]` 仅本机临时 PG16、UserCenter/RPC/BTW/RTW HTTP；`[N1:OUT_OF_SCOPE]` 原业务脏树、生产、其他工程；`[T1:TEMP]` 脚本创建的隔离证据目录。主职责 `[C2:APPLICATION]`，跨 `[C5]` Runner/Session、`[C4]` RTW 终态、`[C7]` AnswerID/固定请求、`[C8]` 红绿及产品验收。

- `SEARCH_SESSION_CHILD=1 go test -mod=readonly -run '^TestRootSessionReadBoundary$' -count=1 -v ./internal/search`：in-memory 反例、当前模型隔离事实、受控历史成功/失败续跑通过。
- 启动本机隔离 PostgreSQL 16 后设置 `SEARCH_SESSION_TEST_POSTGRES_DSN`，同一命令：PostgreSQL Session 公开读取反例通过，下一轮模型请求未包含被拒答案；无该变量时此子测试明确 `SKIP`。
- `go test -mod=readonly -race -count=3 ./internal/search`、`go vet ./internal/search`：通过；原 `ROOT_GRAPH_ACCEPTANCE.md` 的原生 Trace/指标及单 Runner 测试仍在包级测试内。隔离路径的模型请求保留有效框架 Trace。

2026-09-15 本轮新反例先在原边界上用 `close-during-commit` 子测试复现：`Close` 在已校验答案的历史 Commit 仍阻塞时返回，红色 `go test -race` 退出1，断言位置 `root_session_test.go:206`。修复后该测试绿色，关闭取消 Commit、等待整次调用退出，受控历史仍为空、关闭后请求拒绝。`accepted-boundary` 另外断言同 AnswerID 的同固定请求从已接纳轮次回读且不重复 SourceReader/Agent，异输入与损坏/不可用回查在框架前拒绝；八条并发、两主体同逻辑会话的私有 Session 与受控历史隔离通过。

`SEARCH_SESSION_KEEP_EVIDENCE=1 bash internal/search/test-session-postgres.sh` 在新源头退出0：锁定版裸 Runner 的伪造模型引用在 **in-memory 与本机 PostgreSQL 16** 的 `GetSession` 均可读；安全边界的上述子测试全 PASS，PG 日志记录 `received immediate shutdown request` 与 `database system is shut down`，结束后 `pg_ctl status` 报无 server。`go-test.log` SHA256=`e1d13c617b39ee3d4ed5ce588fb648603c5f26dd831b6cfeef45d621289bdcf2`；该脚本的 PostgreSQL 是框架 Session 后端反例，不能伪称 RTW 产品历史数据库。

当前 BTW 源头四个受影响包 `GOFLAGS=-p=1 GOMAXPROCS=2 go test -p 1 -mod=readonly -race -count=1 ./internal/search ./internal/app ./internal/transport/http/search ./cmd/api` 退出0，日志 SHA256=`ab2051c0fad9bd9af2559c03978464e6458423f2bd0a9728e155a726c83bca67`；对应 `go vet`、`go mod verify`、`gofmt -l` 与 `git diff --check` 退出0。真实 RTW Worker HTTP 夹具还逐项核 scoped404、503、损坏/未知终态；生产适配器的 `Get` 不把非404错误退化为无已接纳轮次。

真实权威产品链另从 RTW 410652b 开发树对本 BTW 独立源运行 `GOFLAGS=-p=1 GOMAXPROCS=2 KNOWLEDGE_REAL_USER_GATE=1 SEA_BTW_PRODUCT_SEARCH_ROOT=<本BTW工作树> KNOWLEDGE_KEEP_EVIDENCE=1 bash service/knowledge/scripts/acceptance.sh`，本机隔离 PG16 完整退出0。`TestRealHTTPKnowledgeWorkflowWithUserCenter` 43.52 秒 PASS，实际 User RPC/UserCenter 为两 UID 注册登录签 JWT；`TestRealHTTPKnowledgeWorkflow` 固定 RPC 两 UID 24.37 秒 PASS。两路径均从 RTW 签名 Scope 经 BTW 真 Runner/Graph 与私有 Session 完成空证据和有引用回答，RTW 同版原文、引用收据、答案及引用行在 PG 可读；RTW 同键 POST/GET 不重跑 BTW、异主体历史 GET404。该 RTW 测试还原地核 v2 投影与老 v1 答案 `TurnJson`/hash及 v1 GET 响应原始字节相等；本切片未改旧 H02 Scope/HMAC/请求 hash、生成 wire 或库表。完整知识与 User 包 race/vet、PG 停止通过；RTW `test.log` SHA256=`ae86a55a02e3e67c5900313e96b8788e60cf075e38d4e47b2b8258ad0e2cda9e`，`stop.log` SHA256=`ca19178a35ab4153b75b494963b66ce8243e1b94c173107c87b44d09db23652d`。首轮无限制包并行的真实 User RPC 启动 15 秒内未监听、脚本退出1；低并发完整复验退出0，首轮不能计入通过。

集成分支新增`internal/search/test-session-postgres.sh`：随机端口启动并停止独立PG16，执行上述根Session反例的`go test -mod=readonly -race -count=1 -v`及`go vet`。实跑输出明确列出`inmemory`、`postgres`、`accepted-boundary`三个子测试均PASS；两种框架后端均可读到被最终拒收的原始模型答案，而受控产品历史只交付通过校验的turn。该脚本不等于正式RTW权威历史或跨轮上下文验收。

已落地的 RTW 原子接纳、单 AnswerID 回查和本机两账号/引用产品验收只适用于这些固定路径。仍须把已接纳的受控历史按预算映射为下一轮 Agent 输入，验证不混入原始 Graph StateDelta，并处理会话长度/分页上限；DataCenter/Collector 实际链路和真实模型效果需另验。H07 全组合、正式多轮上下文及生产 OBS-r3 不因本组件测试整体接纳。
