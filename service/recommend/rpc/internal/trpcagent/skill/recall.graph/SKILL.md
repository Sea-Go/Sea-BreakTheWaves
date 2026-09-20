---
name: recall.graph
category: recall
description: 知识图谱多跳召回
tools:
  - graph_query
  - cypher_generate
  - entity_link
inputs:
  - name: user_id
    type: string
    required: true
    description: 用户 ID
  - name: pattern
    type: string
    required: true
    description: 召回模式（user_like_similar/user_similar_like/co_occurrence/entity/article_author/ip）
  - name: hop
    type: int
    required: false
    default: 2
    description: 多跳跳数
  - name: topk
    type: int
    required: false
    default: 50
outputs:
  - name: candidates
    type: list
    description: 图谱召回候选列表（含 graph_score）
  - name: graph_trace
    type: object
    description: 图谱查询链路（Cypher/结果数/耗时）
---

# recall.graph

## 用途
基于 Neo4j 知识图谱做多跳召回，挖掘显式关系（作者/IP/标签/实体共现）下的候选文章。与 CF（行为相似）与 content（语义相似）互补，覆盖"关系近"的候选。

支持 6 种召回 pattern：
- **user_like_similar**：用户点赞文章的相似文章
- **user_similar_like**：相似用户喜欢的文章
- **co_occurrence**：标签/实体共现文章
- **entity**：实体关联文章
- **article_author**：同作者文章
- **ip**：同 IP 系列文章

## 调用方式
Agent 通过 tool_call 调用，指定 `pattern` 与 `hop`。底层 GraphRecaller 调用 `cypher_generate` 生成 Cypher → `graph_query` 执行 → 返回候选。GraphTrace 写入 RecommendResponse.GraphTrace 供观测。

## 二开扩展点
- 在 graph/cypher.go 中新增 Cypher 模板支持新 pattern
- 调整 hop 参数控制召回广度（hop 越大召回越广但噪声越多）
- 替换 GraphClient（如切到 NebulaGraph / HugeGraph）
- 通过 entity_link 注入自定义实体识别模型
