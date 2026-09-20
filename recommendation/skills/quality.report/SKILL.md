---
name: quality.report
category: quality
description: 质量报告查询
tools:
  - quality_query
  - report_aggregate
inputs:
  - name: article_id
    type: string
    required: false
    description: 文章 ID（单篇查询时传）
  - name: author
    type: string
    required: false
    description: 作者名（按作者聚合）
  - name: channel
    type: string
    required: false
    description: 频道（按频道聚合）
  - name: time_range
    type: string
    required: false
    description: 时间范围（如 7d/30d）
  - name: group_by
    type: string
    required: false
    default: article
    description: 聚合维度（article/author/channel）
outputs:
  - name: report
    type: object
    description: 质量报告（含分布/趋势/异常项）
---

# quality.report

## 用途
查询文章质量报告，支持多维度聚合：
- **单篇**：查指定文章的 6 维评分 + 历史 feedback
- **按作者**：聚合作者文章质量分布
- **按频道**：聚合频道质量分布与趋势
- **按时间**：查指定时间范围内的质量趋势

用于运营监控、作者质量画像、频道质量阈值调优。

## 调用方式
Agent 通过 tool_call 调用 `quality_query`，传入查询参数。底层 `report_aggregate` 从质量库与反馈库聚合数据，输出结构化报告。

## 二开扩展点
- 调整 group_by 新增聚合维度（如按 IP / 按标签）
- 注入自定义报告模板（如导出 PDF/Excel）
- 实现 ReportGenerator interface 接入 BI 系统
- 通过 time_range 控制趋势分析窗口
