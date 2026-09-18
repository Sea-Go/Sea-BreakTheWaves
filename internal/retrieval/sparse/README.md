# 学习型 Sparse 独立召回

本路消费 DataCenter 的真实稀疏 token ID/权重，生成固定分片和倒排索引，独立检索正内积候选。已完成本地倒排、原生稀疏 IP 引擎及真实 BGE-M3 的功能/数值验证；**统一观测未实现，WS06-C 整体仍为 PARTIAL，不能标为 ACCEPTED**。

## 边界与依赖

工作区域：[W0] `feat/sparse-retrieval-20260914` 独立工作树；[W1] `internal/retrieval/sparse/` 和本分支必要 Go 依赖；[R1] corpus/H05、Dense 接口形状；[D1] tRPC-Agent-Go v1.8.1/Milvus SDK；[G1] 固定稀疏分片与索引清单；[X1] 经协调的本机 DC/BGE、任务专属原生 Milvus 实例；[N1] Dense/corpus 实现、业务发布和生产；[T1] 验收工件。主职责 C6/C7，跨 C8。SDK 幂等头修复经集成 writer 授权接纳 `5e8d39b`，本域未重写该客户端。

tRPC v1.8.1 `VectorStore.SearchQuery` 只有 `Vector []float64`，无法表达独立 token ID/权重及词表。本域复用 DC typed `Encoder` 与官方 Milvus `SparseEmbedding` 公开接口，不把稀疏展开成 Dense，不调用 BM25，不复制框架内部源码。

Milvus Go SDK 固定 `v2.6.2`，必要传递依赖经 Go MVS 提升；tRPC 核心仍为 `v1.8.1`、pgx `v5.8.0`、OpenAI Go `v1.12.0`。根模块全包 race/vet 已回归；旧 recommendation/agent_v3 是独立模块，不由根 `go test ./...` 覆盖。

## 数据与数学

`Config` 固定 document/query 的 callpoint、configuration ID、physical model，以及同一 Contract/Space；Contract 包含 tokenizer、vocabulary、完整词表维数、归一化及重复 token 聚合声明。BGE 重复 token 已由官方 Provider 取 max，本路检查严格递增唯一 ID 并原样存储，不再求和或改为词频。

本路当前支持有限正权重。H05 明确拒绝空/零表示；空稀疏向量、负权重、重复/越界 token、非有限数均报错，不造一个 token 补齐，也不把缺失文档从覆盖分母移走。有效 query 没有共同词项时返回空候选；没有正内积就不填充无关零分文档。

`Dot` 使用有序稀疏向量的双指针内积，作为精确参考。本地查询只遍历 query 涉及的持久 posting lists；Milvus 使用 `SPARSE_FLOAT_VECTOR`、`SPARSE_INVERTED_INDEX`、`IP`，build/search drop ratio 固定 0。传入 Milvus 前检查每项是否可无溢出/非零地下转 float32；不会悄悄丢掉下溢词项。

## 接口与固定工件

- `New` 创建本地精确倒排服务；`NewMilvus` 借用显式 SDK client，构造时不写集合。Store/Encoder/SDK client 由装配者关闭。
- `Build` 读取固定 `ChunkManifest`，逐批 document 编码并按 ID 对齐乱序响应；持久分片保存 chunk/原文位置、完整表示绑定、向量、向量 hash、usage 和 posting lists。完整分片经验证后才输出 `LaneIndex`。
- `ResumeIndex` 只接受完整且匹配同一 build/generation/chunk/input/profile 的索引工件；恢复 backend 投影不再编码。`Usage` 为本次已确认编码用量，`StoredUsage` 保留原始工件用量，`EncodedChunks`/`ReusedChunks` 分开记录。只有部分分片、尚未产出完整索引时，重试可能重新编码，不能将其称为零成本恢复。失败时保留前面成功批次的已知用量，`UsageUnknown=true` 表示失败批次的费用需由 DC 回查，不虚报为 0。
- `Open` 重新校验全部文件 hash、固定绑定、源位置/正文 hash、逻辑 chunk 覆盖、数值与 posting lists；posting 内容必须能由同分片的真实向量精确重建。Snapshot 之后只读，可供并发查询。
- `Load` 在引擎重启后打开固定工件并显式 `LoadCollection`、核对每个字段/索引类型/实际向量；不 Build、Upsert、重新编码或静默修复。
- `Search` 必须绑定 index ref、module、release、generation 和显式 `ValidRevisionIDs`。空有效集合直接空返回，且不请求模型；Milvus 在检索前过滤修订和 index hash，再对返回候选做本地检查。
- `VerifyAndProbe(context.Context, corpus.LaneIndex, corpus.ChunkManifest, []corpus.Chunk)` 满足 content 的 LaneVerifier；本域只 import corpus 值类型。它读取真实分片、回读真实引擎值、独立 query 编码，并对照精确倒排 TopK 的严格候选边界，不能用 producer 的 probe_passed 自证。

Milvus 集合名包含 namespace、固定 index 摘要及引擎参数摘要，行中再保存 index/vector hash。未提供任意删除集合入口。Search 会检查后端原始分数与实际 float32 稀疏 IP 的一致性；本地重算不能掩盖 BM25、错误 metric、错误向量或损坏投影。

