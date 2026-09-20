# 技能：AI 精排序（ai_rerank_articles）

## 目标

满足你提出的“最终生成步骤要做 AI 自身精排序”的要求：

输入：候选文章 ID 列表  
输出：排序后的文章 ID 列表 + 每条原因 + overall_confidence

## 关键上下文

该技能会从 PG 拉取：

- 长期记忆：`user_memory`（long_term）
- 短期记忆：`user_memory`（short_term）
- 周期记忆：`user_memory`（periodic，demo 默认桶 d1）

并从 `articles` 拉取候选文章的元信息（title/type_tags/tags/score），给 LLM 做排序依据。

## 输出格式

模型必须输出 JSON：

```json
{
  "overall_confidence": 0.74,
  "ranked": [
    {"article_id":"a1","score":0.91,"reason":"与短期意图匹配"},
    {"article_id":"a2","score":0.83,"reason":"符合长期偏好"}
  ]
}
```

## 观测

该技能会写入 `rank.completed` span/event（与你的 schema 对齐）：

- candidate_in / candidate_out
- decision.type = rank，chosen = ai_rerank，confidence = overall_confidence

这样在 Jaeger 里能看到：召回 -> 候选池 -> 精排序 -> 生成/出池 的完整链路。
