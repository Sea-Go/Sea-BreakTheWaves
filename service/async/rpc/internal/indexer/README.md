# indexer — M2 三段 worker 进程骨架

`service/async/rpc/internal/indexer` 把已就绪的三个域包装配成可运行的
indexer 三段 worker：**取事件 → 逐文档编码量化并组装 manifest → 工件落位
+ 树构建 → C-2 READY 回执**。本包只做编排；传输与计算全部走可替换接缝，
真实 DC 事件、真实编码器与对象存储后续接入（dev 形态入口见
`service/async/rpc/cmd/indexer`）。

## 职责

| 能力 | 入口 | 说明 |
| --- | --- | --- |
| 事件消费 | `Run(ctx)` / `HandleEvent(ctx, ev)` | `Run` 消费 `EventSource` 流至 `io.EOF`；单事件失败即返回（事件可重试，重试策略归外层） |
| 三路编码编排 | `process` | 逐 doc 调 `Encoder` → `artifact.QuantizeF32` / `EncodeImpact` / `QuantizeMulti` → 组 `DocEntry` |
| manifest 组装 | `artifact.CanonicalJSON` / `ManifestID` | 内容寻址 id；`AssignID` 后经 `Validate` 把关 |
| 工件落位 | `Sink.Put` | 键 = `manifest_id` 前缀 + 内容寻址 ref（见下） |
| 检索树构建 | `tree.Build` | 用 `DeterministicSummarizer` stub（`Summarizer` 接缝可换真实 LLM），树工件同前缀落位 |
| C-2 回执 | `ReceiptSink.Ready` | 最终提交点：全部工件（含树）落位后才 emit |
| 幂等 | `SeenStore.Claim/Release` | 处理过的 `event_id` 重放直接返回 nil；失败撤销记账可重试 |
| 结构树校验 | `StructureSource`（可选） | 按 `structure_ref` 取 TreeJSON，校验 `revision_id` 与事件一致 |

## 接缝图

```
              C-1 事件流                    三路原始表示                工件字节
 ┌──────────┐   Next    ┌─────────┐  Encode  ┌─────────┐ Quantize  ┌────────┐  Put
 │   事件    │ ────────▶ │         │ ───────▶ │         │ ────────▶ │        │ ──────▶  对象键
 │ (真实 DC  │           │ Pipeline│          │ Encoder │  (artifact│  Sink  │  manifest_id/
 │  后续接)  │           │         │          │ (假/真实)│   包编解码)│        │  <ref>
 └──────────┘           └────┬────┘          └─────────┘           └────────┘
      ▲ EventSource           │ tree.Build(DeterministicSummarizer stub)
      │                       ▼                                     ┌──────────┐
      │                 ┌────────────┐                    tree JSON │ ReceiptSink│ Ready
 ┌──────────┐           │ 检索树      │ ───────────────────────────▶│ (C-2 回执)│──────▶ READY
 │ SeenStore│◀──────────│ (tree 域)  │                              └──────────┘
 └──────────┘  Claim/   └────────────┘
               Release   ┌──────────────┐
               (幂等账本) │StructureSource│ (可选：structure_ref → TreeJSON
                        └──────────────┘  revision 一致性校验)
```

## 流程与不半提交

1. 字段级校验（C-1 契约：三个 ID 非空、docs 非空、doc 三字段非空、
   `char_len>0`）→ `SeenStore.Claim`（已记账直接返回 nil）。
2. 逐 doc：可选结构树校验 → `Encoder.Encode` → 形状校验（dense 非空、
   multi 是 `MultiDim` 整倍数且行数 ∈ [1,2048]、encoder_id 一致非空）→
   三路量化 → `DocEntry`（ref 内容寻址，`budget_bytes` = 三路载荷字节数，
   `source_chars` = `char_len`）。
3. `artifact.ManifestID` 定 id → 全部 doc 载荷按 `manifest_id/<ref>` 落位 →
   `manifest.v1.json` → `tree.Build` → `tree.v1.json` → `ReceiptSink.Ready`。

**不半提交**：所有工件键都带 `manifest_id` 前缀；READY 是最终提交点，任何
一步失败（含任一 doc 编码失败、context 取消）都不 emit READY 且撤销账本，
整事件可重试。manifest_id 与键都是内容的纯函数，重试必重写同一批键，
`Sink` 的覆盖写即幂等补齐。顺序说明：任务箭头图中 READY 位于树构建之前，
本实现以"失败事件不 emit READY"不变式为准，把 READY 放在树工件落位之后。

## 契约

- `ReleaseEvent`/`ReleaseDoc`：C-1 事件镜像（JSON snake_case，
  `event_id/module_id/release_id/published_at/docs[]{doc_key,revision_id,
  content_sha256,char_len,structure_ref}`）；`event_id` 不参与 manifest。
- `Repr`：`Dense []float32`、`Sparse []artifact.Term`、
  `Multi []float32 + MultiDim`（行主序，行数 = `multi_tokens`），
  与 artifact 包量化输入一一对应。
- 工件键：`ObjectKey(manifestID, ref) = manifestID + "/" + ref`，其中
  `ref = "{dense|sparse|multi}.v1:" + hex(sha256(载荷))[:32]`；
  `ManifestKey` / `TreeKey` 为固定名对象。给定 manifest 后 ref → 键是
  纯函数。
- 结构树：`structure.go` 镜像 evalseed TreeJSON 契约（源头
  `service/search/rpc/internal/evidence/types.go`，跨服务 internal 不可
  导入），改动需双边同步。
- `MemSeen`：进程内账本，重启即失忆；跨进程幂等由持久化实现替换。

## 边界（明确不做）

- **不做传输**：事件从哪来（DC/文件）、回执到哪去、对象字节存哪，全部是
  接缝注入。
- **不做编码**：不调用任何向量化模型，Encoder 是接缝。
- **不做重试策略**：失败向上返回，退避/重放/死信归外层装配。
- **不做装载生效**：`Switcher.Load/Activate` 的装载路径不在本管线内。

## 验收

```
GOCACHE=/tmp/gocache-m2 go test -race -count=1 ./service/async/rpc/internal/indexer/... ./service/async/rpc/cmd/indexer/...
GOCACHE=/tmp/gocache-m2 go vet ./service/async/rpc/internal/indexer/... ./service/async/rpc/cmd/indexer/...
```

dev 运行示例（完整管线 + READY 回执）：

```
GOCACHE=/tmp/gocache-m2 go run ./service/async/rpc/cmd/indexer --events service/async/rpc/cmd/indexer/testdata/example-events.jsonl
```
