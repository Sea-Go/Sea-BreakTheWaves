---
name: search.history
category: search
description: 搜索历史
tools:
  - history_query
  - history_aggregate
inputs:
  - name: user_id
    type: string
    required: true
    description: 用户 ID
  - name: action
    type: string
    required: false
    default: list
    description: 操作（list/clear/delete/aggregate）
  - name: limit
    type: int
    required: false
    default: 20
    description: 返回数量（list 场景）
  - name: keyword
    type: string
    required: false
    description: 删除场景的目标关键词
outputs:
  - name: history
    type: list
    description: 搜索历史列表
  - name: aggregated_topics
    type: list
    description: 聚合主题（aggregate 场景）
---

# search.history

## 用途
管理用户搜索历史，支持：
- **list**：列出最近搜索词
- **clear**：清空搜索历史
- **delete**：删除单条历史
- **aggregate**：聚合历史为兴趣主题（驱动画像更新）

搜索历史写入 BehaviorProfile.RecentSearches，影响后续召回与个性化。

## 调用方式
Agent 通过 tool_call 调用 `history_query`，指定 action。底层从 user_history_repo 拉取/修改历史记录。aggregate 场景调用 `history_aggregate` 聚合为主题分布。

## 二开扩展点
- 调整历史保留策略（如按时间/按数量淘汰）
- 注入自定义聚合算法（如 LLM 主题抽取）
- 通过 event_publish 联动画像更新（BehaviorEvent）
- 实现自定义存储后端（如 Redis / Postgres）
