# 技能：候选池管理（pool_get_size / pool_refill / pool_pop_topk）

## 目标

在推荐系统里维护多个“候选池”（你要求的长期/短期/周期池）：

- **长期池（long_term）**：偏好稳定、更新慢
- **短期池（short_term）**：近期行为驱动，更新快
- **周期池（periodic）**：几个时间段的行为（例如最近 1 天 / 1 周 / 周末段等）

候选池落地在 Postgres 表：`user_pool_items`。

## 关键配置（config.yaml）

```yaml
pools:
  long_term:
    min_size: 200
    refill_size: 300
  short_term:
    min_size: 200
    refill_size: 300
  periodic:
    min_size: 200
    refill_size: 300
  recommend:
    take_size: 20
    remove_after_recommend: true
```

## 工具 1：pool_get_size

查询某个用户在指定池子中的数量，便于判断是否需要补充。

## 工具 2：pool_refill

当池子不足时进行补充（核心逻辑）：

1. 使用 `query_text` 生成向量
2. 在 Milvus **粗召回集合**检索 `refill_size*2` 条
3. 结合文章基础分（PG `articles.score`）与向量相似度计算 `remark_score`  
   `remark_score = similarity_weight * similarity + score_weight * score`
4. 写入 `user_pool_items`（重复 article_id 自动忽略）

该技能会额外记录 `side_effect.pool_refill` span/event，包含：

- inserted / considered / pool_size_after
- returned_doc_count / coverage_score / empty

## 工具 3：pool_pop_topk

按 `remark_score` 从池子取 topK：

- 默认 `topk = pools.recommend.take_size`
- `remove=true` 时：推荐后出池（对应你要求的“推荐后把文章从池子中移走”）

## 为什么池子要放 PG？

- **可审计**：能回溯“某次推荐到底从哪个池子拿的候选”
- **好维护**：周期池可以用 `period_bucket` 字段直接分桶（按天/周/自定义时间段）
- **可联动**：后续可以把曝光/点击/喜好分数写回，做持续优化
