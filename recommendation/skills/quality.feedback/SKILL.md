---
name: quality.feedback
category: quality
description: 质量反馈提交
tools:
  - feedback_submit
  - event_publish
inputs:
  - name: article_id
    type: string
    required: true
    description: 文章 ID
  - name: user_id
    type: string
    required: true
    description: 反馈用户 ID
  - name: feedback_type
    type: string
    required: true
    description: 反馈类型（quality_too_low/quality_too_high/misgraded/spam/biased）
  - name: comment
    type: string
    required: false
    description: 反馈详情
  - name: expected_grade
    type: string
    required: false
    description: 期望等级
outputs:
  - name: feedback_id
    type: string
    description: 反馈记录 ID
  - name: status
    type: string
    description: 提交状态（pending/accepted/rejected）
---

# quality.feedback

## 用途
提交用户/编辑对文章质量评分的反馈，用于：
- 修正 LLM Judge 评分偏差
- 标记 spam/biased 内容
- 触发质量重评任务

反馈进入质量反馈库，定期聚合驱动 rubric 迭代与模型微调。

## 调用方式
Agent 通过 tool_call 调用 `feedback_submit`，传入反馈详情。底层：
1. 写入反馈库
2. `event_publish` 发 BehaviorEvent（event_type=quality_feedback）
3. HookRegistry 中注册的 Hook 可消费该事件触发重评

## 二开扩展点
- 注册 Hook 监听 quality_feedback 事件触发自动重评
- 调整 feedback_type 枚举扩展业务反馈类型
- 注入自定义反馈聚合策略（如按作者聚合）
- 通过 event_publish 联动 Kafka 通知下游（数据团队）
