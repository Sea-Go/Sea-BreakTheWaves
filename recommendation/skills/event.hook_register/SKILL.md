---
name: event.hook_register
category: event
description: 注册 Hook
tools:
  - event_hook_register
  - hook_priority_set
inputs:
  - name: name
    type: string
    required: true
    description: Hook 唯一名
  - name: event_types
    type: list
    required: false
    description: 订阅的事件类型（不传则订阅全部）
  - name: priority
    type: int
    required: false
    default: 100
    description: 执行优先级（数值小先执行）
  - name: handler_ref
    type: string
    required: true
    description: Handler 引用名（已注册的二开处理器标识）
outputs:
  - name: registered
    type: bool
    description: 是否注册成功
  - name: hook_id
    type: string
    description: Hook 实例 ID
---

# event.hook_register

## 用途
注册一个事件 Hook，订阅指定类型的行为事件。底层对应 `internal/event` 的 Hook 注册机制（`builtin_hooks.go` 已内置 LogHook/MetricsHook/EvalHook/OnlineFeedbackHook/CFFeedbackHook/RerankFeedbackHook/QualityFeedbackHook）。用于：业务方二开时把自定义逻辑（写外部表、调第三方、触发工作流）挂到事件流上，无需改主链路。

## 调用方式
Agent 通过 tool_call 调用，传入 `name` / `handler_ref` 与可选 `event_types` / `priority`。底层流程：
1. 校验 `handler_ref` 在 Hook 工厂中已注册；
2. 实例化 Hook，按 `event_types` 配置过滤器；
3. 调 `hook_priority_set` 把它插入 Hook 链（按 priority 排序）；
4. 返回 `registered` 与 `hook_id`。

注册后，每次 `event.report` 都会按链顺序触发该 Hook。Hook 失败不阻塞主链路，但会记 `obs.trace`。

## 二开扩展点
- 实现自定义 Hook（实现 `event.Hook` 接口，在工厂中注册 handler_ref）
- 调整 Hook 优先级（保证关键 Hook 先执行）
- 接入异步 Hook（耗时的外部调用走 goroutine，不拖慢事件上报）
- Hook 熔断（连续失败时自动降级，避免拖垮事件链）
