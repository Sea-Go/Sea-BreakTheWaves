---
name: quality.batch_score
category: quality
description: 批量质量评分
tools:
  - quality_judge
  - llm_call
  - batch_dispatch
inputs:
  - name: articles
    type: list
    required: true
    description: 文章列表（每项含 article_id 与 content）
  - name: rubric
    type: string
    required: false
    description: 自定义评判标准
  - name: concurrency
    type: int
    required: false
    default: 5
    description: 并发数
outputs:
  - name: qualities
    type: list
    description: ArticleQuality 列表
---

# quality.batch_score

## 用途
批量对多篇文章做 6 维质量评分，复用 quality.score 的评判逻辑，通过并发控制提升吞吐。适用场景：
- 文章入库时离线打分
- 召回池补充时批量质量过滤
- 质量报告聚合

## 调用方式
Agent 通过 tool_call 调用，传入 `articles` 列表与 `concurrency`。底层 `batch_dispatch` 并发调用 quality.judge，结果聚合为 qualities 列表返回。失败条目降级为默认质量分。

## 二开扩展点
- 调整 concurrency 控制并发度（避免 LLM 限流）
- 注入批处理失败兜底策略（如标记为待重试）
- 替换 batch_dispatch 实现自定义调度（如优先级队列）
- 通过 rubric 统一批量评判标准
