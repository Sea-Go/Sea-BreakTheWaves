---
name: search.graph
category: search
description: 图谱知识搜索
tools:
  - graph_query
  - cypher_generate
  - entity_link
  - graph_knowledge_expand
inputs:
  - name: query
    type: string
    required: true
    description: 查询文本
  - name: entities
    type: list
    required: false
    description: 预识别实体（来自 search.understand）
  - name: hop
    type: int
    required: false
    default: 2
    description: 多跳跳数
  - name: topk
    type: int
    required: false
    default: 20
outputs:
  - name: graph_knowledge
    type: object
    description: GraphKnowledge（entities/edges/articles）
  - name: candidates
    type: list
    description: 图谱关联文章候选列表
---

# search.graph

## 用途
基于知识图谱的搜索增强，输出 GraphKnowledge：
- **entities**：查询实体及其关联实体
- **edges**：实体间关系（HAS_TAG/WROTE_BY/BELONGS_IP/CO_OCCURRED_WITH）
- **articles**：图谱关联文章 ID

适用场景：
- 搜索意图为 comparative 时（如"A vs B"）
- 实体明显的查询（如作者名/IP 名）
- 需要知识背景补全的长尾查询

与 recall.graph 的区别：search.graph 强调查询实体驱动的知识扩展，recall.graph 强调用户行为驱动的候选召回。

## 调用方式
Agent 通过 tool_call 调用：
1. `entity_link` 识别查询实体（若未预识别）
2. `cypher_generate` 生成图谱查询 Cypher
3. `graph_query` 执行返回 entities/edges/articles
4. `graph_knowledge_expand` 扩展关联知识

## 二开扩展点
- 调整 hop 控制知识扩展深度
- 在 cypher_generate 中新增查询模板
- 替换 GraphClient 实现切换图谱后端
- 通过 graph_knowledge_expand 注入业务知识库
