---
name: recall.content
category: recall
description: 向量+BM25 内容召回
tools:
  - milvus_search
  - bm25_search
  - rrf_fusion
inputs:
  - name: query
    type: string
    required: true
    description: 查询文本或用户兴趣拼接串
  - name: topk
    type: int
    required: false
    default: 50
    description: 召回候选数量
  - name: channel
    type: string
    required: false
    description: 频道隔离键
outputs:
  - name: candidates
    type: list
    description: 召回候选列表（含 article_id/score/source）
---

# recall.content

## 用途
基于用户查询或兴趣，通过 Milvus 向量召回 + Postgres BM25 召回 + RRF 融合，返回候选文章列表。该 Skill 是内容召回主路径，覆盖语义相似（向量）与关键词命中（BM25）两路证据，避免任一单路遗漏。

## 调用方式
Agent 通过 tool_call 调用此 skill，传入 `query` 与 `topk`。底层串接：
1. `milvus_search`（粗召回向量）拿到 article_id 列表与相似度；
2. `bm25_search`（Postgres ts_vector 倒排）拿到关键词命中列表；
3. `rrf_fusion` 按 RRF 公式 `1/(k+rank)` 融合两路结果。

## 二开扩展点
- 替换 VectorRepo 实现自定义向量源（如切换到 Faiss / Qdrant）
- 调整 RRF k 参数（默认 k=60，业务可在 config.yaml 覆盖）
- 新增融合策略（如 weighted_fusion / combsum），实现 Fusion interface 注入
- 替换 embedding 模型（embedding/service.TextVector）影响向量召回质量
