---
name: tool.pool_manage
category: tool
description: 协程池管理
tools:
  - pool_size_get
  - pool_refill
  - pool_pop_topk
inputs:
  - name: action
    type: string
    required: true
    description: 操作类型（get_size/refill/pop_topk）
  - name: channel
    type: string
    required: true
    description: 频道名
  - name: topk
    type: int
    required: false
    default: 10
    description: pop_topk 时取出的数量
  - name: user_id
    type: string
    required: false
    description: 按用户隔离的池操作
outputs:
  - name: result
    type: map
    description: 操作结果（含 size/popped/refilled）
---

# tool.pool_manage

## 用途
对频道召回池做管理操作：查大小、补充、弹出 TopK。底层对应 `skills/pool_manage/pool_manage.tool.go` 与 `poolrefill/` 的补充调度。是 `channel.ensure_pool` / `channel.get_pool` 的底层工具，也可被 Agent 直接调用做精细池操作。把"召回池"作为可观测、可补充、可弹出的有状态资源暴露给 Agent。

## 调用方式
Agent 通过 tool_call 调用，传入 `action` / `channel`：
- **get_size**：返回池当前大小（`pool_size_get`）；
- **refill**：触发补充（`pool_refill`，调 recall 链路拉新候选入池）；
- **pop_topk**：弹出 TopK 候选（`pool_pop_topk`，从池顶取走并删除）。

可选 `user_id` 做用户隔离视图。补充是异步的（除非显式要求同步）。

## 二开扩展点
- 新增池操作（如"清理低分候选"/"按标签过滤池"）
- 自定义补充策略（按频道配不同 recall 链）
- 接入池快照与回滚（误操作后恢复）
- 池操作审计日志（记录谁在何时 pop/refill 了哪个池）
