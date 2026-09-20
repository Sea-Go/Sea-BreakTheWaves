---
name: rerank.llm
category: rerank
description: LLM rerank
tools:
  - ai_rerank
  - llm_call
inputs:
  - name: candidates
    type: list
    required: true
    description: 待重排候选列表（含 article_id 与元信息）
  - name: user_id
    type: string
    required: false
    description: 用户 ID（用于拉取画像）
  - name: prompt_template
    type: string
    required: false
    description: 自定义 prompt 模板
  - name: topk
    type: int
    required: false
    default: 20
outputs:
  - name: candidates
    type: list
    description: LLM 重排后候选列表（含 reason）
  - name: overall_confidence
    type: float
    description: 整体置信度
---

# rerank.llm

## 用途
调用 LLM（如 Qwen）对候选文章做精排，输出排序结果 + 每条理由 + overall_confidence。对应 RecommendConfig.RerankModel=llm。

LLM 从 PG 拉取用户记忆（long_term/short_term/periodic）与候选文章元信息（title/type_tags/tags/score），构造 prompt 让模型综合判断。

适用场景：
- 候选数较少（≤20）时的最终精排
- 需要可解释推荐理由（RecommendResponse.Explain）
- 复杂意图（Intent.Complexity=complex）

## 调用方式
Agent 通过 tool_call 调用 `ai_rerank`，传入 `candidates` 与 `user_id`。底层 skills/rerank/ai_rerank.tool.go 构造 prompt → `llm_call` 调用模型 → 解析 JSON 输出（ranked 列表 + overall_confidence）。

## 二开扩展点
- 替换 prompt_template 注入业务领域知识
- 调整 LLM 模型（qwen-max / qwen-plus / gpt-4）
- 通过 BestOfN（RecommendConfig.BestOfN）多次采样取最优
- 实现 Reranker interface 接入其他 LLM（如 Claude/Gemini）
