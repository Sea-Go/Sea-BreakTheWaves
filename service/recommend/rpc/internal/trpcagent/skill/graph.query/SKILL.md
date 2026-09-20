---
name: graph.query
category: graph
description: Cypher 查询执行
tools:
  - neo4j_execute
inputs:
  - name: cypher
    type: string
    required: true
    description: 参数化 Cypher 查询语句
  - name: params
    type: map
    required: false
    description: Cypher 参数绑定键值对
outputs:
  - name: nodes
    type: list
    description: 查询返回的图谱节点列表
---

# graph.query

## 用途
直接执行 Cypher 查询语句，返回图谱节点列表。该 Skill 是图谱侧的"原始查询出口"，供 Agent 在需要灵活、临时、参数化查询时使用，例如人工调试关系、验证 schema、取特定子图做证据。底层对应 `internal/graph` 的 `Client.QueryByCypher(ctx, cypher, params)`，走 Neo4j ExecuteRead 事务包装。

## 调用方式
Agent 通过 tool_call 调用，传入参数化 Cypher 与 `params`。强约束：
- **必须参数化**：用户输入只能通过 `params` 绑定，禁止字符串拼接，避免 Cypher 注入；
- **只读语义**：底层走 ExecuteRead 事务，不承担写入副作用；
- **超时与重试**：由 `graph.Client` 统一管理，调用方只需传 ctx。

典型场景：召回链路需要补一张子图证据、解释链路需要取节点的邻居属性。

## 二开扩展点
- 新增 Cypher 模板（在 `internal/graph/cypher.go` 中追加命名模板，再由本 Skill 暴露）
- 自定义结果解析（把 `[]domain.GraphNode` 转成业务需要的 schema）
- 接入查询白名单 / 配额限流（在 Skill wrapper 层校验 cypher 关键字）
- 切换图存储后端（实现 `domain.GraphQuerier` 接口，替换 Neo4j Client）
