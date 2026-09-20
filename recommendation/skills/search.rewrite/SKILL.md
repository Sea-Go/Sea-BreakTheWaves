---
name: search.rewrite
category: search
description: 查询改写扩展
tools:
  - llm_call
  - query_expand
  - synonym_lookup
inputs:
  - name: query
    type: string
    required: true
    description: 原始查询
  - name: intent
    type: object
    required: false
    description: 搜索意图（来自 search.understand）
  - name: expand_n
    type: int
    required: false
    default: 3
    description: 扩展查询数量
outputs:
  - name: rewritten_query
    type: string
    description: 主重写查询
  - name: expanded_queries
    type: list
    description: 扩展查询列表
---

# search.rewrite

## 用途
对用户查询做改写与扩展，提升搜索召回率：
- **改写**：纠正错别字、补全省略、规范化表述
- **扩展**：同义词替换、相关词补充、生成多视角子查询

适用场景：
- 用户查询过短或含错别字
- 意图为 comparative 时拆分为多子查询
- 长尾 query 召回不足时扩展

## 调用方式
Agent 通过 tool_call 调用：
1. `synonym_lookup` 查同义词表
2. `query_expand` 基于规则扩展
3. `llm_call` 生成语义改写

输出主重写查询（rewritten_query）+ 扩展查询列表（expanded_queries），后续可并行检索后融合。

## 二开扩展点
- 替换 synonym_lookup 同义词源（业务词典/Embedding 近邻）
- 调整 expand_n 控制扩展数量
- 注入业务领域改写规则（如缩写展开）
- 通过 LLM prompt_template 自定义改写风格
