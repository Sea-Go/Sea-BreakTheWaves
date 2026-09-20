---
name: search.suggest
category: search
description: 搜索补全
tools:
  - suggest_lookup
  - trie_match
  - hot_query_lookup
inputs:
  - name: prefix
    type: string
    required: true
    description: 用户输入前缀
  - name: user_id
    type: string
    required: false
    description: 用户 ID（个性化补全）
  - name: topk
    type: int
    required: false
    default: 10
outputs:
  - name: suggestions
    type: list
    description: 补全建议列表（含 query/score/source）
---

# search.suggest

## 用途
根据用户输入前缀返回搜索补全建议，提升搜索效率。建议来源：
- **trie_match**：从 Trie 树前缀匹配历史搜索词
- **hot_query_lookup**：热门查询补全
- **个性化**：结合用户 RecentSearches 个性化

适用搜索框输入实时补全场景。

## 调用方式
Agent 通过 tool_call 调用 `suggest_lookup`，传入 prefix 与 user_id。底层：
1. `trie_match` 前缀匹配
2. `hot_query_lookup` 热门查询补充
3. 按 score 排序取 topk

## 二开扩展点
- 调整 Trie 构建源（全局历史 / 用户历史 / 编辑词表）
- 注入业务领域热词词典
- 通过 user_id 个性化补全顺序
- 实现 Suggester interface 接入第三方补全服务（如 ES suggest）
