---
name: tool.memory_manage
category: tool
description: Memory 管理
tools:
  - user_memory_get
  - user_memory_upsert
  - memory_maintain_window
  - memory_chunk_hybrid_search
inputs:
  - name: action
    type: string
    required: true
    description: 操作类型（get/upsert/maintain_window/chunk_search）
  - name: user_id
    type: string
    required: true
    description: 用户 ID
  - name: memory_type
    type: string
    required: false
    description: 记忆类型（short_term/long_term/periodic）
  - name: payload
    type: map
    required: false
    description: upsert/maintain 时的载荷
outputs:
  - name: result
    type: map
    description: 操作结果（依 action 而定）
---

# tool.memory_manage

## 用途
管理用户记忆（长期 / 短期 / 周期）。底层对应 `skills/memory_manage/memory_manage.tool.go` 与 `internal/session/memory.go`。是画像之外更细粒度的"记忆层"，承载可向量化的记忆片段，供召回链路做意图向量生成。四个动作：
- **get**：读取某类记忆
- **upsert**：写入 / 更新记忆（long_term/periodic 会自动 tokenize + 向量化入 Milvus）
- **maintain_window**：按 1d/7d 窗口聚合行为生成摘要写回
- **chunk_search**：对记忆 chunks 做向量 + 关键词混合检索

## 调用方式
Agent 通过 tool_call 调用，传入 `action` / `user_id` 与对应载荷：
- get：传 `memory_type`，返回该类记忆原文；
- upsert：传 `memory_type` + `payload.text`，自动切块入 Milvus；
- maintain_window：传 `window`(1d/7d) + `target_memory_type`，拉行为聚合摘要；
- chunk_search：传 `query` + `topk`，对 user_memory_chunks 做混合检索。

记忆 chunks 存 PG（原文）+ Milvus（向量），保证可追溯 + 可检索。

## 二开扩展点
- 新增记忆类型（如"活动期记忆"/"兴趣探索期记忆"）
- 自定义 tokenize 切块策略（`splitter.SplitByTokenBudget`）
- 调整周期记忆分桶（period_bucket：d1/w1/weekend）
- 接入记忆衰减（老 chunk 自动降权 / 归档）
