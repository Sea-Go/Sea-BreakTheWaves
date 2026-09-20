# 快搜中智能 Tools 默认关闭候选与交接

此分支从 BTW 固定集成 `3eb55d5fe5339926eec343543103bc8b168b038d` 出发，只签 `fast/medium/tools` 一格的本机 L1 候选。现有 `fast/low/tools` 默认行为保留；十二格产品矩阵、搜索效果和正式生产启用仍是 `PARTIAL`。

## 工作区域与所有权

- `[W0:ROOT]` 本独立 BTW worktree 的根 Go module。
- `[W1:WRITE]` `internal/search/tool_http_boundary.go`、`model_invocation.go` 与本次测试；`internal/transport/http/search/tools_handler.go` 与本次协议测试；`cmd/api/config.go`、`main.go`、本交接和 README。
- `[R1:READ_ONLY]` Sea-Docs《搜索系统设计方案》、并发验收 §147，RTW Tools 签名/回执合同，以及 BTW 既有中档 Summary/低档 Tools 实现。
- `[D1:DEPENDENCY]` 当前 `go.mod` 实际解析的 `trpc.group/trpc-go/trpc-agent-go@v1.8.1`；用 `go doc` 与该版本 Graph/LLMAgent/Runner 源码核 API。未修改 Go 依赖。
- `[G1:GENERATED]` RTW generated API/types 与 BTW 生成文件只读，未手改。
- `[X1:EXTERNAL]` RTW PG/User Center、DC 真实模型和三路已发布索引、本机 Collector/线上均未写入；测试使用隔离模型、Reader、引用夹具。
- `[N1:OUT_OF_SCOPE]` Wiki、推荐/广告、详搜、高档、Web/WhaleHall、其它并行 worktree、RTW/DC 产品源码与业务主分支。
- `[T1:TEMP]` `/private/tmp/sea-btw-fast-medium-tools-*.log` 保存本机命令证据，不进入 Git。

主职责区为 `[C2:APPLICATION]` 搜索 Graph 编排与 `[C5:AGENT_ADAPTER]` 模型运行；跨区 `[C1:TRANSPORT]` 签名 HTTP 放行、`[C3:DOMAIN]` 查询/政策预算、`[C7:CONTRACT]` RTW 同版引用与父预算、`[C8:VERIFY]` 原生事件和协议断言。

## 输入、执行与提交点

```text
RTW 签发完整 Tools 父操作 + child body/scope
  → BTW 验签、请求 hash、固定发布 Snapshot/主体/预算
  → 显式 fast_medium 策略 + 独立 Tools 开关 + 真实等级前置预检
  → 单根 tRPC Graph/Runner 的无 Tool Planner LLMAgent（至多一次）
  → 服务端只收唯一字面量 queries 的 1–2 条有效新查询
  → 原问题 + 新查询在同一批次分别进入 Dense/Sparse/Multi-vector
  → RTW 当前有效修订二次核验 + 同版原文读取 + 耐久引用 Accept
  → 裸 ToolSearchResult（无 Summary Agent、无答案、无进程内父预算）
```

策略文件中的 `fast_medium` 必须拥有独立版本、一个批次/三个总查询槽、各路 TopK、证据额度、1–30 秒总墙钟与 64–512 规划输出 token 上限。`BTW_SEARCH_FAST_MEDIUM_TOOLS` 不设置时保持关闭；仅字面量 `enabled` 且策略显式存在时装配中档 Tools Graph/HTTP。Summary 中档策略本身不会打开 Tools 中档。服务端沿用既有 `ParseFastMediumPlan` 的逐 Token 原始字面键校验，拒重复/转义别名键、未知/尾随 JSON、空/原问题/互重复及超过 512 字节的新查询。模型提示词不改变签发范围、批次数或执行预算。

无有效发布修订时跳过 Planner 和 Reader，返回无收据的空结果。缺中档策略、未开启模型路径、请求等级被实际政策降到低档或不支持的深度/等级均在新 Reader/Runner 前明确 503；HTTP `requested_intelligence` 必须与 `effective_intelligence` 相同。RTW 的签名 `allow_lower_intelligence` 不能让本版 Tools 以中档名义返回低档取证。规划失败或坏 JSON 不进入三路检索、引文读取或最终 Summary；取消在规划中阻止 Reader，晚到的 RTW 引用提交仍由 RTW 原键恢复，本次取消响应不公开引文。Tool Graph 私有 Session 在完整 Runner 终态后删除；`Close` 会等已经进入 RTW Acceptor 的节点。Tools 没有答案历史写入步骤，父操作/累计预算与已接纳引用由 RTW PG 拥有。

