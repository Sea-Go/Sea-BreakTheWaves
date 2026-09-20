---
name: channel.register
category: channel
description: 注册新频道
tools:
  - channel_register
  - recall_pool_init
inputs:
  - name: name
    type: string
    required: true
    description: 频道名（唯一标识）
  - name: description
    type: string
    required: false
    description: 频道描述
  - name: recall_strategy
    type: string
    required: false
    default: hybrid
    description: 召回策略（hybrid/cf/content/rule）
  - name: init_pool
    type: bool
    required: false
    default: true
    description: 是否立即初始化召回池
outputs:
  - name: channel
    type: map
    description: 已注册的频道元信息
  - name: pool_ready
    type: bool
    description: 召回池是否就绪
---

# channel.register

## 用途
注册一个新频道，并可选立即初始化其召回池。底层对应 `internal/channel.ChannelManager.RegisterChannel`。用于：业务方二开新业务线（如"活动频道"/"垂类频道"）时把它纳入推荐系统的频道治理体系，让该频道拥有独立的召回池、排序策略与规则配置。

## 调用方式
Agent 通过 tool_call 调用，传入 `name` 与 `recall_strategy`。底层流程：
1. 校验频道名唯一性；
2. 在 ChannelManager 中登记 `ChannelMeta`（含策略、状态、创建时间）；
3. 若 `init_pool=true`，调 `recall_pool_init` 创建该频道的空召回池；
4. 返回频道元信息与 `pool_ready` 状态。

注册后该频道即可被 `channel.list` / `channel.route` / `channel.ensure_pool` 使用。

## 二开扩展点
- 自定义频道默认策略（按 name 前缀匹配注入不同 strategy）
- 接入频道审批流（注册后需人工 enable 才生效）
- 频道模板复用（从已有频道克隆配置）
- 注册时自动绑定默认规则池（hot/latest/editorial）
