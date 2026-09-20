---
name: recall.channel
category: recall
description: 频道独立池召回
tools:
  - channel_route
  - pool_fetch
inputs:
  - name: user_id
    type: string
    required: true
    description: 用户 ID
  - name: channel
    type: string
    required: true
    description: 频道名
  - name: topk
    type: int
    required: false
    default: 50
outputs:
  - name: candidates
    type: list
    description: 频道独立池召回候选列表
  - name: pool_key
    type: string
    description: 命中的独立池键
---

# recall.channel

## 用途
按频道独立召回池召回候选文章。每个频道有独立的：
- 召回池（PoolSize）
- 质量阈值（QualityThreshold）
- Rerank 模型版本（RerankModel）
- 画像 filterKey（频道隔离）

确保不同频道的推荐结果互不污染（如"科技"频道不出现"娱乐"内容）。

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id` 与 `channel`。底层 ChannelRecaller：
1. `channel_route` 解析频道元信息与 pool_key/filter_key
2. `pool_fetch` 从独立池取 topk 候选

频道独立池由 pool_refill 异步补充。

## 二开扩展点
- 通过 ChannelManager.RegisterChannel 注册自定义频道
- 调整 Channel.PoolSize / QualityThreshold / RerankModel 独立参数
- 替换 PoolRepo 实现自定义池存储（如 Redis ZSet）
- 在 async_pool_refill_dispatcher.go 中调整补充策略
