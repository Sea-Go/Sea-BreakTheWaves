---
name: recall.hybrid
category: recall
description: 多源并行混合召回
tools:
  - recall_content
  - recall_rule
  - recall_cf
  - recall_graph
  - recall_channel
  - hybrid_merge
inputs:
  - name: user_id
    type: string
    required: true
    description: 用户 ID
  - name: channel
    type: string
    required: false
    description: 频道名
  - name: intent
    type: object
    required: false
    description: 意图对象（来自 IntentAgent）
  - name: topk
    type: int
    required: false
    default: 200
    description: 混合后保留的候选总数
  - name: sources
    type: list
    required: false
    description: 启用的召回源列表（默认全启用）
outputs:
  - name: candidates
    type: list
    description: 混合去重后的候选列表
  - name: source_stats
    type: object
    description: 各源召回数与耗时统计
---

# recall.hybrid

## 用途
并行调度多个召回源（content/rule/cf/graph/channel），按 RRF 或加权融合合并去重后输出统一候选池。是推荐主路径默认召回策略（RecommendConfig.RecallStrategy=hybrid）。

由 OrchestratorAgent 根据 Intent.Complexity 决策：
- simple → 仅 content+rule（fast path）
- medium → content+rule+cf（hybrid）
- complex → 全源并行（slow path）

## 调用方式
Agent 通过 tool_call 调用，并行触发子 Skill（recall.content / recall.rule / recall.cf / recall.graph / recall.channel）。各源返回 candidates 后，`hybrid_merge` 按 RRF 融合去重，截断 topk。

## 二开扩展点
- 调整 hybrid_merge 融合策略（rrf/combsum/weighted）
- 通过 sources 参数动态裁剪召回源（A/B 实验）
- 注入自定义召回源（实现 Recaller interface 并注册到 recall/registry.go）
- 调整各源权重（Candidate.Scores 中的 source 字段权重）
