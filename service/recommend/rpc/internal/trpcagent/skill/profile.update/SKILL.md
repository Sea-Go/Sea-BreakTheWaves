---
name: profile.update
category: profile
description: 更新用户画像
tools:
  - profile_upsert
  - behavior_profile_merge
inputs:
  - name: user_id
    type: string
    required: true
    description: 目标用户 ID
  - name: patch
    type: map
    required: true
    description: 画像增量 patch（按字段合并）
  - name: source
    type: string
    required: false
    default: agent
    description: 更新来源（agent/event/periodic/manual）
outputs:
  - name: profile
    type: map
    description: 更新后的全量画像
  - name: version
    type: int
    description: 画像版本号
---

# profile.update

## 用途
增量更新用户画像，是画像侧的"写入入口"。底层对应 `internal/profile` 的更新逻辑与 `internal/repo.UserRepo` 持久化。支持字段级 patch 合并，避免全量覆盖丢历史。常被 `event.hook_register` 中的 OnlineFeedbackHook 调用，把行为事件转成画像增量。

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id` 与 `patch`。底层流程：
1. 加载当前画像（走 `profile.load` 缓存）；
2. 按 `behavior_profile_merge` 策略合并 patch（行为计数累加、偏好权重衰减后加权）；
3. 递增 `version`，写回 `UserRepo`；
4. 失效本地缓存，返回最新画像。

合并语义：行为计数单调累加；偏好标签按 `decay` 衰减后并入；短期意图覆盖。

## 二开扩展点
- 自定义合并策略（实现 `profile.Merger` 接口，替换默认加权合并）
- 接入版本冲突检测（乐观锁，version 不匹配则拒绝）
- 异步批量写入（攒批降 DB 压力）
- 按 source 做审计日志（记录谁改了什么）
