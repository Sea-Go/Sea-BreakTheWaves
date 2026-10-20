# tree — 检索树纯域层（C6 btw-tree-builder / §3b）

`service/async/rpc/internal/tree` 是检索树构建器的纯域层：把一组已编码文档
（`DocVec{DocKey, Vec}`）确定性地组织成一棵可检索的聚类树，并支持新文档的
增量分配。纯 Go 实现，无任何第三方依赖。

## 职责

| 能力 | 入口 | 说明 |
| --- | --- | --- |
| 确定性凝聚聚类 | `cluster.go` `Cluster(docs, stopSize) [][]int` | 余弦距离 + 平均链接的贪心凝聚聚类，返回原始下标分组 |
| 树构建 | `build.go` `Build(moduleScope, encoderID, docs, summarize, embed)` | 递归聚类建树，节点摘要经 Summarizer 接缝取得，簇向量取成员均值或经 Embedder 再编码 |
| 结构校验 | `tree.go` `(*RetrievalTree).Validate()` | 根唯一、parent/children 双向一致、DFS 判环且全连通、叶/簇节点形状、members 与子树叶集一致 |
| 增量分配 | `incremental.go` `Assign(t, docs)` | 新文档按簇向量最近余弦分配到叶父簇，返回 assignment 与受影响（dirty）簇 id |
| 标脏剪枝 | `incremental.go` `Prune(t, docKeys)` | 返回被删除文档所属簇的 dirty 集合，不立即重建、不改动树 |

## 边界

- **不做 LLM 调用**：摘要完全通过 `Summarizer` 接缝注入
  （`Summarize(docTexts []string) (string, error)`）。域层自带确定性 stub
  `DeterministicSummarizer`（成员 doc_key 规范拼接 + 长度与内容哈希），
  真实 LLM 实现由后续 DC 接线替换。
- **不做向量再编码的隐式行为**：`Embedder` 接缝可选（`nil` 时簇向量 = 成员
  向量均值）；传入时必须返回与文档一致的维度。
- **不做存储**：不读写任何数据库 / 对象存储 / 索引 manifest。树是纯数据
  结构（JSON snake_case 序列化），持久化由外层负责。
- **encoder_id 只记录不校验**：`encoder_id` 与索引 manifest 的同源绑定、
  不匹配拒载，属于装载侧职责（manifest 归装载方所有）。域层仅要求非空。
- **dirty 是返回值不是状态**：`Assign` / `Prune` 均为纯函数，不改动树；
  增量落地（就地生长还是重建、何时重建）由 DC 编排层决策。

## 确定性声明

同输入必得同树，依据以下固定规则（详见各文件注释）：

1. 聚类前先按 `DocKey` 排序（稳定排序），输入顺序不影响结果；
2. 合并选择：平均链接相似度最高者胜，平局按当前簇表中字典序最小的
   索引对 `(i < j)`；
3. 停机规则：仅执行合并后大小 ≤ `stopSize`（默认 8，非正值回退默认）的
   合并；不存在可行合并（各簇已达上限）或只剩 1 簇时停止。任何返回分组
   大小都不超过 `stopSize`；
4. 相似度用 float64 固定顺序累加，平均链接用固定加权更新公式维护，
   浮点路径逐位可复现；
5. `node_id = hex(sha256(moduleScope ‖ epoch ‖ 路径))[:16]`，路径为从根
   出发的簇下标路径（`""`, `"0"`, `"0/3"` …），字段以 unit separator 分隔；
   `tree_id` 同源但加盐 `"#tree"`，不与任何节点 id 冲突；
6. `Assign` 平局取 node_id 字典序最小者；`dirty` / 分组成员 / 关键词列表
   一律排序输出；
7. `cluster_dense_ref` 以 `dense.v1:` + float32 小端 hex 内联编码，自包含
   可解码，不依赖外部状态。

## 契约快照（JSON snake_case）

```json
{
  "tree_id": "…16 hex…",
  "module_scope": "sea.knowledge",
  "root_id": "…16 hex…",
  "build_epoch": 1,
  "encoder_id": "enc-bge-m3-20261001",
  "nodes": [
    {
      "node_id": "…16 hex…",
      "is_cluster": true,
      "members": ["chat/a1", "chat/a2"],
      "summary_ref": "stub-summary:v1:…",
      "cluster_dense_ref": "dense.v1:…",
      "keywords": ["chat"],
      "parent_id": "",
      "children": ["…", "…"]
    }
  ]
}
```

叶节点：`is_cluster=false` 且 `doc_key` 非空、无 children/members/簇引用。
簇节点：`is_cluster=true`、`members` 非空且与子树叶集完全一致、具备摘要与
稠密引用。`Validate()` 拒绝一切违反上述形状的树。

## 用法

```go
docs := []tree.DocVec{{DocKey: "chat/a1", Vec: vec}, …}
tr, err := tree.Build("sea.knowledge", encoderID, docs,
    tree.DeterministicSummarizer{}, nil) // nil Embedder → 簇向量取均值
if err := tr.Validate(); err != nil { … }

assignment, dirty, err := tree.Assign(tr, newDocs) // 新文档就近入簇
dirty, err = tree.Prune(tr, removedKeys)           // 标脏，稍后重建
```

## 测试

`go test -race -count=1 ./service/async/rpc/internal/tree/` 覆盖：聚类
确定性（同输入两次、打乱输入后按 doc_key 规范化）、停机规则（上限、单例、
归并为一簇、空输入）、Build 结构合法性与确定性（含输入乱序、epoch 派生）、
环检测反例（手工坏树：脱离根的双节点环、父子不一致、双根、缺 members、
叶缺 doc_key 等）、Assign 最近簇与平局规则、stub 摘要确定性、稠密引用编解码。
