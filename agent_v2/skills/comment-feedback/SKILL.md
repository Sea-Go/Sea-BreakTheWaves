---
name: comment-feedback
description: 分析旅游攻略评论，并提炼成稳定的写作策略。Use when the task is to turn article comments into reusable writing improvements for future generations.
---

# Comment Feedback

## 目标

从评论中提炼可复用的写作策略，而不是复述情绪。

## 分析重点

- 读者喜欢的结构和表达方式
- 读者反复指出的信息缺口
- 高频追问，例如交通、预算、亲子、老人适配
- 需要减少的写法，例如流水账、空泛形容词、无关背景

## 输出要求

- `positive_signals`
- `negative_signals`
- `high_frequency_questions`
- `writing_improvements`
- `strategy_memory_update`

## 记忆规则

- 只保留稳定的改进策略
- 不要把单条极端评论升级为全局规则
- 高频问题可写入 `unanswered_questions`
