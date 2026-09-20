---
name: rank.traditional
category: rank
description: 加权/LR/GBDT 传统排序
tools:
  - weighted_rank
  - lr_rank
  - gbdt_rank
  - feature_extract
inputs:
  - name: candidates
    type: list
    required: true
    description: 待排序候选列表
  - name: profile
    type: object
    required: false
    description: 用户画像
  - name: model
    type: string
    required: false
    default: weighted
    description: 排序模型（weighted/lr/gbdt）
  - name: weights
    type: object
    required: false
    description: 特征权重（weighted 模式）
outputs:
  - name: candidates
    type: list
    description: 排序后候选列表
---

# rank.traditional

## 用途
基于传统机器学习模型对召回候选排序，支持三种模型：
- **weighted**：加权求和（按 weights 对 cf_score/graph_score/freshness 等特征加权）
- **lr**：逻辑回归（LRRanker，线性特征交叉）
- **gbdt**：梯度提升树（GBDTRanker，非线性特征交叉）

是 fast path 默认排序器，延迟低、可解释性强。Candidate.Scores 中的特征字段作为模型输入。

## 调用方式
Agent 通过 tool_call 调用，指定 `model` 与 `candidates`。底层 RankerRegistry 根据 model 名分发到对应 Ranker 实现。排序后 Candidate.Score 更新为模型输出分。

## 二开扩展点
- 在 rank/registry.go 中注册自定义 Ranker（如 FM/DeepFM）
- 通过 `weights` 参数动态调整特征权重（A/B 实验）
- 实现 FeatureExtractor interface 注入自定义特征工程
- 调整 GBDT/LR 模型文件路径（config.yaml 中 model_path）
