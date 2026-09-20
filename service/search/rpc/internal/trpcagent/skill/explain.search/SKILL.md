---
name: explain.search
category: explain
description: 搜索解释
tools:
  - explain_search
  - query_intent_parse
  - retrieval_evidence_collect
inputs:
  - name: query
    type: string
    required: true
    description: 用户搜索串
  - name: article_id
    type: string
    required: true
    description: 命中的文章 ID
  - name: style
    type: string
    required: false
    default: concise
    description: 解释风格
outputs:
  - name: explanation
    type: string
    description: 可读解释文本
  - name: matched_terms
    type: list
    description: 命中关键词列表
  - name: semantic_score
    type: float
    description: 语义相似度
---

# explain.search

## 用途
为一次搜索命中生成可读解释，告诉用户"这篇为什么被搜出来"。与 `explain.recommend` 的区别：搜索解释的证据源以 query 为主（关键词命中、语义匹配、意图对齐），而非用户画像。底层对应 `agent/content_search_agent.go` 的解释逻辑。

## 调用方式
Agent 通过 tool_call 调用，传入 `query` / `article_id`。底层流程：
1. 调 `query_intent_parse` 解析 query 意图（找作者/找主题/找具体文章）；
2. 调 `retrieval_evidence_collect` 收集证据：
   - BM25 关键词命中位置（高亮片段）；
   - 向量相似度分数；
   - 图谱实体链接命中（query 中的实体出现在文章标签 / 作者节点）；
3. 调 LLM 生成自然语言解释（按 style）；
4. 返回 `explanation` / `matched_terms` / `semantic_score`。

## 二开扩展点
- 新增证据源（如"文章权威度"/"时效性"）
- 自定义高亮片段生成策略
- 接入意图分类模型，按意图走不同解释模板
- 搜索解释缓存（同 query×article 短时不重复生成）
