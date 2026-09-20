---
name: profile.temporal
category: profile
description: 时间画像查询
tools:
  - temporal_profile_query
  - time_bucket_select
inputs:
  - name: user_id
    type: string
    required: true
    description: 目标用户 ID
  - name: bucket
    type: string
    required: false
    default: d1
    description: 时间桶（h1/d1/w1/weekend）
  - name: metric
    type: string
    required: false
    default: preference
    description: 查询指标（preference/active_hours/ctr）
outputs:
  - name: temporal
    type: map
    description: 时间分桶画像（含桶名/偏好/活跃度）
---

# profile.temporal

## 用途
查询用户在特定时间桶下的画像，捕捉"时段性偏好"。底层对应 `internal/domain.TemporalProfile` 与 `internal/profile` 的分桶逻辑。用于：周末 / 工作日 / 深夜等时段做差异化推荐，避免把工作日白天的偏好硬套到周末晚上。

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id` 与 `bucket`。底层流程：
1. 按 `time_bucket_select` 解析桶名（h1/d1/w1/weekend）；
2. 从 `TemporalProfile` 取该桶下的偏好向量与行为统计；
3. 若该桶数据稀疏，回退到 d1 全天桶并打 `fallback=true`；
4. 返回 `temporal` 画像。

典型场景：召回前先按当前时段选桶，把对应偏好喂给 `recall.content` 做向量召回。

## 二开扩展点
- 新增自定义桶（如"午休"/"通勤"）
- 调整桶回退策略（稀疏桶如何降级）
- 接入实时时段感知（按用户本地时间而非服务器时间）
- 与 `profile.tune` 联动，按桶调权重
