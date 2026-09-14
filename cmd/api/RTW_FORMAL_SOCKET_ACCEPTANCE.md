# 真实三路索引经正式搜索 API 的跨仓验收

日期：2026-09-14。BTW 分支 `feat/search-api-socket-cross-20260914` 基于 `caed957`，RTW 分支 `feat/knowledge-search-api-socket-cross-20260914` 基于 `ded36cb`。状态：**真实 BGE-M3 → 三路 local-exact 工件 → RTW READY/人工发布 → 正式 `cmd/api` 回环 socket → RTW 有引用答案与耐久收据的隔离子链 `INTEGRATED`**；H02/H06/H07 整体仍 `PARTIAL`。

工作区域：`[W0:ROOT]` 本 BTW 独立工作树及 RTW 独立验收树；`[W1:WRITE]` 本仓 `internal/transport/http/search/rtw_product_server_test.go`、`rtw_real_three_lane_test.go` 和此文档，RTW 的对应 `service/knowledge/api/http*_test.go` 与验收说明；`[R1:READ_ONLY]` BTW/RTW/DC 集成树与 Docs；`[D1:DEPENDENCY]` 锁定的 tRPC-Agent-Go v1.8.1 与 BGE-M3 serving 环境；`[G1:GENERATED]` 无；`[X1:EXTERNAL]` 测试创建的回环 DC/RTW/User Center、隔离 PG 和内容寻址对象目录；`[N1:OUT_OF_SCOPE]` 原业务脏树、共享/生产环境、Milvus 与正式 Collector；`[T1:TEMP]` 各验收脚本的随机临时证据目录。主职责 `[C8:VERIFY]`，跨 `[C6:INFRA]` 进程装配、`[C2:APPLICATION]` 搜索、`[C4:PERSISTENCE]` RTW 引用/答案、`[C5:AGENT_ADAPTER]` 原生 Graph/Runner 与 `[C7:CONTRACT]` 签名范围。

## 真实运行及提交点

DataCenter `cmd/server` 的隔离验收使用官方 hash 锁定的 `BAAI/bge-m3@5617a9f61b028005a4858fdac845db406aefb181` CPU 模型，经真实控制面、typed representation 网关和一次性本地身份生成 Dense、Sparse、Token Matrix 表示。它在自己专属的 PostgreSQL 和一次性会话中成功持久化 6 次交互，并暂持活 Provider 供本次消费者使用。BTW 的 opt-in `build_only` 子进程复用原有 `buildRTWRealThreeLane`，从 RTW 固定来源/Wiki 两个已列入测试的 chunk 构建并 probe 三路实际 local-exact 工件，将同代不可变 Ref 和**构建时同一套** document/query 编码配置写到 0600 结果文件后退出；它不再充当产品搜索 HTTP Handler。

RTW 从本地内容寻址目录回读工件，校验三路 manifest/ref，按已有 `AcceptBuild` 写入 READY，再由管理员显式激活当前发布。此后 RTW 验收进程独立编译并启动 `cmd/api` 二进制，显式传入 `BTW_SEARCH_MODE=local-exact`、只有 `fast/low` 的策略、三路构建配置、`BTW_SEARCH_REPRESENTATION_MAX_IN_FLIGHT=1`、活 DataCenter typed 端点、真实 RTW Worker URL/token、签名 key、两个不同回环监听以及本机 OTLP HTTP 地址。先要求 `/livez` 204、`/metrics` 200，未签名 summary 403 且不触发固定模型。`/livez` 仅证明进程存活，不代替后面的依赖和业务验收。

