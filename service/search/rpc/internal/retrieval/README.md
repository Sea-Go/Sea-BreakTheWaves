# retrieval —— M3 检索内核（整篇化，dev 形态）

`service/search/rpc/internal/retrieval` 把已就绪的域包串成端到端可运行的
**整篇检索 + 评测**切片（dev 形态）：从 M2 indexer 产出的工件集合装载
只读 `Store`，在其上执行三路整篇召回（dense 余弦 / sparse impact 内积 /
multi exact MaxSim）、RRF 融合与档位化检索，最终经 evidence 组装
`EvidencePack`。评测入口见
[cmd/retr_eval](../../cmd/retr_eval/)。

## 职责

| 能力 | 入口 | 说明 |
| --- | --- | --- |
| 工件装载 | `Load(sink, entries)` | 从 M2 工件集合（键 = `manifestID/<ref>`，与 indexer.ObjectKey 一致）解码三路表示；`artifactmirror.go` 提供 artifact 契约镜像 |
| 只读视图 | `Store` / `Snapshot()` / `NewStore(docs)` | 构造后只读、可并发检索；`AttachSource` 后置补结构树/源文本 |
| 三路整篇召回 | `Snapshot.Dense/Sparse/Multi` | 各自独立产生候选；分数降序、平局按 doc_key 字典序；只保留 score>0 |
| 融合 | `RRF(lists, k)`，`KDefault=60` | 公式 1/(k+rank)，rank 从 1 起 |
| 档位检索 | `Searcher.Search(ctx, req)` | 档位×能力矩阵（下表）→ RRF → 证据组装 → `evidence.EvidencePack` |
| dev 命中定位 | `firstParagraphSpan` | 文档第一个段落节点整段为 HitSpan（近似，见下） |

## 档位×能力矩阵（工程方案 §3.2）

| 档位 | dense | sparse | multi | 融合 | 说明 |
| --- | --- | --- | --- | --- | --- |
| `fast` | ✅ | ✅ | ❌（跳过） | RRF | 低延迟档：两路，刻意不含 multi 路结果（测试用探针 fake 断言） |
| `balanced` | ✅ | ✅ | ✅ | RRF | 三路全开 |
| `deep` | ✅ | ✅ | ✅ | RRF | 三路 + **每路候选 ×2**（`TopN`→`2×TopN`），模拟"多取 top"的放宽；真实 deep 档（更宽召回 + 重排）属后续 |

路内候选截断：fast/balanced 取 `Searcher.TopN`（默认 `evidence.MaxCandidates`=50），
deep 取 `2×TopN`；融合后 pack 条数再截到 `MaxCandidates`。

## dev 口径声明（哪些是近似）

1. **查询表示**（retr_eval 入口）：评测查询来自 gold 文档——dense = 相关
   文档去量向量均值、sparse = 相关文档种子文本词频 hash、multi = 主文档
   （rel 最高）multi 矩阵前 8 行。这是"理想查询编码"的确定性替身，
   真实查询编码器（DC representation）属后续。
2. **命中定位**：`Search` 取文档**第一个段落节点整段**为唯一 HitSpan
   （不按各路分数选最高分段）。真实命中定位（按段打分 + 多段证据）属
   后续。
3. **deep 档放宽**：以双倍路内候选模拟，未接真实 deep 语义（更宽召回 +
   重排）。种子集 20 篇文档 < TopN，故 deep 与 balanced 结果一致属预期。
4. **召回是全量精确扫描**：无 ANN/倒排索引；真实引擎（Milvus 等，
   `service/common/retrieval`）接入后替换 `Snapshot` 三路实现。

## 工件契约镜像（重要）

Go 的 internal 包可见性规则禁止 search 服务导入
`service/async/rpc/internal/{artifact,indexer}`（本仓架构红线，先例：
indexer/structure.go 镜像 evalseed TreeJSON）。`artifactmirror.go`
按"逐字节一致"原则镜像检索侧消费所需的最小面（DocEntry/manifest/
量化编解码/impact/对象键），并有黄金向量测试锁定与 artifact 侧的一致性
（期望值由真实 artifact 包产出）。**任何一侧改动必须双边同步。**

## 契约

- `Load(sink map[string][]byte, entries []DocEntry)`：sink 中必须恰有一个
  `<manifestID>/manifest.v1.json` 对象（indexer.ManifestKey 约定），其
  `manifest_id` 与键前缀、内容重算三方一致；每条 entry 的三路对象缺失或
  形状不符即整体失败。`DocEntry` 为镜像类型。
- `LoadedDoc.Sparse` 是 `map[uint32]float32`（u8 impact 线性值）；
  `Multi` 为按行切分的 token 矩阵（各行等宽，`NewStore` 校验）。
- `Search` 前置校验：`query_id` 非空、档位合法、三件套表示非全空
  （`ErrInvalidTier` / `ErrEmptyQuery` 哨兵）；候选缺结构树/源文本时在
  证据组装阶段报错。
- 确定性：三路排序、RRF、EvidencePack 排序全部"分数降序 + 平局 doc_key
  字典序"，同输入必得同输出（有测试强制）。

## 边界（明确不做）

- **不做编码**：查询/文档表示由调用方（或 retr_eval 的假编码）提供；
- **不做工件生产与切换**：归 M2 indexer 与 artifact.Switcher 层；
- **不做持久化与网络 IO**：进程内只读 Store；
- **不做真实命中定位/重排**：dev 近似，见上。

## 验收

```sh
GOCACHE=/tmp/gocache-m3 go test -race -count=1 ./service/search/rpc/internal/retrieval/... ./service/search/rpc/cmd/retr_eval/...
GOCACHE=/tmp/gocache-m3 go vet ./service/search/rpc/internal/retrieval/... ./service/search/rpc/cmd/retr_eval/...

# 评测入口（样例输出数字见 cmd/retr_eval/README.md）
GOCACHE=/tmp/gocache-m3 go run ./service/search/rpc/cmd/retr_eval
```

## 真实化路径

1. 查询编码：retr_eval 的 `buildRequest` 换真实查询编码器（与文档侧同
   encoder_id），gold 替身退役为回归对照；
2. 命中定位：`firstParagraphSpan` → 按各路分数选最高分段（段落级子索引）
   + 多段证据；
3. deep 档：双倍候选模拟 → 真实放宽召回 + rerank 阶段（`LaneScores.Rerank`
   字段已预留）；
4. 召回引擎：`Snapshot` 三路 → Milvus（`service/common/retrieval`），
   Store 装载路径对接 artifact.Switcher 的生效清单；
5. 镜像退役：若工件契约稳定，可上提为两服务可导入的共享包（需架构评审）。
