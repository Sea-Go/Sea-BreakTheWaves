# 搜索 API 进程阶段验收

日期：2026-09-14。基线 BTW `5a9b8ed`，分支 `feat/search-api-process-20260914`。`[W0]` 此独立工作树；`[W1]` `cmd/api/` 与 `internal/app/search_rtw_effective*`；`[R1]` RTW 产品搜索签发/PG 测试、BTW 已有搜索/Worker/客户端；`[D1]` 根 go.mod 锁定 tRPC-Agent-Go v1.8.1；`[G1]` 无；`[X1]` 仅本地测试 HTTP/OTLP fixture，无共享环境写入；`[N1]` 原业务脏树、Docs、生产、真实 Milvus/对象存储；`[T1]` 测试临时目录和本地监听。主职责 `[C6:INFRA]`/`[C1:TRANSPORT]`，跨 `[C2]` 搜索服务与 RootSessionBoundary、`[C4]` RTW 耐久历史、`[C5]` tRPC Graph/Runner、`[C7]` 签名和模型/表示合同、`[C8]` 验证。

入口在缺环境字段、非回环监听、短签名密钥、非法 URL/策略或不支持模式时拒绝启动。只有显式配置的本地 exact 三路、DataCenter 表示客户端、RTW 当前发布 Checker/原文/引用/答案适配和真实 OpenAI 兼容模型被装配；无默认 fixture 或运行时索引构建。`RTWEffectiveRevisionChecker` 在候选检索后重新取 RTW 当前发布，只有相同发布指针/代/三路索引且修订仍有效时才让候选进入证据。`RootSessionBoundary` 用框架私有会话消费完成事件，只有经引用/答案接纳的结构化结果可向产品 HTTP 回答。

局部验证：`go test -mod=readonly -race -count=1 ./cmd/api ./internal/app`、根模块 `go test -mod=readonly -race -count=1 ./...`、`go vet ./...` 与 `go mod verify` 均通过。真实回环 socket 上 `/livez` 和 `/metrics` 可读，未签名 `POST /v1/search/summary` 返回 403，DC/RTW/模型目标均为不可达端点，证明该请求在上游调用前被拒绝。`TestRTWEffectiveRevisionChecker` 覆盖有效、撤回、换代和移指针。

当前状态为 **`LOCAL_VERIFIED/PARTIAL`**。RTW 既有真实 HTTP/PG 验收证明了同一签名范围经 BTW 测试服务和 Root Graph 可提交 `insufficient`，但其三路索引是结构性固定工件，并未通过本 `cmd/api` 的真实本地 exact 读取，不能据此称本进程有证据成功回答或跨仓 PG 已验。模型 Provider/DC BGE、真实三路同代索引、RTW 当前发布与本进程合并运行、正式双进程签名范围、OTLP Collector 查询链、生产 Milvus 与对象存储、详细搜/高中智能/Tools/SSE 尚未验收。
