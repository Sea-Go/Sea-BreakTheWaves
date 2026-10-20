# indexer（dev 形态入口）

`service/async/rpc/cmd/indexer` 是 M2 indexer 三段 worker 的 dev 形态可运行
入口（对应部署形态 dev 的 btw-worker-all）：各接缝全部用内存实现，从
JSONL 事件文件读 C-1 镜像事件，跑完整管线并打印 C-2 READY 回执。管线本体
与接缝契约见 [`internal/indexer`](../../internal/indexer/README.md)。

## 装配

| 接缝 | 实现 | 说明 |
| --- | --- | --- |
| `EventSource` | JSONL 文件（`--events`） | 每行一个 release 事件，坏行带行号报错 |
| `Encoder` | `fakeEncoder`（确定性假编码） | dense=sha256 展开 64 维；sparse=词频 hash（fnv32a TermID + `artifact.ImpactWeight` 权重）；multi=前 64 token 复制（每 token 一行 8 维哈希） |
| `Sink` | 内存 map | 落位同时打印 `artifact <key> <n>B` 轨迹 |
| `ReceiptSink` | stdout | 打印 `READY manifest_id=<id>` |
| `SeenStore` | `indexer.NewMemSeen()` | 进程内幂等账本 |

假编码的种子取事件稳定字段（`doc_key`/`structure_ref`/`revision_id` 拼接）：
C-1 事件只含内容引用不含正文，真实 DC 编码器按引用取正文，接缝形状相同；
同一字段必得同一表示（确定性由 `TestFakeEncoderDeterministicAndShaped`
保证）。SIGINT/SIGTERM 触发 context 取消，管线干净退出（无 READY、不记账）。

## 运行示例

```
GOCACHE=/tmp/gocache-m2 go run ./service/async/rpc/cmd/indexer \
  --events service/async/rpc/cmd/indexer/testdata/example-events.jsonl
```

输出形如：

```
indexer dev: events=…/example-events.jsonl count=2 encoder=fake-encoder.v1
artifact 532a7e84322385bedb36ff591951b4c9/dense.v1:96f6… 68B
…
artifact 532a7e84322385bedb36ff591951b4c9/manifest.v1.json 1155B
artifact 532a7e84322385bedb36ff591951b4c9/tree.v1.json 1484B
READY manifest_id=532a7e84322385bedb36ff591951b4c9
…
indexer dev: done lines=2 artifacts=16
```

## 验收

```
GOCACHE=/tmp/gocache-m2 go test -race -count=1 ./service/async/rpc/cmd/indexer/...
```