中档模型仍通过已有官方 OpenAI adapter 走显式 DC 模型地址，以固定 SubjectRef 兼容表示、父 OperationID、child SearchID、Snapshot SHA 与模型请求 SHA 形成 `btw-plan-*` 幂等键。现有 `ModelInvocationRef.TenantID="platform"` 是 v1 兼容槽，系统没有额外产品租户；不由模型 JSON 或客户端字段推导身份。规划 Agent/Chat 与 Graph 校验/搜索节点使用 tRPC 原生 Span/指标，应用 `slog` 仅记录阶段、状态和固定操作 ID，测试拒主体及改写查询落入日志/指标标签。

## 本机证据和限制

红轮 `go test -mod=readonly -run '^TestFastMediumToolsNativePlannerAndBareEvidence$' ./internal/search` 在旧代码缺构造器时退出 1：`/private/tmp/sea-btw-fast-medium-tools-red.log` SHA256 `4004951f440d48758181eafd4c1677ebc6631f6ec9858f6f4c3128f7a157ee9d`。绿轮的真 tRPC Graph/Runner 模型夹具核一调用/无 Tool、原问题加两改写、每路按序三次同 Snapshot/Ref、两份同版证据先经引用 Acceptor、只返结构化结果；重复/转义 JSON 等坏计划零新增 Reader，低档原单查询回归，空修订不调用模型/Reader。独立签名 HTTP 夹具核默认关闭 503、有效开关但缺政策 503、显式开关 200 裸回执；规划中的取消、晚到 RTW Acceptor 提交与 Close 等待分别经 race 测试。

最终源码全模块 `GOMAXPROCS=2 go test -p 1 -mod=readonly -count=1 ./...` 43 包/0 FAIL，日志 `/private/tmp/sea-btw-fast-medium-tools-full-final.log` SHA256 `4039309e1b3470c5a598b56966d5f6f338d1cbc09480328f8016627cfa70859f`；受影响 `internal/search`、`internal/transport/http/search`、`cmd/api` 三包 `-race -count=1` 退出 0，日志 `/private/tmp/sea-btw-fast-medium-tools-affected-final-v2.log` SHA256 `3ae543a86a644ed073b5dce3bd415d4e5a0bd8cb9a0be17f28a2d57a9a34bb60`；单独晚到 RTW 接纳 race 日志 SHA256 `b06314abbf591a973b458fc03d4b8958cf47201d1aa861464028171ae0c5ff02`。全仓 `go vet -mod=readonly -p 1 ./...` 退出 0 的初轮空日志 SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`，`go mod verify` 输出 `all modules verified`；最后源码未修改依赖。上述 fixture 没有让真人 RTW User Center/PG 签发新的中档父操作，没有使用真实 DC 模型/实测 input、output token 或路由收据，也没有在同一轮查询真实 BGE 三路发布索引；HTTP 成功夹具的投影与原生 Runner 夹具是不同隔离测试。`EvidencePack.coverage_status=not_assessed`，不能把改写证明成覆盖缺口判断或 Recall/qrel 改进。

## 并发交接与下一验收

1. **RTW Tools owner**：在真实 User Center 下签发 `fast/medium` child scope，并从本分支的 BTW 200 裸回执回查相同 SearchID/PackHash 的 PG 引用及父预算扣减；验证掉线重试与取消后同键恢复，独立记录产品历史边界。
2. **DC model owner**：把规划模型在受控路由配置为可用，给真实请求回报输入/输出 token、幂等收据和路线版本；不足时 503/可观测失败，不用固定夹具成本代表实际用量。
3. **BTW 三路索引和效果 owner**：用同发布代的 Dense、学习型 Sparse、ColBERT Multi-vector 查询和人工 qrels，在同一快搜批次对照低档与中档的独有命中、误召和延迟；不可把不同快照的 matrix 报告拼成一格产品效果。
4. **OBS/客户端 owner**：在 Collector 下验证 HTTP → 原生 Planner/Chat → 三路 Reader → RTW Acceptor 的同 Trace 下钻；Web/WhaleHall 如要选中档 Tools，需显示实际等级、覆盖未知和累计父预算，不把 Tools 证据冒充最终答案。

在上述同轮跨服务证据出现前，本分支只可按默认关闭的开发候选合入 BTW 开发集成，不应记为第二个 Tools 产品已交付格。
