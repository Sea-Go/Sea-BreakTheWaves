# pipeline

端到端检索管线：B2 planner → B3 retriever → B5 evidence → B6 summary。

## 职责
把 B 域四个域包串联为一步到位的 `Execute(ctx, req)`；tools 交付在 evidence 组装后直返，summary 交付继续调 Summarizer 并格式化引用。

## 组件注入
| 字段 | 接口 | 默认（NewDefaultPipeline） |
| --- | --- | --- |
| Planner | planner.Planner | tierPlanner（fast→Identity / balanced→Rule / deep→Decomposer） |
| Searcher | *retrieval.Searcher | NewSearcher(store) |
| Summarizer | summary.Summarizer | StubSummarizer |

## 边界
- 不做 B4 fusion——B3.Searcher 已含 RRF 融合
- 不做流式——A1 的 SSE 切分属装配层
- 纯域层，无 IO

## 验收
`go test -race -count=1 ./service/search/rpc/internal/pipeline/...`
