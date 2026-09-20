---
name: search.filter
category: search
description: 标签/类型/IP/作者/时间/质量过滤
tools:
  - filter_apply
  - metadata_query
inputs:
  - name: candidates
    type: list
    required: true
    description: 待过滤候选列表
  - name: filter
    type: object
    required: true
    description: SearchFilter 过滤条件
outputs:
  - name: candidates
    type: list
    description: 过滤后候选列表
  - name: filtered_count
    type: int
    description: 被过滤掉的数量
---

# search.filter

## 用途
对候选文章按多维度过滤，支持：
- **Tags**：标签过滤（Include/Exclude）
- **Authors**：作者过滤
- **Channel**：频道过滤
- **TimeRange**：时间范围（如 7d/2024-01/2024-Q1）
- **QualityThreshold**：质量阈值（低于阈值的过滤）

可独立调用，也可作为 search.hybrid 的子步骤。

## 调用方式
Agent 通过 tool_call 调用 `filter_apply`，传入 candidates 与 filter。底层：
1. 按 filter 各字段从 `metadata_query` 拉取候选元信息
2. 逐条过滤
3. 返回过滤后候选与 filtered_count

## 二开扩展点
- 注入自定义过滤维度（如按 IP 系列过滤）
- 调整 QualityThreshold 实现动态门槛
- 实现 Filter interface 接入业务规则引擎
- 通过 filter 复合条件（AND/OR）支持复杂查询
