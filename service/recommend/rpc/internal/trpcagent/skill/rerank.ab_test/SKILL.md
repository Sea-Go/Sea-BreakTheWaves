---
name: rerank.ab_test
category: rerank
description: A/B 分桶 rerank 对比
tools:
  - ab_bucket
  - rerank_dispatch
  - metric_compare
inputs:
  - name: user_id
    type: string
    required: true
    description: 用户 ID（用于分桶）
  - name: candidates
    type: list
    required: true
    description: 待重排候选列表
  - name: experiments
    type: list
    required: true
    description: 实验配置列表（每项含 model/weights/比例）
  - name: topk
    type: int
    required: false
    default: 20
outputs:
  - name: candidates
    type: list
    description: 命中实验分组的重排结果
  - name: experiment_id
    type: string
    description: 命中的实验分组 ID
  - name: model_used
    type: string
    description: 实际使用的重排模型
---

# rerank.ab_test

## 用途
按用户 ID 哈希分桶，将流量分配到不同 rerank 实验（如 self vs external vs llm），对比各模型在线效果。支持：
- 按比例分流（如 50% self / 30% external / 20% llm）
- 按 channel 隔离实验
- 输出 experiment_id 供后续指标归因

用于验证新重排模型上线前的实际收益。

## 调用方式
Agent 通过 tool_call 调用：
1. `ab_bucket` 按 user_id 哈希落到某实验组
2. `rerank_dispatch` 调用对应 rerank Skill（rerank.self / rerank.external / rerank.llm）
3. `metric_compare`（离线）对比各组 CTR/完成率/质量分

## 二开扩展点
- 调整分桶哈希算法（如 murmur3 / md5）
- 通过 experiments 参数动态配置实验组比例
- 注入自定义分流规则（如按用户分层分流）
- 在 metric_compare 中新增业务指标（如停留时长/分享率）