## 已执行验收

2026-09-14，详见 [`acceptance.json`](acceptance.json)。原始证据位于任务临时交付目录，其日志 hash 已记入摘要。

| 范围 | 结果 |
| --- | --- |
| 固定小向量 | 精确分数 8/4/2；query 权重倍增得 16/8/4；document 对应 token 权重倍增也线性增分 |
| 本地边界 | 空有效集合不编码；无交集空候选；损坏 postings、缺片、改来源、词表漂移、负/空/非法向量、取消和作用域错配拒收；恢复编码成本为 0 |
| 原生引擎 | 官方 SDK 实际创建 SPARSE_INVERTED_INDEX/IP、逐项回读稀疏向量、完整候选和分数对照、有效修订预过滤通过 |
| 实际损坏 | 原生集合向量被放大、metadata/hash 字符串保持不变；Load 准确报实际值漂移，Search 准确报后端非预期 IP，均未被本地重排掩盖 |
| 进程恢复 | 停止原进程后，用同一数据库启动第二个进程，显式 Load 后继续查询；没有重新 Build/Upsert/编码，无交集/空有效集合仍空 |
| 真实 DC+BGE | 4 个 chunk 的官方 BGE-M3 学习权重，经 DC 固定配置编码，得到持久 exact 真值后零新增编码投影到原生索引；3 个候选及后端原始分数与真值一致，Probe/逐值回读通过 |
| Go 回归 | `go test -race ./...`、`go vet ./...` 通过；数据库及外部模型门禁未配置的用例明确 skip，不作为对应真实环境证据 |

真实 Sparse 编码原始用量为 32 tokens，同工件引擎投影新增编码为 0；示例查询的三个分数约 0.11008256、0.10491808、0.09892056。它们证明指定输入的数值一致，不是泛化相关性或排序效果评测。

## 原生测试环境

采用官方 `milvus-lite 2.5.1` macOS ARM64 wheel，PyMilvus `2.6.11`，兼容依赖 setuptools `75.8.0`。原生二进制 SHA256：`cc2c3a13237f94bf0889a84fcee1d5e53c411bdd48d105ba7d11673e9682f6b9`；wheel URL/hash 见 [`native-engine.lock.json`](native-engine.lock.json)。旧 Lite 不具备真实 HNSW 的限制与本任务无关，本次只接纳已实证的 Sparse IP 能力。

没有使用 Dense 的 Lite 3.2.1 实例：固定源码 `43d1257774e629bc9f66873977ab7c320d5bf5a7` 的 `sparse_inverted.py` 实际执行 BM25，即使名字是 SPARSE_INVERTED_INDEX，也不能冒充学习权重 IP。Milvus 的字段和索引协议依据[官方稀疏向量文档](https://milvus.io/docs/sparse_vector.md)及固定 Go SDK 源核对。

启动只用于验收，默认不会创建常驻进程：

```bash
uv run --no-project --python 3.12 \
  --with pymilvus==2.6.11 --with milvus-lite==2.5.1 --with setuptools==75.8.0 \
  python internal/retrieval/sparse/native_acceptance.py
```

脚本输出独立目录和随机 endpoint；设置 `SPARSE_MILVUS_ADDRESS` 后运行 `TestRealMilvus`。在目录创建 `release` 文件即可请求 owner 收尾，必须等待该进程退出，再用 `--restore-directory` 对同一任务目录启动新实例。`SPARSE_RESTORE_RECORD` 指向首次 `milvus-projection.json`，执行 `TestRestoreMilvus`。`SPARSE_DC_RUNTIME` 仅接收 DC 联验 owner 的一次性本机 runtime 文件；测试报告不会复制其中的 token。

本轮两个原生进程均已结束，DC/BGE 由其独立 owner 确认结束；未写生产，也未停止 Dense 的服务。

## 观测与剩余门禁

本域原始 `acceptance.json` 固定在 **OBS-2026-09-14-r1** 时形成，作为历史数值证据保留，不追改工件。当前统一门禁已升级为 Sea-Docs 中的 **OBS-2026-09-14-r3**：BTW 必须在实际业务入口中同时证明 tRPC-Agent-Go 原生 Agent/Graph/Tool/Model Span 与应用阶段、结构日志、指标和 Collector/DC 下钻，不能以公共 Bundle 或另一业务路径代验。本 Sparse 包的 `observability_status=NOT_IMPLEMENTED`：尚缺该路径的 build/encode/project/load/search/probe 阶段日志与真实 Trace/Span、操作/失败指标和 Collector 回查。返回 Usage/候选数、错误链和测试摘要不等于观测实现。

本域新增 Go 路径没有直接打印作为生产日志；验收脚本 JSON 是测试握手，不作为 OBS 证据。待仓库统一 telemetry 接口接入后，本域仍须记录 build/encode/project/load/search/probe 的开始/终态，以及空召回、取消、词表/工件/后端分数错配和恢复成本。不能推给 WS09 最后补看板。

尚未验证完整 standalone Milvus 部署、生产吞吐与大规模持久化容量、相关性评测或整个搜索产品；这些与本轮原生 Sparse IP 功能证据分开验收。
