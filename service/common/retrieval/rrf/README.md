# rrf

Reciprocal Rank Fusion 的**共享实现**（唯一实现点，`shared_symbol_test.go` 强制）。

## 为什么存在

2026-10-08 复用审计前，搜索侧（`search/internal/retrieval/fusion.go`）与推荐侧（`recommend/internal/model/recall/content_recaller.go`）各有一份私有 RRF，Milvus Go SDK 的 `NewRRFReranker` 是第三处（服务端形态）。本包把 RRF 上提为共享实现：搜索侧经类型适配消费（fusion.go 的 var 转发），推荐侧与 Milvus 适配器后续可指向同一实现。

## API

| 项 | 说明 |
| --- | --- |
| `RRF(rankLists [][]Entry, k int) []Fused` | 融合多路候选：每路贡献 1/(k+rank)，rank 从 1 起 |
| `KDefault = 60` | 默认平滑常数（业界惯例，与 Milvus RRFReranker 默认一致） |
| `Entry{Key, Score}` | 单路候选（分数仅用于路内排序，融合只用名次） |
| `Fused{Key, Score}` | 融合结果（按分降序、平局 Key 字典序升序，确定性） |

## 语义约定

- 未排序的路内列表先按分数降序（平局 Key 升序）确定名次，再融合
- 未出现在某路的文档不贡献该路分数
- `k<=0` 时取 `KDefault`
- 空输入 / 全空列表返回 nil
