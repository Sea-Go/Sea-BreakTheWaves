---
name: profile.load
category: profile
description: 加载用户画像
tools:
  - profile_load
  - behavior_profile_get
inputs:
  - name: user_id
    type: string
    required: true
    description: 目标用户 ID
  - name: fields
    type: list
    required: false
    description: 指定加载字段（不传则全量）
  - name: cache_ttl
    type: int
    required: false
    default: 300
    description: 画像缓存秒数
outputs:
  - name: profile
    type: map
    description: 用户画像（含长期偏好/短期意图/行为统计）
---

# profile.load

## 用途
加载用户画像，是推荐 Agent 的"读画像入口"。底层对应 `internal/profile` 与 `internal/domain.UserProfile`，聚合 BehaviorProfile（RecentClicks/RecentLikes/RecentFavorites）、TrendingProfile、TemporalProfile 等多源信号。供召回 / 排序 / 解释链路统一引用，避免各模块重复拉取。

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id`。底层流程：
1. 先查本地缓存（按 `cache_ttl`）；
2. 未命中则从 `internal/repo.UserRepo` 拉取长期画像 + 行为统计；
3. 联合 `TrendingProfile`（24h 突增）与 `TemporalProfile`（时间分桶）组装完整画像；
4. 回写缓存并返回。

可通过 `fields` 限定返回字段，降低序列化开销。

## 二开扩展点
- 扩展画像字段（在 `domain.UserProfile` 中新增，并在 loader 中填充）
- 替换缓存后端（默认本地 LRU，可换 Redis）
- 接入实时画像流（Kafka 行为事件触发增量更新）
- 按 channel 隔离画像（不同频道维护独立偏好桶）
