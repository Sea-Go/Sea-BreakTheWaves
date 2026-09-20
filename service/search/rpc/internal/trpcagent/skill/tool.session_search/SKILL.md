---
name: tool.session_search
category: tool
description: 会话搜索
tools:
  - session_search
  - session_summary_get
  - filterkey_resolve
inputs:
  - name: user_id
    type: string
    required: true
    description: 用户 ID
  - name: query
    type: string
    required: false
    description: 会话内检索关键词
  - name: channel
    type: string
    required: false
    description: 限定频道会话
  - name: window
    type: string
    required: false
    default: 1h
    description: 会话时间窗
outputs:
  - name: sessions
    type: list
    description: 命中的会话片段列表
  - name: summary
    type: string
    description: 会话摘要
---

# tool.session_search

## 用途
在用户会话内做检索 / 取摘要。底层对应 `internal/session` 的 `memory.go` / `summary.go` / `filterkey.go`。会话是"同一用户在某频道某时间窗内"的连续交互单元，承载短期上下文。用于：召回时排除"本轮已看过"、解释链路引用"刚聊过什么"、跨轮推荐保持连贯性。

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id` 与可选 `query` / `channel` / `window`。底层流程：
1. 调 `filterkey_resolve` 按 user×channel 解析会话过滤键；
2. 在 `window` 时间窗内取会话事件流；
3. 若传 `query`，调 `session_search` 做关键词 / 向量混合检索命中片段；
4. 调 `session_summary_get` 生成会话摘要；
5. 返回 `sessions` 列表与 `summary`。

## 二开扩展点
- 自定义会话切分策略（按空闲间隔 / 按频道切换）
- 接入会话级去重（本轮看过的不再推）
- 会话摘要缓存（同会话短时不重复生成）
- 跨会话兴趣迁移（旧会话兴趣如何影响新会话）
