# RTW 签发到 BTW 产品搜索：消费者半边验收

日期：2026-09-14。基线 BTW 集成提交 `896d510`，根模块锁定 tRPC-Agent-Go v1.8.1。工作区 `[W0]` 独立 `feat/rtw-product-search-consumer-20260914`；`[W1]` 本目录新测试与本说明；`[R1]` BTW 已有 `SignedScopeResolver`、`NewHandler`、`RootSessionBoundary`、RTW 生成客户端和知识服务契约；`[D1]` 锁定 Go 依赖；`[G1]` 无；`[X1]` 仅 RTW 验收父进程启动的隔离知识服务、HTTP 和 PostgreSQL；`[N1]` 原始业务脏树、生产、真实模型及三路索引；`[T1]` 有界本地测试进程和 0600 夹具文件。

RTW 父测试将 `SEA_RTW_PRODUCT_SERVER_FIXTURE` 指向 0600 JSON，字段为 `rtw_base`、`worker_token`、原始 UTF-8 `scope_key`（至少 32 字节）及 `ready_path`。BTW 的 `TestRTWRealProductSearchServer` 在独立测试进程里建立统一 telemetry、原生框架追踪、真实 `RootSessionBoundary` 和 `RTWAcceptedRootHistory`，用 `SignedScopeResolver` 验证 RTW 透明转发的签名请求。就绪后向 `ready_path` 写纯本地 HTTP URL（0600），直到收到 SIGTERM 或 120 秒上限。普通无夹具运行会 `SKIP`。

本轮 `SearchExecutor` 返回合法 `empty/no_evidence`，经过真实单根 GraphAgent/Runner，形成 `insufficient` 产品轮次，再由 RTW 生成客户端向同一知识服务 POST 提交。原文读取、引用接纳、模型调用在此路径均必须为零；子进程退出时确认原生根 Agent Span。RTW 父测试负责从自己的 PostgreSQL 验证 AnswerID 和主体、会话归属，并覆盖坏签名、错请求不能入库。这个路径不证明有证据的成功回答、模型质量、三路同代索引、客户端 SSE/Tools 或 H02/J02 的整体验收。

本仓验证：`go test -race ./...`、`go vet ./...`、`go mod verify` 均通过；`go test -race ./internal/transport/http/search -run '^TestRTWRealProductSearchServer$' -v` 无夹具时按预期跳过。

跨仓验证：从 RTW 集成树运行 `SEA_BTW_PRODUCT_SEARCH_ROOT=/Users/edy/Sea/.codex-worktrees/sea-btw-rtw-product-consumer-20260914 bash service/knowledge/scripts/acceptance.sh`，退出码 0。RTW `TestRealHTTPKnowledgeWorkflow` 的 stage4 将自身签发的原始请求透明转发给此真实 BTW HTTP，BTW 验签后运行根 Graph/Runner，通过 RTW Worker HTTP 提交 `insufficient`。RTW 在同一隔离 PostgreSQL 的 `knowledge_accepted_answers` 中核实该 AnswerID、SearchID、`rtw.identity/platform/<UID>` 和逻辑会话只有一条；`knowledge_search_citations` 中该 SearchID 为零；同一幂等键重投未重跑 BTW，GET 与 POST 结果一致。BTW 子进程退出检查原文读取、引用接纳、模型调用均为零及原生根 Agent Span。此子链结论为 **`INTEGRATED`**，H02/J02 整体仍为 **`PARTIAL`**。

当前默认脚本中的 User RPC 是真实 gRPC 传输上的验收替身；`TestRealHTTPKnowledgeWorkflowWithUserCenter` 明确跳过，不能把这次结果写成真实 UserCenter 进程已联验。三路真实检索命中和有证据的模型回答仍待后续验收。
