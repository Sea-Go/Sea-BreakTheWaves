---
name: obs.cost
category: obs
description: 成本查询
tools:
  - cost_query
  - cost_breakdown
inputs:
  - name: dimension
    type: string
    required: false
    default: total
    description: 维度（total/llm/embedding/milvus/neo4j/pg）
  - name: window
    type: string
    required: false
    default: 24h
    description: 查询时间窗
  - name: group_by
    type: string
    required: false
    description: 分组键（channel/user_id/skill）
outputs:
  - name: cost
    type: map
    description: 成本明细（含调用量/单价/总费用）
  - name: trend
    type: list
    description: 成本趋势序列
---

# obs.cost

## 用途
查询推荐系统的运行成本（LLM 调用、embedding、Milvus、Neo4j、PG 等）。底层对应 `internal/obs` 的成本采集与 `metrics/`。用于：成本归因（哪个频道 / skill / 用户最烧钱）、预算告警、二开业务方按成本分摊。

## 调用方式
Agent 通过 tool_call 调用，传入 `dimension` / `window`。底层流程：
1. 调 `cost_query` 从指标库取该窗口的成本数据；
2. 若传 `group_by`，调 `cost_breakdown` 做分组聚合；
3. 计算 trend（与上一窗口对比）；
4. 返回 `cost` 明细与 `trend` 序列。

成本按各依赖的计费单位换算（LLM 按 token、Milvus 按检索次数、PG 按查询行数）。

## 二开扩展点
- 新增成本维度（接入新依赖时登记）
- 自定义单价表（按业务实际计费填）
- 接入预算告警（超阈值自动通知）
- 成本分摊到二开业务方（按 channel 标签归集）
