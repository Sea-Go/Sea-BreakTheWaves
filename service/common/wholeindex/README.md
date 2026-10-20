# wholeindex

整篇索引工件的**共享算法与契约包**（跨 async/search 两服务，根 module 下唯一实现）。

## 为什么存在

2026-10-08 复用审计发现：量化编解码、canonical JSON、manifest 内容寻址、对象键约定在 `async/internal/artifact` 与 `search/internal/retrieval` 的镜像文件里**各有一份逐字节重复的实现**（靠人工维护"双边同步义务"）。Go internal 可见性规则禁止跨服务导入，但 `common/` 是共享包——正确做法是上提到此处，两侧都导入。

## 内容

| 文件 | 职责 |
| --- | --- |
| `codec.go` | int8 标量量化（QuantizeF32/DequantizeI8）与多向量矩阵量化（QuantizeMulti/DequantizeMulti） |
| `manifest.go` | WholeDocIndexManifest 契约 + CanonicalJSON + ManifestID 内容寻址 |
| `sparse.go` | 稀疏 impact 编解码（Term/EncodeImpact/DecodeImpact） |
| `objectkey.go` | 工件对象键约定（ObjectKey/ManifestKey/TreeKey） |
| `structuretree.go` | 结构树 wire 契约（TreeJSON/NodeJSON + ParseStructureTree） |

## 消费方

- `service/async/rpc/internal/artifact`（经 alias.go 转发）
- `service/async/rpc/internal/indexer`（直接导入）
- `service/search/rpc/internal/retrieval`（直接导入）
- `service/search/rpc/internal/evidence`（TreeJSON/NodeJSON 别名）
- `service/search/rpc/internal/devseed` / `fakerepr`（直接导入）

## 与 RTW 的同步义务

`TreeJSON`/`NodeJSON` 是 RTW `structure` 契约的同仓镜像（跨仓不可导入 internal）；改字段/tag 需与 RTW 源头同步——此义务与上提前相同，未新增。
