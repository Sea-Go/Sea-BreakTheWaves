---
name: explain.recommend
category: explain
description: 推荐解释
tools:
  - explain_recommend
  - evidence_collect
  - llm_explain_generate
inputs:
  - name: user_id
    type: string
    required: true
    description: 目标用户 ID
  - name: article_id
    type: string
    required: true
    description: 被推荐文章 ID
  - name: style
    type: string
    required: false
    default: concise
    description: 解释风格（concise/narrative/bullet）
  - name: max_tokens
    type: int
    required: false
    default: 128
    description: 解释文本最大 token
outputs:
  - name: explanation
    type: string
    description: 可读解释文本
  - name: evidences
    type: list
    description: 证据列表（含来源/权重/原文片段）
  - name: confidence
    type: float
    description: 解释置信度
---

# explain.recommend

## 用途
为一次推荐生成可读解释 + 结构化证据。底层对应 `agent/explain.go`，聚合多路证据：画像匹配（长期偏好/短期意图）、图谱路径（多跳关系）、CF 近邻、内容相似度、规则命中。用于：推荐位展示"为什么推这篇"、运营复盘推荐质量、满足可解释性合规要求。

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id` / `article_id`。底层流程：
1. 调 `evidence_collect` 拉取该 user×article 的所有证据源：
   - 画像侧：偏好标签命中、时段偏好匹配；
   - 图谱侧：`graph.recall` 的 path；
   - CF 侧：相似用户 / 相似文章；
   - 内容侧：向量相似度、BM25 命中关键词；
2. 按权重排序证据，取 TopK；
3. 调 `llm_explain_generate` 把证据转成自然语言（按 style）；
4. 返回 `explanation` / `evidences` / `confidence`。

## 二开扩展点
- 新增证据源（实现 `explain.EvidenceProvider` 接口）
- 自定义解释 prompt 模板（按业务话术改写）
- 接入解释缓存（同 user×article 短时不重复生成）
- 证据可信度分级（图谱路径 > CF > 内容，按来源加权 confidence）
