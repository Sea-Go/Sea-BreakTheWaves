---
name: profile.tune
category: profile
description: 画像参数调优
tools:
  - profile_tune
  - ab_bucket_assign
  - metric_compare
inputs:
  - name: user_id
    type: string
    required: false
    description: 单用户调优（不传则全局调优）
  - name: param
    type: string
    required: true
    description: 调优参数名（decay_rate/temporal_weight/diversity_lambda）
  - name: strategy
    type: string
    required: false
    default: bayesian
    description: 调优策略（grid/bayesian/online）
outputs:
  - name: before
    type: map
    description: 调优前参数值
  - name: after
    type: map
    description: 调优后参数值
  - name: metric_delta
    type: map
    description: 关键指标变化（ctr/coverage/dwell）
---

# profile.tune

## 用途
调优画像侧参数（衰减率、时间权重、多样性 lambda 等），让画像更贴合业务指标。底层对接 `internal/profile/decay.go` / `trending.go` 的参数与 A/B 分桶。用于：上线新画像策略前做参数搜索、线上指标漂移时自动回调。

## 调用方式
Agent 通过 tool_call 调用，传入 `param` 与 `strategy`。底层流程：
1. 按 `ab_bucket_assign` 把目标用户分到实验桶；
2. 按 strategy 搜索参数候选（grid 遍历 / bayesian 优化 / online 梯度）；
3. 每个候选跑一段窗口，收集 ctr/coverage/dwell；
4. 用 `metric_compare` 对比基线，选最优写入 `profile.tune` 配置；
5. 返回 before/after/metric_delta。

单用户调优（传 `user_id`）只影响该用户；全局调优影响默认参数。

## 二开扩展点
- 新增可调参数（在 `profile` 包暴露 knob，并在本 Skill 注册）
- 接入自定义调优策略（实现 `Tuner` 接口）
- 加严安全护栏（参数变化幅度超阈值需人工审批）
- 接入 `eval.drift` 触发自动回调
