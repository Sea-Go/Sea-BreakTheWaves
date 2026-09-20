---
name: graph.image_search
category: graph
description: 以图搜文
tools:
  - image_search_articles
  - visual_similar_lookup
inputs:
  - name: image_url
    type: string
    required: true
    description: 图片 URL 或 base64
  - name: topk
    type: int
    required: false
    default: 20
    description: 返回文章数量
  - name: channel
    type: string
    required: false
    description: 频道隔离键
outputs:
  - name: articles
    type: list
    description: 视觉相似文章列表（含 article_id/similarity）
---

# graph.image_search

## 用途
以一张图片为输入，召回视觉相似的文章（以图搜文）。底层对应 `internal/graph` 的 `Client.ImageSearchArticles(ctx, imageURL)` 与 `RecallImageVisualSimilar`，走图谱 `VISUALLY_SIMILAR` 边做近邻扩展。用于：用户上传封面图找同类内容、运营按视觉风格补稿。

## 调用方式
Agent 通过 tool_call 调用，传入 `image_url`。底层流程：
1. 对图片做 embedding（视觉模型）；
2. 在图谱中定位 / 创建图片节点；
3. 沿 `VISUALLY_SIMILAR` 边扩展到文章节点；
4. 按 similarity 排序、截断 topk 返回。

若图谱中无该图片节点，会先调 `visual_similar_lookup` 在 Milvus 视觉向量库做兜底近邻。

## 二开扩展点
- 替换视觉 embedding 模型（CLIP / 业务自有模型）
- 调整 `VISUALLY_SIMILAR` 边的建边阈值（在 `operations.go` 的 `LinkVisuallySimilar` 中）
- 接入频道隔离（按 channel 过滤文章节点）
- 与 `recall.content` 文本路融合（图文双路 RRF）
