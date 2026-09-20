---
name: search.understand
category: search
description: 搜索意图理解
tools:
  - llm_call
  - entity_link
  - intent_classify
inputs:
  - name: query
    type: string
    required: true
    description: 原始查询
  - name: user_id
    type: string
    required: false
    description: 用户 ID（结合画像理解意图）
outputs:
  - name: intent
    type: object
    description: SearchIntent（label/entities/time_intent）
  - name: complexity
    type: string
    description: 复杂度（simple/medium/complex）
---

# search.understand

## 用途
理解用户搜索意图，输出 SearchIntent：
- **label**：意图类型（informational/navigational/transactional/comparative）
- **entities**：识别实体（Author/IP/Tag/Title/Topic）
- **time_intent**：时效意图（如"最新"/"2024年"）

驱动后续搜索路径决策：
- simple → 直接 hybrid 搜索
- medium → 改写 + hybrid
- complex → 改写 + 图谱 + hybrid + 个性化

## 调用方式
Agent 通过 tool_call 调用：
1. `entity_link` 识别查询中的实体
2. `intent_classify` 分类意图标签
3. `llm_call` 综合判断复杂度与时效意图

## 二开扩展点
- 替换 intent_classify 模型（规则/小模型/LLM）
- 注入业务领域实体词典提升 entity_link 精度
- 调整 complexity 决策阈值影响路径选择
- 通过 user_id 结合画像个性化意图理解
