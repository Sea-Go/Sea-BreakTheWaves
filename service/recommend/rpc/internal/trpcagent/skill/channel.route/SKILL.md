---
name: channel.route
category: channel
description: 频道路由
tools:
  - channel_route
  - channel_filter_isolate
inputs:
  - name: user_id
    type: string
    required: true
    description: 目标用户 ID
  - name: intent
    type: string
    required: false
    description: 用户意图（activity/browse/search）
  - name: explicit_channel
    type: string
    required: false
    description: 用户显式指定的频道
outputs:
  - name: channel
    type: string
    description: 路由命中的频道名
  - name: reason
    type: string
    description: 路由决策原因
  - name: fallback
    type: bool
    description: 是否走了兜底频道
---

# channel.route

## 用途
根据用户意图 / 画像 / 显式选择，把请求路由到合适的频道。底层对应 `internal/channel` 的路由策略与 `internal/session` 的 `ChannelFilter.Isolate`（把频道键注入 ctx，下游召回 / 排序自动隔离）。用于：活动期间自动切到活动频道、垂类浏览切到垂类频道、无明确意图走默认频道。

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id` 与可选 `intent` / `explicit_channel`。底层流程：
1. 若传 `explicit_channel`，直接命中并校验该频道是否 active；
2. 否则按 `intent` + 用户画像匹配频道路由表；
3. 命中后调 `channel_filter_isolate` 把 channel 写入 ctx；
4. 未命中走默认频道并打 `fallback=true`；
5. 返回 `channel` / `reason` / `fallback`。

下游所有召回 / 排序 / 池操作都会从 ctx 取频道键做隔离。

## 二开扩展点
- 自定义路由规则（实现 `channel.Router` 接口，按业务规则匹配）
- 接入 LLM 意图识别（intent 由模型判定，再喂给路由）
- 频道切换平滑（路由变更时清理旧频道 ctx，避免跨频道串池）
- 路由决策日志（记录每条路由的原因，便于复盘）
