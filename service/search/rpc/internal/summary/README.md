# summary（B6 摘要交付 · 纯域层）

`service/search/rpc/internal/summary` 实现工程方案 §2.2/图 3 的 B6：
**summary 交付**——把 B5 产出的 `EvidencePack` 变成"带行内引用角标的
答案 + 引用列表"（`B5 ──C-10──► B6 ──答案+引用──► A1 ──► 用户`）。
本包是纯域层：不调模型、无 IO、无状态，stub 与格式化器均零值可用、
并发安全、同输入同输出。

## 职责（B6）

| 能力 | 入口 | 说明 |
| --- | --- | --- |
| 域契约 | `SummaryRequest` / `SummaryResult` / `Citation` / `Summarizer`（summary.go） | C-10 出口侧的 Go 形态：请求携带 query + 证据包；结果携带 answer（内嵌 `[n]` 角标）+ citations（角标序号 + doc_key + Locator） |
| dev 摘要器 | `StubSummarizer.Summarize`（stub.go） | 确定性摘要（见下），不调模型 |
| 出口校验 | `SummaryResult.Validate()`（validate.go） | answer 非空 ≤4000 rune、citations 1..8、角标唯一且在 1..8、每条 Index 在 answer 内以 `[n]` 至少出现一次、quote 非空且 ≤200 rune、doc_key 非空、query_id 非空 |
| 前端格式化 | `FormatAnswer` / `RenderCitations`（format.go） | `[n]` → `[n](#cit-n)` Markdown 锚点链接（CitationCard 挂 `#cit-n` 锚点）；脚注式引用列表 `[n] docKey §path ¶para — quote` |

stub 的确定性口径：答案 = `根据 N 篇文档……` + 按 RRF 降序前 3 候选
（平局按 doc_key 字典序，与 `evidence.BuildPack` 同规则）的首条 quote
拼接（截 200 rune），行内角标 `[1][2][3]` 与候选序对应；citations 从
这些候选的 evidence 各取首条 Locator。空包（0 候选）直接拒绝——
**绝不产出无证据答案**。

## 与 C-10 的对应

C-10（B5→B6 证据包）是本包的唯一入口载荷：

```
B4 ──C-9──► B5 ──BuildPack──► EvidencePack ──C-10──► B6（本包）
                                              │
                     tools 交付：B5 终止直返 B1（不经本包）
                     summary 交付：本包 Summarize → 答案+引用 → A1
```

入口侧复用 `evidence.EvidencePack.Validate()` 做入口校验（doc_key
非空、evidence 1..8 条、quote ≤200 rune）；`SummaryRequest.QueryID`
与 `Pack.QueryID` 同时非空且不一致视为链路错位，拒绝。

## 边界（不能做什么）

- **不调模型**：`StubSummarizer` 是 dev 替身——只做证据包到答案的
  确定性拼装，`Query` 字段仅供真实实现拼 prompt（stub 透传不用）。
- **tools 交付不经此**：tools 档在 B5 拿到 EvidencePack 即直返 B1，
  本包只服务 summary 档。
- **不做检索与重排**：候选顺序沿用证据包（RRF 序），本包只按序消费。
- **不做持久化 / 网络 / 存储**：纯函数包，可独立测试。
- **不做流式**：本包产出完整 `SummaryResult`；C-6 chat 的流式切分
  在 A1/B1 交付层处理，不进域层。

## 真实化路径（stub → 模型）

stub 与真实实现共享同一 `Summarizer` 接口与 `Validate` 出口闸，替换
步骤：

1. 新增 `real.go`：`RealSummarizer` 持有 D1 `dc-gateway` 的客户端句柄
   （b-online 队列），把 `SummaryRequest.Query` + 候选 quote 组装成
   prompt，调用模型生成带 `[n]` 角标的答案；
2. 模型输出解析为 `SummaryResult` 后照旧跑 `Validate()`——角标与
   引用对不上（模型漏标/幻觉角标）即整单拒绝或重试，闸门不变；
3. 装配处（后续里程碑的 svc 层）把注入的 `NewStub()` 换成真实实现，
   域层与前端格式化零改动。

## 验收

```bash
go vet ./service/search/rpc/internal/summary/...
GOCACHE=/tmp/gocache-b6 go test -race ./service/search/rpc/internal/summary/...
```

接线（B5 出口到 B6 的装配、A1 的流式切分、A7 引用接纳）在后续装配
里程碑完成，本包不依赖它们。
