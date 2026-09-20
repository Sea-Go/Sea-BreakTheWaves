---
name: obs.metric
category: obs
description: 指标查询
tools:
  - metric_query
  - metric_aggregate
inputs:
  - name: metric
    type: string
    required: true
    description: 指标名（ctr/dwell/coverage/qps/latency_p99/pool_size）
  - name: window
    type: string
    required: false
    default: 1h
    description: 查询时间窗
  - name: group_by
    type: string
    required: false
    description: 分组键（channel/user_id/skill）
  - name: step
    type: string
    required: false
    default: 1m
    description: 采样步长
outputs:
  - name: series
    type: list
    description: 指标时间序列
  - name: summary
    type: map
    description: 聚合统计（avg/p50/p99/min/max）
---

# obs.metric

## 用途
查询推荐系统的运行指标（业务指标 + 系统指标）。底层对应 `metrics/metrics.go` 与 `internal/obs`。用于：实时大盘监控、`eval.drift` 的数据源、二开业务方看自己频道的指标健康度。

## 调用方式
Agent 通过 tool_call 调用，传入 `metric` / `window`。底层流程：
1. 调 `metric_query` 从指标库拉原始序列；
2. 若传 `group_by`，调 `metric_aggregate` 做分组聚合；
3. 按 `step` 重采样；
4. 计算 summary（avg/p50/p99/min/max）；
5. 返回 `series` 与 `summary`。

支持的业务指标：ctr / dwell / coverage / diversity / pool_size；系统指标：qps / latency_p99 / error_rate / cost。

## 二开扩展点
- 新增业务指标（在 `metrics.go` 注册，并登记到本 Skill）
- 自定义聚合维度（如按"新用户/老用户"分群）
- 接入指标告警（超阈值自动通知）
- 指标下钻（从总指标下钻到某频道 / 某 skill）
