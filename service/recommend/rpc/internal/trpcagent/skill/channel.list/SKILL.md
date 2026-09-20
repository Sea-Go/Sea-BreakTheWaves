---
name: channel.list
category: channel
description: 频道列表
tools:
  - channel_list
inputs:
  - name: scope
    type: string
    required: false
    default: all
    description: 范围（all/active/user）
  - name: user_id
    type: string
    required: false
    description: scope=user 时必传
outputs:
  - name: channels
    type: list
    description: 频道列表（含 name/description/status）
---

# channel.list

## 用途
列出系统中已注册的频道。底层对应 `internal/channel` 的 ChannelManager。用于：Agent 决策"该走哪个频道"、运营后台展示频道清单、用户端展示可切换频道。频道是召回池 / 排序策略 / 规则配置的隔离单元。

## 调用方式
Agent 通过 tool_call 调用，传入 `scope`：
- **all**：返回所有已注册频道（含下线的）；
- **active**：只返回状态为 active 的频道；
- **user**：返回某用户当前可访问的频道（需传 `user_id`，按权限过滤）。

返回的每条频道含 `name` / `description` / `status` / `recall_pool_size`。

## 二开扩展点
- 扩展频道元信息（在 `channel.ChannelMeta` 中新增字段）
- 接入频道权限模型（按用户分群决定可见频道）
- 增加频道排序（按热度 / 最近活跃）
- 缓存 active 频道列表，降低 ChannelManager 查询压力
