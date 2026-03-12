# 技能：阿里百炼文本精排（dashscope_text_rerank）

## 目的

使用阿里百炼 `qwen3-rerank` 对候选文本做语义精排，适合接在：

1. 向量召回之后
2. SQL / 规则召回之后
3. 多路召回合并之后

输入是 `query + documents[]`，输出是按相关性排序的结果列表。

## 入参（JSON）

```json
{
  "query": "动漫推荐",
  "documents": ["候选1", "候选2", "候选3"],
  "topk": 10,
  "instruct": "Retrieve semantically similar text."
}
```

说明：
- `query`：用户检索问题或排序查询
- `documents`：待精排的候选文本
- `topk`：返回前 K 条
- `instruct`：可选；为空时使用配置里的默认 instruct

## 出参（JSON）

```json
{
  "model": "qwen3-rerank",
  "query": "动漫推荐",
  "topk": 3,
  "input_count": 3,
  "returned": 3,
  "request_id": "xxx",
  "total_tokens": 123,
  "items": [
    {"index":1,"relevance_score":0.98,"document":"候选2"},
    {"index":0,"relevance_score":0.91,"document":"候选1"},
    {"index":2,"relevance_score":0.74,"document":"候选3"}
  ],
  "latency_ms": 132
}
```

## 配置

复用 `ali.apikey`，并支持以下可选配置：

- `ali.rerank_url`：默认 `https://dashscope.aliyuncs.com/compatible-api/v1/reranks`
- `ali.rerank_model`：默认 `qwen3-rerank`
- `ali.rerank_instruct`：默认空
- `ali.rerank_topn_cap`：默认 100

## 观测

该技能会输出 `skills.rerank.dashscope_text_rerank` span/event，包含：

- model
- input_count
- returned
- topk
- latency_ms
