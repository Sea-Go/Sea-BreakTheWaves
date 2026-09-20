---
name: eval.drift
category: eval
description: 漂移检测
tools:
  - eval_drift_detect
  - distribution_compare
  - drift_alert
inputs:
  - name: metric
    type: string
    required: true
    description: 监控指标（ctr/dwell/coverage/diversity/pool_size）
  - name: window
    type: string
    required: false
    default: 1h
    description: 当前窗口时长
  - name: baseline_window
    type: string
    required: false
    default: 24h
    description: 基线窗口时长
  - name: threshold
    type: float
    required: false
    default: 0.1
    description: 漂移判定阈值（相对偏差）
outputs:
  - name: drifted
    type: bool
    description: 是否检测到漂移
  - name: current
    type: float
    description: 当前窗口指标值
  - name: baseline
    type: float
    description: 基线窗口指标值
  - name: direction
    type: string
    description: 漂移方向（up/down）
---

# eval.drift

## 用途
检测线上指标是否相对基线发生漂移。底层对应 `internal/eval` 的漂移检测逻辑，对比当前窗口与基线窗口的指标分布。用于：在线监控推荐质量，发现画像失效、召回池腐败、模型衰减等问题时及时告警 / 自动回调 `profile.tune`。

## 调用方式
Agent 通过 tool_call 调用，传入 `metric` 与 `window`。底层流程：
1. 从 `obs.metric` 拉当前窗口与 `baseline_window` 的指标序列；
2. 调 `distribution_compare` 做分布对比（均值 / 方差 / KL 散度）；
3. 相对偏差超过 `threshold` 则判漂移；
4. 漂移时调 `drift_alert` 推送告警，并可选触发 `profile.tune` 自动回调；
5. 返回 `drifted` / `current` / `baseline` / `direction`。

## 二开扩展点
- 新增可监控指标（在 `eval.drift` 注册表中登记）
- 自定义漂移检测算法（如 ADWIN / Page-Hinkley）
- 接入自动回调（漂移时自动跑 `profile.tune` 或 `eval.run`）
- 漂移分级（轻微 / 中度 / 严重，对应不同处置策略）
