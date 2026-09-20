---
name: rerank.self
category: rerank
description: 自研 Cross-encoder/Two-tower/LambdaMART
tools:
  - self_rerank
  - cross_encoder
  - two_tower
  - lambda_mart
inputs:
  - name: candidates
    type: list
    required: true
    description: 待重排候选列表
  - name: query
    type: string
    required: false
    description: 查询文本（搜索场景）
  - name: user_id
    type: string
    required: false
    description: 用户 ID
  - name: model
    type: string
    required: false
    default: cross_encoder
    description: 重排模型（cross_encoder/two_tower/lambda_mart）
  - name: topk
    type: int
    required: false
    default: 20
outputs:
  - name: candidates
    type: list
    description: 重排后候选列表
  - name: model_used
    type: string
    description: 实际使用的模型
---

# rerank.self

## 用途
自研重排模型精排候选，覆盖三种架构：
- **cross_encoder**：query-doc 交叉编码，精度高但延迟高
- **two_tower**：双塔召回后精排，延迟低、适合大候选集
- **lambda_mart**：listwise 排序优化 NDCG

是 slow path 默认重排器，对应 RecommendConfig.RerankModel=self。

## 调用方式
Agent 通过 tool_call 调用，指定 `model` 与 `candidates`。底层 RerankerRegistry 分发到对应实现。重排分数写入 Candidate.Scores["rerank_score"]，并同步到 RecommendResponse.RerankScores。

## 二开扩展点
- 在 rerank/ 目录新增自研模型（如 DSSM/PolyEncoder）
- 调整 cross_encoder 的 max_length 与 batch_size 平衡精度/延迟
- 通过 RecommendConfig.RerankWeights 调整多模型融合权重
- 替换模型服务端点（self_rerank 调用 internal/ai_client.go）
