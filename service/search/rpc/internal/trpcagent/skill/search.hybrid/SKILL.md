---
name: search.hybrid
category: search
description: Milvus hybrid 搜索
tools:
  - milvus_search
  - bm25_search
  - rrf_fusion
inputs:
  - name: query
    type: string
    required: true
    description: 查询文本
  - name: user_id
    type: string
    required: false
    description: 用户 ID（个性化）
  - name: filter
    type: object
    required: false
    description: SearchFilter 过滤条件
  - name: topk
    type: int
    required: false
    default: 20
outputs:
  - name: hits
    type: list
    description: 搜索命中列表（含 article_id/score/source）
  - name: rewritten_query
    type: string
    description: 重写后查询（如有）
  - name: graph_knowledge
    type: object
    description: 图谱知识（如有）
---

# search.hybrid

## 用途
Milvus 向量 + Postgres BM25 混合搜索，是搜索主路径。与 recall.content 的区别：
- search.hybrid 强调精准命中（topk 较小，默认 20）
- 支持完整 SearchFilter（标签/作者/频道/时间/质量阈值）
- 输出 SearchResult（含 rewritten_query 与 graph_knowledge）

适用用户主动搜索场景。

## 调用方式
Agent 通过 tool_call 调用：
1. `milvus_search` 向量检索
2. `bm25_search` 关键词检索
3. `rrf_fusion` 融合两路结果
4. 按 SearchFilter 过滤后返回 hits

## 二开扩展点
- 调整 RRF k 参数影响融合效果
- 新增检索源（如 image_search / graph_search）
- 通过 filter 实现业务过滤逻辑
- 替换 milvus_search 的 metric_type（COSINE/IP/L2）
