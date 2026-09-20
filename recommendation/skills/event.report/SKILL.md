---
name: event.report
category: event
description: 上报行为事件
tools:
  - event_report
  - event_persist
inputs:
  - name: user_id
    type: string
    required: true
    description: 用户 ID
  - name: event_type
    type: string
    required: true
    description: 事件类型（click/like/favorite/dwell/share/dislike）
  - name: article_id
    type: string
    required: false
    description: 关联文章 ID
  - name: payload
    type: map
    required: false
    description: 事件附加字段（dwell_ms/position/channel 等）
  - name: timestamp
    type: int
    required: false
    description: 事件发生时间戳（毫秒，不传取服务端时间）
outputs:
  - name: event_id
    type: string
    description: 事件唯一 ID
  - name: accepted
    type: bool
    description: 是否被系统接受
---

# event.report

## 用途
上报用户行为事件，是推荐系统"反馈闭环"的入口。底层对应 `internal/event` 与 `internal/domain.BehaviorEvent`。事件会被多个 Hook 消费：OnlineFeedbackHook 更新画像、CFFeedbackHook 写图谱边、RerankFeedbackHook 更新精排模型、EvalHook 进评估缓冲、MetricsHook 进指标。一次上报，多路消费。

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id` / `event_type` 与可选 `article_id` / `payload`。底层流程：
1. 组装 `domain.BehaviorEvent`（含时间戳、来源、trace_id）；
2. 调 `event_persist` 落库（user_rec_history）；
3. 同步触发注册的 Hook 链（顺序执行，失败不阻塞主链路，仅记日志）；
4. 返回 `event_id` 与 `accepted`。

事件类型受白名单约束，未知类型会被拒绝（`accepted=false`）。

## 二开扩展点
- 新增事件类型（在 `domain.BehaviorEvent` 类型枚举中注册）
- 注册自定义 Hook（通过 `event.hook_register` 接入业务逻辑）
- 接入异步 Kafka 上报（高流量场景把同步落库改成投递）
- 加严事件校验（防刷：同 user+article 短时去重）