有引用产品请求从 RTW 真实 User Center JWT/User RPC 进入，RTW 固定 `SubjectRef`、当前人工发布和 search/answer ID 后签名。透明中继保留请求 body 和 `X-Sea-Search-Scope` 原字节，目标是正式 `cmd/api` socket。进程内的三路 Service 对刚发布的实际 local-exact 工件各自调用活 DC BGE 查询表示；DataCenter Provider 同时只能处理一个表示请求，进程共享门限 1 保证三路排队。BTW 使用 RTW 同版 Reader 取原文，取得 RTW PostgreSQL 的耐久 citation receipt 后，tRPC-Agent-Go v1.8.1 单根 Graph/Runner 才调用本地**固定 OpenAI 兼容模型夹具**。夹具只从已接纳 EvidencePack 读取 evidence ID/quote，回传确定性 JSON `answer/citations`；它不是对真实 LLM 质量的验收，也不提供检索候选。RTW 自身回查相同 subject/session/search/answer 的引用、答案和 PG 行后公开成功结果；同键重投与 GET 相同且无第二次 BTW 调用。

固定模型调用计数为 1。正式进程在已处理签名请求后暴露 `sea_btw_operations_total` 指标；本机 OTLP 接收端解码 protobuf，确认 `trpc.agent.go` instrumentation scope 中存在 `invoke_agent search_summary_root` span。SIGTERM 后 `cmd/api` 输出带 `service/component/log_source/event/outcome` 的结构化 started/stopped 事件并完成进程等待。该 OTLP 断言证明本地导出，不证明 Collector→DataCenter→查询 UI 下钻。

## 可重跑门禁与结果

先在 DC 集成树运行其自带脚本，传入已验证的锁定 serving 根目录与缓存快照，并设置 `BGE_HOLD_FOR_CONSUMER=1`。待脚本输出的本次 `runtime.json` 出现、且其持有测试进程仍活，再在 RTW 独立验收树运行：

```bash
KNOWLEDGE_KEEP_EVIDENCE=1 KNOWLEDGE_REAL_USER_GATE=1 \
SEA_DC_BGE_RUNTIME=<本次DC隔离目录>/runtime.json \
SEA_BTW_PRODUCT_SEARCH_ROOT=<本BTW独立工作树绝对路径> \
SEA_BTW_SEARCH_API_SOCKET_ROOT=<同一BTW独立工作树绝对路径> \
bash service/knowledge/scripts/acceptance.sh
```

RTW 最终完整脚本退出码 **0**：真实 User Center 与同协议 gRPC 替身两套 `TestRealHTTPKnowledgeWorkflow` 分别 PASS（63.84s、33.10s）；`go test -race ./service/knowledge/...`、`go vet ./service/knowledge/...`、真实 User Center race/vet、`git diff --check` 均通过。新增正式 socket 断言涵盖签名拒绝、业务后指标、固定模型一次调用、原生 OTLP 根 span、真实 PG 引用/答案/幂等及正常关机。DC 持有进程随后按其 `runtime.json` 指定的一次性 release file 释放，DC 原验收最终退出码 **0**、Provider `wait_completed=true`。BTW 全根 `go test -mod=readonly -race -count=1 ./...`、`go vet ./...` 与两仓 `go mod verify` 均退出码 0。

第一次 DC 启动在新工作树首次生成 uv 环境并加载约 2.3 GB 模型时超过现有 90 秒就绪门限，Provider 没有给出 ready；同一锁定模型缓存预热后重新创建独立 PG/Provider，第二次启动与全链通过。第一次正式 socket 父测试曾在**业务请求之前**要求已经出现业务指标而失败；调整为启动只查端点，原生指标与 OTLP 在签名请求/关机后验收，后续完整脚本两次退出 0，最后一次含 OTLP protobuf 的原生根 span 检查。

本子链没有证明完整来源覆盖：固定来源 `Book A\n\nEvidence` 只将第二段 `Evidence` 与一个 Wiki 段落写入测试 manifest。它没有通过正式 `content.IndexCoordinator`/DC 技术 job 构建生产索引，没有 Milvus、生产对象存储、真实 LLM 回答质量、详搜/中高智能、Tools/SSE、客户端浏览器或正式 Collector 查询链；这些仍按各自交接合同验收。
