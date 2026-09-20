---
name: channel.ensure_pool
category: channel
description: 确保频道池就绪
tools:
  - channel_pool_ensure
  - pool_refill_async
  - pool_size_query
inputs:
  - name: channel
    type: string
    required: true
    description: 频道名
  - name: min_size
    type: int
    required: false
    default: 100
    description: 池最低水位
  - name: refill_topk
    type: int
    required: false
    default: 200
    description: 触发补充时拉取的候选数
  - name: async
    type: bool
    required: false
    default: true
    description: 是否异步补充
outputs:
  - name: ready
    type: bool
    description: 池是否已达到 min_size
  - name: current_size
    type: int
    description: 当前池大小
  - name: refill_triggered
    type: bool
    description: 是否触发了补充
---

# channel.ensure_pool

## 用途
确保某频道的召回池水位达标，不达标则触发补充。底层对应 `poolrefill/` 的异步补充调度与 `storage.PoolRepo`。用于：推荐请求前的"预检"，避免池子空了才临时召回导致延迟尖刺；也用于活动 / 大流量前的预热。

## 调用方式
Agent 通过 tool_call 调用，传入 `channel` 与 `min_size`。底层流程：
1. 调 `pool_size_query` 取当前水位；
2. 若 `current_size >= min_size`，直接返回 `ready=true`；
3. 否则按 `async` 决定：
   - async=true：投递到 `pool_refill_async` 异步补充，立即返回 `refill_triggered=true`；
   - async=false：同步执行 `pool_manage.NewPoolRefill`，阻塞到补充完成；
4. 返回 `ready` / `current_size` / `refill_triggered`。

## 二开扩展点
- 自定义水位阈值（按频道 / 时段动态调整 min_size）
- 接入预热策略（活动前定时 ensure，避免峰值时触发）
- 补充失败告警（refill 多次失败仍不达标时告警）
- 与 `channel.route` 组合，路由前先 ensure 目标频道
