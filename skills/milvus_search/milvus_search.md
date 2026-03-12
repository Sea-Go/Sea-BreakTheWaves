# 技能：Milvus 向量检索（milvus_search）

## 目的

在 Milvus 中做向量检索，服务于推荐系统的 **粗召回 / 精召回** 两阶段检索：

- **粗召回向量**：标题 + 封面 + 手打 type 标签类型 + 关键词检测 + 各类二级标题  
  用于：快速筛出文章级候选（article_id）
- **精召回向量**：二级标题 + 段落内容（包括图片和文字）  
  用于：在候选文章中定位更具体的证据（chunk_id）

## 入参（JSON）

```json
{
  "mode": "coarse | fine",
  "query_text": "检索短文本（由意图/记忆生成）",
  "topk": 10,
  "filter": "可选：Milvus 表达式过滤"
}
```

## 出参（JSON）

```json
{
  "returned_doc_count": 8,
  "empty": false,
  "coverage_score": 0.63,
  "hits": [
    {"id":"a1","article_id":"a1","similarity":0.88},
    {"id":"a2","article_id":"a2","similarity":0.81}
  ],
  "collection": "recall_coarse",
  "latency_ms": 48
}
```

## 观测（日志 + Trace）

该技能会额外打出一条 `retrieval.completed` span/event（与你的观测 schema 对齐），包含：

- returned_doc_count / empty / coverage_score
- decision.type = retrieve，chosen = vector，reason_codes = NEED_GROUNDING

这样你可以在 Jaeger 里看到：

- `invoke_agent`（根）
  - `execute_tool.milvus_search`
    - `retrieval.completed`（检索摘要）

## 关键实现点

1. **向量化**：对 `query_text` 调用 embedding（`embedding/service.TextVector`）得到 float32 向量  
2. **Milvus Search**：`WithVectorFieldName("vector")` + `WithMetricType(COSINE|IP|L2)`  
3. **结果解析**：
   - coarse：`id == article_id`
   - fine：`id == chunk_id`，并从 `{article_id}#chunk_x` 解析 article_id
4. **覆盖度摘要**：`coverage_score = topK 平均相似度`
