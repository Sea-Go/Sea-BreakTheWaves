---
name: quality.score
category: quality
description: 单文章 6 维质量评分
tools:
  - quality_judge
  - llm_call
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
    required: false
    description: 自定义评判标准（可空用默认）
outputs:
  - name: quality
    type: object
    description: ArticleQuality 6 维评分对象
---

# quality.score

## 用途
对单篇文章做 6 维质量评分，输出 ArticleQuality：
- **Authority** 权威性（0-1）
- **Depth** 深度（0-1）
- **Freshness** 新鲜度（0-1）
- **Completeness** 完整性（0-1）
- **Readability** 可读性（0-1）
- **Citation** 引用质量（0-1）
- **Overall** 综合分（加权）
- **Grade** 等级（A-T）

质量分用于召回过滤（QualityThreshold）、排序特征、推荐解释。

## 调用方式
Agent 通过 tool_call 调用 `quality_judge`，传入 article_id 与 content。底层调用 `llm_call` 让模型按 rubric 打分，输出结构化 ArticleQuality。

## 二开扩展点
- 替换 RecommendConfig.QualityModel 切换评判模型
- 通过 rubric 参数注入业务领域评判标准
- 实现 QualityJudger interface 自定义评判逻辑（如规则 + LLM 混合）
- 调整 6 维加权权重影响 Overall 计算
