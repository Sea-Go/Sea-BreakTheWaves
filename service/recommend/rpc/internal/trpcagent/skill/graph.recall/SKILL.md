---
name: graph.recall
category: graph
description: 图谱多跳召回
tools:
  - graph_multi_hop_recall
  - user_like_neighbor
  - similar_user_like
inputs:
  - name: user_id
    type: string
    required: true
    description: 目标用户 ID
  - name: hops
    type: int
    required: false
    default: 2
    description: 多跳深度（1-3）
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
    description: 图谱召回候选列表（含 graph_score / path）
---

# graph.recall

## 用途
基于图谱多跳关系召回候选文章，作为内容召回 / CF 召回之外的"关系证据"补充路径。底层对应 `internal/graph` 的 `Client.RecallByGraph(ctx, req)`，覆盖三类典型多跳模板：
- **user_like_neighbor**：用户 → 喜欢 → 文章 → 相似 → 文章
- **similar_user_like**：用户 → 相似用户 → 喜欢 → 文章
- **tag_co_occurrence**：文章 → 标签 ← 文章 共现

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id` 与 `hops`。底层会按 `GraphRecallRequest` 组装多模板 Cypher 并发执行，结果按 `graph_score` 排序、去重、截断 topk。每条候选会带 `path` 字段，供解释链路回溯"为什么推这篇"。

## 二开扩展点
- 新增多跳模板（在 `cypher.go` 的模板表中追加，并在 `RecallByGraph` 中分发）
- 调整多跳权重（在结果合并阶段加权 `graph_score`）
- 接入频道隔离（按 channel 过滤节点）
- 与 `recall.cf` / `recall.content` 做 RRF 融合（在 recall/registry.go 中编排）
