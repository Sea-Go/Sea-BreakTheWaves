# 技能：用户推荐历史（Milvus 向量索引 + PG 事实落库）（user_history_add / user_history_recent / user_history_similar）

## 目的

你要求的“推荐用户历史记录”采用 **PG + Milvus 分层**：

- **Postgres**：落事实记录（可审计、可做时间窗口统计）
- **Milvus**：仅做向量相似检索（去重/偏好分析），避免同时维护 pgvector

字段（满足你提出的最低要求）：

- user_id
- article_id
- clicked（是否点击进入）
- preference（喜好程度）

向量索引：

- embed（向量）写入 Milvus 的 `user_rec_history` 集合
- PG 表 `user_rec_history` 额外有 `history_id` 作为主键，与 Milvus 的主键一致

表结构见：`infra/postgres_init.go` 的 `user_rec_history`。

## 工具说明

### 1) user_history_add

写入一条历史记录：

- 如果提供 `embed_text`，会自动向量化后写入 Milvus（PG 仍保存事实字段）
- 记录 `clicked/preference`，用于长期偏好建模

### 2) user_history_recent

读取最近 N 条历史记录（按时间倒序）。

### 3) user_history_similar

使用 Milvus 做相似检索：

- 输入 `query_text` -> 向量化 -> Milvus Search（按 user_id filter）-> 回 PG 取回事实字段

## 观测

- `user_history_add` 会额外打 `side_effect.user_history_add` span/event
- 其余读接口属于“读取类工具”，默认由 `execute_tool.xxx` span 体现
