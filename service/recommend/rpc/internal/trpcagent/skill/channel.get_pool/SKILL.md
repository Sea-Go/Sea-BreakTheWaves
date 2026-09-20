---
name: channel.get_pool
category: channel
description: 获取频道召回池
tools:
  - channel_pool_get
  - pool_size_query
inputs:
  - name: channel
    type: string
    required: true
    description: 频道名
  - name: user_id
    type: string
    required: false
    description: 按用户隔离的池视图
  - name: include_meta
    type: bool
    required: false
    default: false
    description: 是否返回池元信息（容量/水位/上次补充时间）
outputs:
  - name: pool
    type: list
    description: 召回池候选列表
  - name: meta
    type: map
    description: 池元信息（include_meta=true 时返回）
---

# channel.get_pool

## 用途
读取某频道的召回池内容（候选列表 + 元信息）。底层对应 `internal/channel` 与 `storage.PoolRepo`。用于：Agent 决策"池子还够不够用、要不要触发补充"、调试时人工查看池内容、解释链路回溯"这篇是从哪个池出来的"。

## 调用方式
Agent 通过 tool_call 调用，传入 `channel`。底层流程：
1. 从 PoolRepo 取该频道的候选池（可选按 `user_id` 隔离视图）；
2. 若 `include_meta=true`，附加池元信息（容量、当前水位、上次 `pool_refill` 时间、平均分）；
3. 返回 `pool` 列表与可选 `meta`。

池内容按候选分排序，已去重、已过滤黑名单。

## 二开扩展点
- 自定义池视图（按用户分群返回不同子池）
- 接入池快照（返回某一历史时刻的池内容，便于复盘）
- 增加池健康度评分（水位 + 多样性 + 新鲜度）
- 与 `channel.ensure_pool` 联动，水位低于阈值时自动触发补充
