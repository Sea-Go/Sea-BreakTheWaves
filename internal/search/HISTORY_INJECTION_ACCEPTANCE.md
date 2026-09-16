# 搜索公开会话的已接纳历史显式注入

BTW 独立叶子 `feat/search-history-injection-20260916` 基搜索 Native 开发远端 `b389671`。写区 `[W1]`：`internal/search/root_history.go`、`root_session.go`、`root_graph.go`、`summary.go` 的指令句、`cmd/api` 配置与装配；只读区 `[R1]`：RTW 历史客户端、既有 Graph/Runner/Session 合同、Sea-Docs。主职责 `[C2]` 搜索会话与 `[C5]` 原生Graph，跨 `[C8]` 验收。H02 状态表原文要求的"已接纳历史以显式预算、已核对事实引用注入下一轮模型，并核拒收答案绝不入上下文"在此实现。

## 设计

- **显式预算 `RootHistoryBudget{MaxTurns≤8, MaxBytes∈[256,32768]}`**：零值保持原行为（不注入）。`cmd/api` 要求 `BTW_SEARCH_HISTORY_MAX_TURNS` 与 `BTW_SEARCH_HISTORY_MAX_BYTES` 成对显式给出，单边或越界拒启动。
- **只有 `RootSessionBoundary` 能构造注入**：`NewRootSessionBoundaryWithHistorySeed`（及 fast/medium 组合构造器）在每次新尝试前经 `history.List` 重读同主体同会话的全部已接纳轮，逐轮复用 `History()` 的整轮校验（主体/会话/请求有效/结果有效），再投影为 `acceptedHistoryTurn{search_id,answer_id,question,answer,verified_citations[evidence_id,quote]}`——引用必须是该轮自身已接纳 Pack 里的真实证据且 Quote 非空，否则视为损坏、整次请求失败关闭（模型零调用）。`insufficient` 轮（无答案无引用）合法跳过。
- **注入通道是 context 私有类型 `acceptedHistorySeedKey`**：Graph 的 search 节点在构建既有 `{question, fixed_evidence_pack}` 信封时附加 `accepted_session_history` 字段，内容为规范 JSON 块（轮数组＋预算回显）。节点对 seed 做逐字节完整性重算（`historySeedIntegrity`：Turns+Budget 重序列化必须等于 Block，且 Block ≤ MaxBytes）；被篡改的 context seed 使该次搜索失败关闭，绝不进入提示。
- **预算数学**：超出 MaxTurns 先丢最旧；整块超 MaxBytes 时从最旧整轮丢弃直至放下；全空历史不产生字段。块是唯一真源，消费者不再重排。
- **拒收答案绝不入上下文**：私有尝试 Session 在成功与失败两路都被删除；失败/未校验的模型事件从未到达历史接口，因而不存在可注入表示。指令句同步约束：历史仅作会话上文保持一致，引用只允许当前 `fixed_evidence_pack`，模型夹具答案仍只引当前 Pack 证据（测试断言）。

## 已执行验收（本机 race）

- `TestAcceptedHistoryInjectionGates`（隔离子进程，共享 telemetry 单例）：同会话先一次拒收、两次接纳后，第四次尝试的模型提示含规范历史块（两轮完整 question/answer/verified_citations，预算回显 4/8192），拒收输出 `unsupported/invented` 不在任何提示里；零预算边界两次接纳后提示无 `accepted_session_history` 字段；损坏 Quote 的已存储轮与 `List` 失败均使下一次请求在模型零调用下失败关闭；直接向 `RootSummarizer` 传篡改 seed 同样失败关闭且模型零调用。
- `TestRenderAcceptedHistoryBlockBudget`：4 轮预算 2 保留最新两轮；紧字节预算整轮丢弃最旧；空历史无块；单边/越界预算 `ErrHistoryBudget`。
- `cmd/api`：`TestHistoryBudgetMustBeOneExplicitPair` 覆盖默认关、单边拒启、0/9 轮、256 以下/32768 以上/非数字字节界、合法对 3/4096 逐字到达边界。
- `internal/search`、`cmd/api` 全包 race PASS；`go vet ./...`、`go mod verify`、`git diff --check` 顶层 0。全根 `go test -race ./...` 单轮在**本切片未触碰的** `internal/recommend` 出现 `TestPredictionCandidateDefaultsOffAndRunsThroughTRPCGraph` span-outcome 抖动一次；该测试随后单测两轮与整包 race 均 PASS，属既有负载敏感抖动，不属本切片改动面，不在此掩盖或修复。

## 边界

这是受控本机合同与夹具层实现：模型夹具是确定性假模型，"保持一致"的行为质量未评（`qrel_evaluable=false`）；真实 RTW×BTW 同父轮（真 RTW UserCenter/PG/List 接口与本注入并跑）、Collector 下钻、十二格中高/详搜与生产部署另签。H02 整体仍 `PARTIAL`。
