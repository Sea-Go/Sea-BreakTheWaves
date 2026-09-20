# Multi-vector 独立召回

本域使用 DataCenter H05 `token_matrix` 强类型表示。每个有效 document token 建一条独立索引行，query token 先在本路索引内召回并按 chunk 去重，随后读取候选的**完整** token 矩阵计算 MaxSim。它不接受 Dense/Sparse 的候选作为唯一入口，也不把矩阵压成单个 Dense 向量。

## 责任与合同

工作区域：[W0] `feat/multivector-retrieval-20260914` 独立工作树；[W1] `internal/retrieval/multivector/`；[R1] `corpus`、DataCenter H05、`internal/telemetry`、Dense/Sparse 和 Docs；[D1] tRPC-Agent-Go v1.8.1、Milvus Go SDK v2.6.2、隔离 Lite 3.2.1；[G1] 内容寻址分片与 `LaneIndex`；[X1] 任务专属 BGE/DC/PostgreSQL/Milvus；[N1] 原始业务树、其他 lane、正式发布和生产；[T1] `/tmp/sea-multivector-*` 验收目录。主职责 C6/C7，跨 C8。

tRPC-Agent-Go v1.8.1 的 `knowledge/vectorstore.VectorStore.SearchQuery` 只包含单个 `Vector []float64`，不能表达文档逐 token 矩阵和完整 MaxSim。Agent 的 Runner、Tool 与其原生观测由上层装配；此包实现确定性索引与评分，复用固定 DC SDK 和共享 telemetry Bundle，不复制框架内部实现。

`Config` 固定 document/query 的 callpoint、配置 ID、实际模型及共同的 Contract/Space。`Profile` 必须为 `lane=multivector`、`mask=valid`、契约规定的 `sum_maxsim` 或 `mean_maxsim`；模型、tokenizer、空间、维数与聚合均不可在打开旧索引时漂移。H05 mask 中 `true` 才是有效行；masked 行必须全零。所有有效行有限且非零，L2 声明时逐行检查单位范数；空有效矩阵拒绝，不补虚构 token。

`sum_maxsim` 对每个有效 query token 取其与全部有效 document token 的最大内积并求和；`mean_maxsim` 再除以**有效 query token 数**。BGE-M3 使用后者，两个分数空间不能默默互换。导出的 `MaxSim` 是精确数值真值函数。

## 构建、检索和恢复

- `Build` 只读固定 `ChunkManifest`，逐批调用 DC document 表示，按 item ID 对齐乱序响应。每个内容寻址分片保存 chunk/原文位置、完整矩阵/shape/mask/hash、模型绑定、用量和 token 行目录；`Open` 回读并重新计算行目录、矩阵 hash、数值和必需 chunk 覆盖。缺片、错修订、错空间或损坏行不能作为本路就绪证据。
- 本地 `New` 从持久分片重建精确 token 行索引，逐 query token 在**全部有效修订**的本路 token 行取有界 TopK，然后对去重 chunk 做完整 MaxSim。`NewMilvus` 在独立 digest 集合使用原生 FloatVector HNSW/IP，行主键为固定 chunk/position 的 hash，并在 ANN 前过滤 index hash 和有效修订。每个返回 token 行还核对修订、矩阵 hash、position 和原生 IP 分数；最终排名只使用完整矩阵的 MaxSim。
- 每个查询必须指定 index ref、module、release、generation 和 `ValidRevisionIDs`；空有效集合直接返回空而不请求模型。`TokenTopK` 为每个有效 query token 的候选预算，不能超过服务配置。预算过小可能漏召回；`VerifyAndProbe` 对同一个固定语料计算全量精确 MaxSim TopK，拒绝严格边界漏失，不用自身 `probe_passed` 自证。
- 完整索引在 backend 投影失败时以 `ProjectionError.IndexRef` 保留，调用者须显式重试 `ResumeIndex`；它重新核对全部工件，零次新编码地投影同一代。`Usage` 是本次已确认消费，`StoredUsage` 是原始编码成本；失败中的未知模型请求由 `UsageUnknown` 标记。`Load` 只打开/核验已存在的集合，不能创建、修复或 Upsert。Store、Encoder 和 SDK Client 均由调用方管理生命周期；没有删除集合的 API。

`BuildResult.ActiveTokenRows` 和 `MatrixValueBytes` 是 token 数和矩阵 float64 值的**逻辑字节数**，不等于 Go 堆内存或 JSON/索引磁盘占用。查询 `Cost.BackendTokenRowsObserved` 在精确后端是计算过的行数，在 Milvus 是返回行数；SDK 不提供 ANN 内部距离计算次数。`ScoredMatrixValueBytes` 是被完整 MaxSim 打分的候选矩阵逻辑字节数，`ExactDotProducts` 是完整打分点积数。上层应缓存一次 `Load` 后的只读 `Snapshot`；直接调用 `Service.Search` 会重新打开固定分片。单分片超过 `artifacts.MaxBytes=16 MiB` 时构建失败，不拆分为不完整的 READY。

## 观测与交接

可用 `WithTelemetry` 注入 BTW 进程唯一 Bundle；build、每批 encode、project、load、search、probe 有结构化阶段开始/终态、Trace 及固定标签指标。错误按取消、超时、合同拒收和其他失败区分。测试中的 JSON、Span 及指标验证的是**此包的注入路径**；尚未有正式 worker/Search Graph 的完整调用链或 Collector→DataCenter 下钻，OBS 状态为 `LOCAL_VERIFIED`，WS06-D 整体仍为 `PARTIAL`。

向 WS06-A 交 `LaneIndex`、`VerifyAndProbe` 和矩阵/覆盖证据；向 WS06-E 交 `Query`、本路 `Candidate`、分数及真实成本；向 WS09-E 交精确对照与预算实验输入。RTW 仍拥有手动激活指针，编制结果不自动发布。真实验收与限制见 [ACCEPTANCE.md](ACCEPTANCE.md)。
