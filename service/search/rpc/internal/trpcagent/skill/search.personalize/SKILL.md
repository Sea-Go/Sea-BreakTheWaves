---
name: search.personalize
category: search
description: 个性化搜索
tools:
  - profile_lookup
  - personalized_rerank
inputs:
  - name: query
    type: string
    required: true
    description: 查询文本
  - name: user_id
    type: string
    required: true
    description: 用户 ID
  - name: candidates
    type: list
    required: true
    description: 待个性化排序的候选列表
  - name: topk
    type: int
    required: false
    default: 20
outputs:
  - name: candidates
    type: list
    description: 个性化排序后候选列表
---

# search.personalize

## 用途
基于用户画像对搜索结果做个性化重排，让相同 query 的不同用户看到差异化结果。结合：
- StaticProfile（注册兴趣/偏好类型/难度）
- DynamicProfile（当前兴趣主题）
- BehaviorProfile（近期点击/点赞/收藏）
- TemporalProfile（长期/短期/会话兴趣）

适用场景：用户登录态下的搜索。

## 调用方式
Agent 通过 tool_call 调用：
1. `profile_lookup` 拉取用户完整画像
2. `personalized_rerank` 按画像特征对 candidates 重排

与 rerank.* 的区别：search.personalize 强调画像特征驱动，rerank.* 强调模型驱动；二者可串联（先 rerank 精排，再 personalize 个性化）。

## 二开扩展点
- 调整画像特征权重影响个性化强度
- 注入自定义画像字段（如 VIP 等级/地域）
- 通过 TemporalProfile 控制长短期兴趣平衡
- 实现 PersonalizedReranker interface 接入自研模型
