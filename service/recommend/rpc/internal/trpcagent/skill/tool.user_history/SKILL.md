---
name: tool.user_history
category: tool
description: 用户历史查询
tools:
  - user_history_add
  - user_history_recent
  - user_history_similar
inputs:
  - name: action
    type: string
    required: true
    description: 操作类型（add/recent/similar）
  - name: user_id
    type: string
    required: true
    description: 用户 ID
  - name: limit
    type: int
    required: false
    default: 50
    description: recent/similar 返回条数
  - name: article_id
    type: string
    required: false
    description: add/similar 时的文章 ID
outputs:
  - name: history
    type: list
    description: 历史记录列表
---

# tool.user_history

## 用途
查询 / 写入用户推荐历史。底层对应 `skills/user_history/user_history.tool.go` 与 `storage/user_history_repo.go`。是召回去重、CF 近邻、解释链路"看过没"判定的数据源。三个动作：
- **add**：追加一条历史（推荐曝光 / 点击 / 反馈）
- **recent**：取最近 N 条历史（用于去重窗口、CF 用户行为）
- **similar**：找与指定 article 相似的历史文章（用于"看过类似的"判定）

## 调用方式
Agent 通过 tool_call 调用，传入 `action` / `user_id`：
- add：传 `article_id` + 事件类型，追加到 user_rec_history；
- recent：传 `limit`，按时间倒序返回最近历史；
- similar：传 `article_id` + `limit`，按内容相似度返回相似历史文章。

历史表 `user_rec_history` 是 CFRecaller、OnlineFeedbackHook、explain.recommend 的共同数据源。

## 二开扩展点
- 新增历史字段（如"曝光位置"/"来源频道"）
- 自定义去重窗口（按业务调 recent 的默认时间范围）
- 接入历史归档（老数据冷存，热表只保留近 N 天）
- similar 改用图谱路径相似（接 `graph.similar`）
