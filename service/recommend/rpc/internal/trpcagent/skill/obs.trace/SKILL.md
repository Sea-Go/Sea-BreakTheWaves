---
name: obs.trace
category: obs
description: 链路查询
tools:
  - trace_query
  - span_get
  - trace_search
inputs:
  - name: trace_id
    type: string
    required: false
    description: 精确 trace ID 查询
  - name: user_id
    type: string
    required: false
    description: 按用户检索近期 trace
  - name: skill
    type: string
    required: false
    description: 按技能名过滤 span
  - name: window
    type: string
    required: false
    default: 1h
    description: 检索时间窗
outputs:
  - name: traces
    type: list
    description: trace 列表（含 span 树）
  - name: spans
    type: list
    description: 命中的 span 明细
---

# obs.trace

## 用途
查询推荐链路的 trace（Jaeger / OpenTelemetry）。底层对应 `internal/obs/callbacks.go` 与 `infra/otel_init.go`、`zlog/AgentTrace.go`。推荐一次请求会打 `invoke_agent`（根）→ recall / rank / rerank / explain / tool 各层 span，含 `retrieval.completed` / `rank.completed` / `side_effect.*` 等关键事件。用于：定位延迟尖刺、复盘推荐决策链、排查"为什么没推某篇"。

## 调用方式
Agent 通过 tool_call 调用：
- 传 `trace_id`：精确取一条 trace 的完整 span 树（`trace_query`）；
- 传 `user_id` + `window`：检索该用户近期所有 trace（`trace_search`）；
- 传 `skill`：在该 trace 内过滤出指定 skill 的 span（`span_get`）。

返回 `traces` 列表与 `spans` 明细，每个 span 含 name / duration / tags / logs。

## 二开扩展点
- 新增业务 span（在二开 skill 中打自定义 span，自动被采集）
- 接入 trace 采样率调整（高流量时降采样）
- trace 异常检测（某 span 耗时超阈值自动告警）
- trace 与日志联动（按 trace_id 关联 zlog 日志）
