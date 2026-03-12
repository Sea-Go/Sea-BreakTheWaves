# 技能：记忆管理与维护（user_memory_get / user_memory_upsert / memory_maintain_window）

## 目标

满足你对“长期/短期/周期记忆”的要求：

- **写入/读取用户记忆**（PG：`user_memory`）
- 对长期/周期记忆做 **tokenize 分块 + 向量化存储**（PG：`user_memory_chunks` 保存原文；Milvus：`user_memory_chunks` 做向量索引）
- 基于用户过去 1 天 / 7 天行为，生成摘要写回记忆（用于全局理解与后续意图生成）

## user_memory_upsert 的 tokenize 逻辑

当 memory_type 为 `long_term` 或 `periodic` 时：

1. 使用 `splitter.SplitByTokenBudget` 按 token 预算把记忆文本切成较大的块（避免切得过碎）
2. 对每个块做向量化（embedding）
3. 先删除旧 chunks，再写入新 chunks（`user_memory_chunks`）

对应配置：

```yaml
split:
  memory_chunk_max_tokens: 600
  memory_chunk_overlap_tokens: 60
```

## memory_maintain_window 的行为摘要逻辑

输入：

- user_id
- window: 1d / 7d
- target_memory_type: short_term / long_term

流程：

1. 从 `user_rec_history` 拉取最近若干条记录
2. 按时间窗口过滤
3. 联表 `articles` 获取文章的 type_tags/tags
4. 聚合得到偏好类型/标签的 TopK
5. 生成一个可读摘要，写入 `user_memory`

该摘要会被后续推荐 Agent 用于生成“意图向量”，再去 Milvus 做召回。

## 观测

- `user_memory_upsert` 打 `side_effect.user_memory_upsert`
- `memory_maintain_window` 打 `side_effect.memory_maintain_window`

你可以在 Jaeger 里看到“维护记忆 -> 召回 -> 推荐”的完整链路。


## periodic 的分桶

当 target_memory_type=periodic 时，你可以传 `period_bucket` 来维护多个周期段（例如 d1 / w1 / weekend）。不同桶会分别写入 `user_memory`，互不覆盖。
