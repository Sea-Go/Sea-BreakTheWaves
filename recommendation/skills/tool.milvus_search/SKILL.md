---
name: tool.milvus_search
category: tool
description: Milvus 向量搜索
tools:
  - milvus_search
  - embedding_text_vector
inputs:
  - name: mode
    type: string
    required: true
    description: 检索模式（coarse/fine）
  - name: query_text
    type: string
    required: true
    description: 检索文本（由意图/记忆生成）
  - name: topk
    type: int
    required: false
    default: 10
    description: 返回候选数
  - name: filter
    type: string
    required: false
    description: Milvus 表达式过滤
outputs:
  - name: hits
    type: list
    description: 命中列表（含 id/article_id/similarity）
  - name: coverage_score
    type: float
    description: 覆盖度分数
  - name: latency_ms
    type: int
    description: 检索耗时
---

# tool.milvus_search

## 用途
在 Milvus 中做向量检索，服务于推荐系统的粗召回 / 精召回两阶段。是 `recall.content` 的底层工具，也可被 Agent 直接调用做临时检索。底层对应 `skills/milvus_search/milvus_search.tool.go` 与 `embedding/service.TextVector`。两路向量：
- **粗召回向量**：标题 + 封面 + 标签 + 关键词 + 二级标题（定位 article_id）
- **精召回向量**：二级标题 + 段落内容（定位 chunk_id）

## 调用方式
Agent 通过 tool_call 调用，传入 `mode` / `query_text` / `topk`。底层流程：
1. 对 `query_text` 调 `embedding_text_vector` 得 float32 向量；
2. 按 mode 选 collection（recall_coarse / recall_precise）；
3. 执行 Milvus Search（`WithVectorFieldName("vector")` + `WithMetricType(COSINE)`）；
4. 解析结果：coarse 时 `id==article_id`；fine 时从 `{article_id}#chunk_x` 解析 article_id；
5. 计算 `coverage_score`（topK 平均相似度）；
6. 打 `retrieval.completed` span（与观测 schema 对齐）。

## 二开扩展点
- 切换 embedding 模型（替换 `embedding/service.TextVector`）
- 调整 metric type（COSINE/IP/L2）
- 新增过滤表达式生成器（按业务规则构造 filter）
- 接入多 collection 路由（按频道 / 语言分库）
