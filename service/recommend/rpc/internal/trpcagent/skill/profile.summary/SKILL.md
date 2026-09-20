---
name: profile.summary
category: profile
description: 画像摘要
tools:
  - profile_summarize
  - llm_compress
inputs:
  - name: user_id
    type: string
    required: true
    description: 目标用户 ID
  - name: style
    type: string
    required: false
    default: concise
    description: 摘要风格（concise/narrative/bullet）
  - name: max_tokens
    type: int
    required: false
    default: 256
    description: 摘要最大 token 数
outputs:
  - name: summary
    type: string
    description: 用户画像可读摘要
  - name: tags
    type: list
    description: 提炼的偏好标签
---

# profile.summary

## 用途
把结构化用户画像压缩成可读摘要 + 偏好标签列表。用于：解释链路给用户看"我们为什么推"、运营后台人工核查画像质量、Agent 间传递紧凑画像。底层走 `llm_compress`，把 BehaviorProfile / TemporalProfile / TrendingProfile 喂给 LLM 生成自然语言摘要。

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id` 与 `style`。底层流程：
1. 调 `profile.load` 拉全量画像；
2. 组装 prompt（画像 JSON + 风格指令 + token 限制）；
3. 调 LLM 生成摘要与标签；
4. 返回 `summary` 文本与 `tags` 列表。

摘要会带时间戳，便于追溯"这是哪个时间点的画像"。

## 二开扩展点
- 自定义摘要 prompt 模板（按业务话术改写）
- 接入摘要缓存（同一画像版本不重复调 LLM）
- 多风格预生成（一次生成 concise/narrative/bullet 三种，按场景取用）
- 标签后处理（去重、归并到业务标签体系）
