# 技能：文档入库（doc_ingest）

## 目的

把一篇结构化文章入库到推荐系统的“知识侧”：

1. 按约定样式做 **文本切割**
2. 生成两类向量并写入 Milvus：
   - 粗召回向量（文章级）
   - 精召回向量（chunk 级）
3. 把原文、chunk、文章元信息写入 Postgres（用于引用、解释、质量验证、回溯）

## 入参（JSON）

支持两种输入方式（二选一）：

- `article_json`：直接传 `splitter.Article` 的 JSON（适合上游已结构化）
- `markdown`：传符合约定的 Markdown（适合直接导入文档）

```json
{
  "article_id": "可选，空则自动生成",
  "score": 0.0,
  "article_json": "...",
  "markdown": "..."
}
```

## 出参（JSON）

```json
{
  "article_id": "a1",
  "coarse_vector_inserted": true,
  "fine_vector_inserted": 12,
  "fine_chunk_count": 12
}
```

## 切分规则（与你的定义一致）

- 粗召回向量：标题 + 封面 + 手打 type 标签类型 + 关键词检测 + 各类二级标题
- 精召回向量：二级标题 + 段落内容（包括图片和文字）

详细实现见：`splitter/` 目录与 `doc/文档切分设计.md`。

## 观测

该技能会输出 `side_effect.doc_ingest` span/event，包含：

- article_id
- fine_chunk_count
- outcome

方便你在一条 Trace 内定位“入库 -> 检索 -> 推荐 -> 退场”的完整链路。
