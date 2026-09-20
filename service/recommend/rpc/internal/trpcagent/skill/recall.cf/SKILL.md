---
name: recall.cf
category: recall
description: 协同过滤召回
tools:
  - cf_recall
  - user_similar_lookup
  - item_similar_lookup
inputs:
  - name: user_id
    type: string
    required: true
    description: 目标用户 ID
  - name: pattern
    type: string
    required: false
    default: user_cf
    description: CF 模式（user_cf/item_cf/matrix_factor）
  - name: topk
    type: int
    required: false
    default: 50
outputs:
  - name: candidates
    type: list
    description: CF 召回候选列表（含 cf_score）
---

# recall.cf

## 用途
基于协同过滤召回候选文章，提供行为相似性证据，弥补内容召回的"语义近但行为冷"问题。支持三种模式：
- **user_cf**：找相似用户喜欢的文章
- **item_cf**：找与用户历史行为文章相似的文章
- **matrix_factor**：矩阵分解隐向量近邻

依赖 BehaviorProfile 中的 RecentClicks/RecentLikes/RecentFavorites 作为用户行为输入。

## 调用方式
Agent 通过 tool_call 调用，传入 `user_id` 与 `pattern`。底层 CFRecaller 从用户行为矩阵计算近邻并取 topk。CF 分数会写入 Candidate.Scores["cf_score"]，供排序器加权。

## 二开扩展点
- 替换 CFRecaller 实现自定义 CF 算法（如 Swin、SASRec 序列推荐）
- 调整 user_similar_lookup 的近邻数与相似度阈值
- 通过 RecommendConfig.CFEnabled 控制是否启用 CF 召回
- 注入自定义行为权重（点击/点赞/收藏/完成阅读的差异化权重）
