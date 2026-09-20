---
name: eval.regression
category: eval
description: 回归测试
tools:
  - eval_regression_run
  - baseline_diff
  - regression_alert
inputs:
  - name: baseline_version
    type: string
    required: true
    description: 基线版本号
  - name: candidate_version
    type: string
    required: true
    description: 待测版本号
  - name: tolerance
    type: float
    required: false
    default: 0.02
    description: 指标允许波动阈值
  - name: dataset
    type: string
    required: false
    default: baseline
    description: 回归数据集
outputs:
  - name: passed
    type: bool
    description: 是否通过回归
  - name: diffs
    type: list
    description: 各指标对比明细
  - name: regressions
    type: list
    description: 超过 tolerance 的退化指标
---

# eval.regression

## 用途
对比两个版本的评估结果，判定是否存在指标退化。底层对应 `cmd/regrdiff` 与 `storage/reco_evaluation_repo.go` 的历史版本对比。用于：每次策略 / 模型变更后强制跑回归，阻止退化版本上线。是 CI/CD 中推荐质量门禁的核心环节。

## 调用方式
Agent 通过 tool_call 调用，传入 `baseline_version` / `candidate_version`。底层流程：
1. 从 `reco_evaluation_repo` 拉两个版本的评估结果；
2. 调 `baseline_diff` 逐指标对比（ndcg/recall/ctr/coverage/diversity）；
3. 对波动超过 `tolerance` 的指标，加入 `regressions` 列表；
4. 若 `regressions` 非空，调 `regression_alert` 推送告警；
5. 返回 `passed`（regressions 为空则 true）与 `diffs`。

## 二开扩展点
- 自定义退化判定规则（如 ndcg 退化必须告警，coverage 退化可容忍）
- 接入多版本对比（一次对比多个候选版本，选最优）
- 回归报告可视化（生成趋势图）
- 与 CI 联动（push 时自动跑回归，未通过阻断 merge）
