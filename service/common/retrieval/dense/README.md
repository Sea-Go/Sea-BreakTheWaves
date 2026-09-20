# Dense 独立召回

工作区域：[W0] 本仓库；[W1] `internal/retrieval/dense/`、根 `go.mod/go.sum`；追加授权的 `internal/clients/datacenter/client.go` 与对应测试；[R1] `internal/corpus`、`internal/content`、DC 客户端及 Sea-Docs；[D1] Go 缓存；[G1] 无手改生成物；[X1] 仅显式指定的任务专属 Milvus collection；[N1] 其他模块与现有集合；[T1] 测试临时目录。主职责 C4，跨区契约为 H05 强类型表示、H06 不可变工件、H07 候选。

本包拥有 dense 一路。构建读取固定 ChunkManifest，验证正文哈希和来源覆盖，固定 query/document 编码配置、tokenizer、空间、维度、归一化与度量。编码调用通过 DC typed Represent，工件保存原始 float64 向量、输入定位与逐向量哈希。批次边界检查取消，部分工件不会自动获得 READY 或发布资格。content 的最终租约提交仍由 content 持有。

`Build` 产出 corpus.LaneIndex；`Open` 校验所有固定分片并建立可复用只读快照；`Search` 为便捷入口，重复查询应复用 Open 返回的 Snapshot。查询必须传 module、release、generation 和本次有效 revision 集合；空集合返回空，永远不表示无限范围。有效集合由调用者从权威失效状态获得，本包不假设修订永久有效。发布和失效消费者仍是外部合同。

精确文件后端用于小数据数值与恢复验收；Milvus 后端使用 SDK 的 HNSW、COSINE 或固定契约的 IP，不使用 exact 替代失败的 ANN。Milvus 中的 float32 近似召回候选使用原 float64 向量重算分数，保留 ANN 原始分数；因此精排数值误差与 ANN 漏召回可独立评价。不能把重算候选分数当成全域精确召回。

框架核对：核心 trpc-agent-go v1.8.1 的 VectorStore 与独立 milvus v1.8.0 公开适配支持 HNSW，但 New 自动创建/加载并附带 BM25；Add 为逐条 Insert，公开接口没有 Flush、强一致读、单次 HNSW ef 或集合 schema/index 校验。固定代批量投影需这些提交/查询边界，故本包直接组合既有 Milvus Go SDK v2.6.2 公开 client/entity/index/column，而不复制框架 internal。新增依赖只接受 SDK 的必要 MVS 提升，既有 tRPC、pgx、OpenAI 显式版本保留。

验收结果与命令见 `ACCEPTANCE.md`。测试编码器是协议 fixture，不能证明真实语义编码质量。

恢复时先使用 `Load` 校验已有 Milvus 投影；`Open` 只读文件，不能自动加载重启后的引擎集合。构建的 backend 失败以 `ProjectionError.IndexRef` 交还完整编码结果供显式 ResumeIndex，不隐式变更发布状态。共享 DC SDK 的 Represent 为独立调用，需跨进程恢复同一次模型调用时使用 RepresentWithKey 并持久化其 key。

Milvus Lite 必须显式 `Engine:"lite"`。采用完整 digest 集合名和实际回读验证，适配其缺少 description、固定索引名称及 nested 参数协议；查询明确传 metric，BackendScoreKind 为 cosine_similarity 或 dot_product。正式 Standalone 与分布式容量尚未验收。统一业务日志/trace 尚未达到新标准，见验收记录，不以当前函数返回值替代运行观测。

用量语义与Sparse一致：BuildResult.Usage只计本次确认的模型调用，StoredUsage是完整旧工件的历史用量；ResumeIndex不重新编码，本次Usage和EncodedChunks为0，ReusedChunks为复用数量。失败保留已确认批次的Usage，模型结果不明时UsageUnknown=true，不把未知成本记为0。
