---
name: graph.entity_link
category: graph
description: 实体链接
tools:
  - entity_link_lookup
  - alias_resolve
inputs:
  - name: query
    type: string
    required: true
    description: 待链接的自然语言文本
  - name: topk
    type: int
    required: false
    default: 5
    description: 每个提及返回的候选实体数
  - name: entity_types
    type: list
    required: false
    description: 限定实体类型（如 author/tag/article）
outputs:
  - name: entities
    type: list
    description: 命中的实体列表（含 entity_id/type/score）
---

# graph.entity_link

## 用途
把自然语言文本中的提及链接到图谱实体节点，是图谱召回与解释链路的"前置锚点"。底层对应 `internal/graph` 的 `Client.EntityLink(ctx, query)`，结合别名表与模糊匹配定位 `domain.Entity`。典型用途：把用户搜索串或画像关键词对齐到图谱节点，再走 `graph.recall` 多跳扩展。

## 调用方式
Agent 通过 tool_call 调用，传入 `query` 与可选的 `entity_types`。底层流程：
1. 对 query 做分词 / 别名归一；
2. 在图谱中按 `alias_resolve` 查候选实体；
3. 按相似度与别名置信度排序、截断 topk；
4. 返回 `entities` 列表（含 `entity_id` / `type` / `score`）。

## 二开扩展点
- 扩展别名词典（在 `entity_link.go` 中维护 alias→entity 映射）
- 接入 LLM 做消歧（多候选时让模型选最贴合上下文的实体）
- 限定实体类型（通过 `entity_types` 过滤，避免误链到非目标类型）
- 缓存热门 query 的链接结果，降低图谱查询压力
