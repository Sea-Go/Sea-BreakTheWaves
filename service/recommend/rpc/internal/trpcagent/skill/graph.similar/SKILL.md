---
name: graph.similar
category: graph
description: 相似节点查询
tools:
  - similar_nodes_query
  - user_like_similar
  - similar_user_like
inputs:
  - name: node_id
    type: string
    required: true
    description: 种子节点 ID（user/article/tag）
  - name: node_type
    type: string
    required: true
    description: 种子节点类型
  - name: relation
    type: string
    required: false
    default: similar
    description: 关系类型（similar/visually_similar/co_occurrence）
  - name: topk
    type: int
    required: false
    default: 20
outputs:
  - name: nodes
    type: list
    description: 相似节点列表（含 node_id/type/score）
---

# graph.similar

## 用途
给定一个种子节点，查询图谱中与之相似的节点列表。是图谱侧的"单跳近邻"通用查询，比 `graph.recall` 更轻量（不做多跳、不组装候选）。底层复用 `internal/graph` 的 `RecallUserLikeSimilar` / `RecallSimilarUsersLike` 等单跳模板。用于：找相似作者、相似标签、相似文章。

## 调用方式
Agent 通过 tool_call 调用，传入 `node_id` / `node_type` / `relation`。底层按 relation 分发到对应 Cypher 模板，返回单跳邻居及其相似度。结果按 score 降序截断 topk。

典型场景：
- 给一篇文章找相似文章做"相关推荐"；
- 给一个用户找相似用户做 CF 兜底；
- 给一个标签找共现标签做标签扩展。

## 二开扩展点
- 新增关系类型（在 `cypher.go` 中追加模板，并在分发逻辑中注册）
- 调整相似度阈值（过滤低质量邻居）
- 接入节点属性过滤（如只返回某频道的文章节点）
- 与 `graph.recall` 组合：先 similar 找邻居，再 recall 多跳扩展
