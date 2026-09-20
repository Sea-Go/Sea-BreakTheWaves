---
name: quality.judge
category: quality
description: LLM Judge + Best-of-N
tools:
  - llm_call
  - best_of_n
  - rubric_eval
inputs:
  - name: article_id
    type: string
    required: true
    description: 文章 ID
  - name: content
    type: string
    required: true
    description: 文章内容
  - name: rubric
    type: string
    required: true
    description: 评判标准
  - name: n
    type: int
    required: false
    default: 3
    description: Best-of-N 采样次数
outputs:
  - name: quality
    type: object
    description: ArticleQuality 评分
  - name: confidence
    type: float
    description: 评分置信度（多次采样一致性）
---

# quality.judge

## 用途
对文章做 LLM Judge 质量评判，并采用 Best-of-N 策略：
- 同一文章用 LLM 采样 N 次评判
- 取一致性最高（或平均）的结果
- 输出 confidence 反映评判稳定性

适用于高质量门槛场景（如频道 QualityThreshold≥0.8）或争议文章复核。对应 RecommendConfig.BestOfN。

## 调用方式
Agent 通过 tool_call 调用：
1. `best_of_n` 触发 N 次 `llm_call`（temperature>0 采样）
2. `rubric_eval` 按 rubric 计算每次评分
3. 聚合为最终 quality + confidence

## 二开扩展点
- 调整 n 控制采样次数（n 越大越稳但成本越高）
- 替换聚合策略（取众数/取中位数/取最高）
- 通过 rubric 注入领域专属评判标准
- 实现 QualityJudger interface 接入多模型集成评判
