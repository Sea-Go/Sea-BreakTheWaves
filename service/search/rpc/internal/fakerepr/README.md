# fakerepr —— dev 假编码器核心（纯域层）

`service/search/rpc/internal/fakerepr` 是 dev 形态的确定性假编码器：
把一段文本编码成三路检索表示（dense 64 维 / sparse 词频 hash / multi
每行 8 维、至多前 64 token）。**镜像 `service/async/rpc/cmd/indexer`
的 fake.go 口径**（package main 无法导入；先例见 cmd/retr_eval 的镜像
声明）——同文本必得同表示，两侧改动必须双边同步。

## 消费方

| 消费方 | 种子 | 用途 |
| --- | --- | --- |
| `internal/devseed` | `doc_key‖structure_ref‖revision_id` | 文档侧编码 → M2 工件量化落位（与 cmd/indexer 同构） |
| `internal/pipeline`（FakeEncoder） | 查询文本 | 查询侧编码（真实查询编码器的 dev 替身） |
| `cmd/retr_eval` | gold 文档清单文本 | gold 查询的 sparse 表示 |

## API

| 入口 | 说明 |
| --- | --- |
| `Encode(text) Repr` | 三路原始表示（Terms 按 TermID 升序；TermID 冲突后键覆盖，与 indexer 同口径） |
| `Repr.Rows(max)` | multi 平铺矩阵按行切分（至多保留前 max 行） |
| `Repr.SparseMap()` | Terms → `map[TermID]权重`（检索侧查询形态） |
| `SparseMapOfText(text)` | 直接产出 sparse 查询 map（gold 查询等单路消费） |
| `Tokenize(s)` | 按非字母数字切分并小写化（与 indexer 同口径） |
| 常量 `EncoderID/DenseDim/MultiDim/MultiRows` | `fake-encoder.v1` / 64 / 8 / 64 |

## 边界与真实化

- 纯哈希推导：不调模型、无 IO、无状态、并发安全（有测试钉死确定性）；
- 种子空间由调用方决定（文档侧 key 种子 / 查询侧文本种子 / 评测侧
  gold 种子）——不同种子空间的 sparse term 不相交属预期；
- 真实化：DC representation 编码器替换（同 encoder_id 契约），本包
  退役为回归对照。

## 验收

```sh
go vet ./service/search/rpc/internal/fakerepr/...
GOCACHE=/tmp/gocache-e2e go test -race ./service/search/rpc/internal/fakerepr/...
```
