---
name: rerank.external
category: rerank
description: 外部 DashScope rerank
tools:
  - dashscope_rerank
inputs:
  - name: candidates
    type: list
    required: true
    description: 待重排候选列表（含 article_id 与文本）
  - name: query
    type: string
    required: true
    description: 查询文本
  - name: model
    type: string
    required: false
    default: gte-rerank
    description: DashScope rerank 模型名
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

# rerank.external

## 用途
调用阿里云 DashScope rerank API 对候选精排，无需自研模型即可获得 SOTA 重排效果。对应 RecommendConfig.RerankModel=external。

适用场景：
- 自研模型未上线前的过渡方案
- A/B 实验对照基线
- 长尾 query 的兜底精排

## 调用方式
Agent 通过 tool_call 调用 `dashscope_rerank`，传入 `query` 与 `candidates`（每条候选含文本）。底层调用 skills/rerank/dashscope_rerank.tool.go 暴露的工具，请求 DashScope API，返回 rerank_score。

## 二开扩展点
- 替换 DashScope 模型名（gte-rerank / gte-rerank-v2）
- 调整 topk 控制返回数量
- 实现 Reranker interface 接入其他外部服务（如 Cohere/Jina）
- 通过 config.yaml 配置 api_key 与 endpoint
