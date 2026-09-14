# WS06-E/F 搜索 HTTP 交接局部验收

日期：2026-09-14。基线 BTW 集成分支 `fe88d9c`；框架版本为根 `go.mod` 锁定的 tRPC-Agent-Go v1.8.1。工作区 `[W0]` 独立 `feat/search-http-entry-20260914` worktree；`[W1]` 仅本 `internal/transport/http/search/`；`[R1]` 现有 RootSessionBoundary/Delivery/Runtime、Docs H07/OBS-r3；`[D1]` 锁定依赖只读；`[G1]` 无；`[X1]` 隔离本机 HTTP、合成模型/来源与引用接纳；`[N1]` 原业务脏树、生产、正式 RTW/DC 与客户端；`[T1]` Go 测试进程。

`NewHandler` 在类型上只接 `*search.RootSessionBoundary`，不允许把会持久保存失败模型事件的裸 `RootSummarizer` 接到产品 HTTP。客户端 JSON 只能指定 module/query/fast或detailed/low或medium或high；完整 SubjectRef、逻辑会话、SearchID/AnswerID、固定发布 Snapshot 和降级权限由可信 RTW facade 的 `ScopeResolver` 提供。未知字段、超限请求、非 JSON、错误范围在调用模型前拒绝。成功仅公开通过引用收据校验的答案、引用原文与状态；原始 Runner/Graph 事件和候选内部状态不出 HTTP。取消、错引用或历史接纳失败不公开答案。

`TestHTTPHandlerFrameworkRootAndPublicProjection` 启动真实本地 HTTP server，在隔离子进程安装同一 Bundle 和框架 Logger/Tracer/Meter，实际走 HTTP → 应用阶段 → 项目 Runtime → 单根 tRPC GraphAgent → 无搜索 Tool 的 Summary LLMAgent。OTLP 内存出口核对 HTTP Span、应用 Span、Runtime Span、原生 Agent Span 的同 Trace 父子链；`/metrics` 有框架 Agent/Chat 指标，JSON 有搜索 HTTP 终态。固定 fixture 的 RTW 引用接纳先于模型；成功响应只含已验证引用。伪造 `subject_ref` 在 JSON 解码即拒，可信解析器拒绝时模型不调用；超限 body 也不执行。模型输出错 citation 时 HTTP 仅返回稳定错误码，正文和受控产品历史均无该答案。`go test -mod=readonly -race -count=1 ./internal/transport/http/search` 与 `go vet` 通过。

这只证明**HTTP组件接到了真实框架运行与统一观测**。`ScopeResolver`、SourceReader、CitationAcceptor、AcceptedRootHistory 和模型均为本地 fixture；尚无正式 `cmd/api` 进程装配、RTW权威主体/发布/引用与答案历史、DC真实模型、三路同代索引、SSE/Tools协议、Collector→DataCenter查询。因此 WS06-E/F、H07 和 OBS-03/04/07 整体保持 `PARTIAL/NOT_VERIFIED`。尤其 `RootSessionBoundary` 的私有框架 Session 当前不会向模型注入已接纳的跨轮历史，不能据此声称多轮会话语义完成。
