---
name: tool.doc_ingest
category: tool
description: 文档导入
tools:
  - doc_ingest
  - article_chunk_split
  - article_vectorize
inputs:
  - name: article_json
    type: string
    required: false
    description: 结构化文章 JSON（与 markdown 二选一）
  - name: markdown
    type: string
    required: false
    description: Markdown 原文（与 article_json 二选一）
  - name: article_id
    type: string
    required: false
    description: 文章 ID（不传自动生成）
  - name: score
    type: float
    required: false
    default: 0.0
    description: 文章初始质量分
outputs:
  - name: article_id
    type: string
    description: 文章 ID
  - name: coarse_vector_inserted
    type: bool
    description: 粗召回向量是否写入
  - name: fine_chunk_count
    type: int
    description: 精召回 chunk 数
---

# tool.doc_ingest

## 用途
把一篇结构化文章入库到推荐系统的知识侧。底层对应 `skills/doc_ingest/doc_ingest.tool.go` 与 `chunk/`。是内容能被召回的前提：入库后才会同时有粗召回向量（article 级）、精召回向量（chunk 级）、PG 原文与元信息（供引用 / 解释 / 质量验证）。用于：运营导入新稿、爬虫回灌、二开业务导入垂类内容。

## 调用方式
Agent 通过 tool_call 调用，传 `article_json` 或 `markdown`（二选一）。底层流程：
1. 调 `article_chunk_split` 按约定样式切分：
   - 粗召回向量来源：标题 + 封面 + 标签 + 关键词 + 二级标题；
   - 精召回向量来源：二级标题 + 段落内容；
2. 调 `article_vectorize` 对两路文本做 embedding；
3. 写 Milvus（recall_coarse + recall_precise）；
4. 写 PG（articles 原文 + 元信息 + chunks）；
5. 打 `side_effect.doc_ingest` span；
6. 返回 `article_id` / `coarse_vector_inserted` / `fine_chunk_count`。

## 二开扩展点
- 自定义切分规则（实现 `chunk.Splitter` 接口）
- 替换 embedding 模型
- 接入增量更新（文章改版后重入库，先删旧向量再写新）
- 入库前质量校验（拒低质 / 重复内容）
