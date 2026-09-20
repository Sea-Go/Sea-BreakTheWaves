---
name: recall.rule
category: recall
description: 热点/最新/编辑规则召回
tools:
  - rule_recall
  - hot_list_query
  - editorial_pick
inputs:
  - name: channel
    type: string
    required: false
    description: 频道名（按频道取热点/编辑位）
  - name: rule_type
    type: string
    required: true
    description: 规则类型（hot/latest/editorial/trending）
  - name: topk
    type: int
    required: false
    default: 50
outputs:
  - name: candidates
    type: list
    description: 规则召回候选列表
---

# recall.rule

## 用途
按业务规则召回候选文章，覆盖四类强运营信号：
- **hot**：近 24h 高点击/高互动文章（热点榜）
- **latest**：最新发布文章（按 publish_at 倒排）
- **editorial**：编辑人工选稿位
- **trending**：24h 突增热度（与 TrendingProfile 联动）

当用户画像稀疏（冷启动）或意图不明确时，作为兜底召回路径。

## 调用方式
Agent 通过 tool_call 调用，指定 `rule_type` 与 `topk`。可按 channel 隔离规则池。多规则可并行调用，结果合并后送入排序阶段。

## 二开扩展点
- 实现自定义 RuleRecaller interface 注入业务规则（如"关注作者最新文")
- 调整 hot/trending 计算窗口与权重
- 通过 ChannelManager.RegisterChannel 为新频道注入独立规则池
- 在 fallback 链路（recall/fallback.go）中调整规则召回顺序
