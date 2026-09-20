---
name: event.feedback
category: event
description: 反馈事件
tools:
  - feedback_submit
  - quality_judge_record
inputs:
  - name: user_id
    type: string
    required: true
    description: 用户 ID
  - name: feedback_type
    type: string
    required: true
    description: 反馈类型（like/dislike/not_interested/report_quality/inline_edit）
  - name: target_type
    type: string
    required: true
    description: 反馈对象类型（article/recommendation_list/explanation）
  - name: target_id
    type: string
    required: true
    description: 反馈对象 ID
  - name: reason
    type: string
    required: false
    description: 反馈理由文本
outputs:
  - name: feedback_id
    type: string
    description: 反馈记录 ID
  - name: applied
    type: bool
    description: 是否已应用到画像/模型
---

# event.feedback

## 用途
上报用户显式反馈（点赞 / 点踩 / 不感兴趣 / 质量举报），并即时应用到画像与模型。是行为事件（`event.report`）的"显式信号"特化版，权重高于隐式行为。底层对应 `internal/event` 的 OnlineFeedbackHook + QualityFeedbackHook。用于：用户主动表达偏好时快速纠偏推荐方向。

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id` / `feedback_type` / `target_type` / `target_id`。底层流程：
1. 组装反馈事件（标记为显式反馈，权重高于普通行为）；
2. 调 `feedback_submit` 落库；
3. 触发 OnlineFeedbackHook → 更新画像偏好 / 负向权重；
4. 若 `feedback_type=report_quality`，额外触发 QualityFeedbackHook → `quality_judge_record` 进质量评估缓冲；
5. 返回 `feedback_id` 与 `applied`。

## 二开扩展点
- 新增反馈类型（如"已读不感兴趣"/"内容过期"）
- 接入 LLM 解析 reason 文本，转成结构化标签
- 反馈权重分级（dislike 比 not_interested 影响更深远）
- 反馈防滥用（同 user 短时大量 dislike 触发风控）
