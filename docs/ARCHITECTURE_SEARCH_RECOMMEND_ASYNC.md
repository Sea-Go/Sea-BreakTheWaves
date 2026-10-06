# BTW 搜索、推荐与离线能力

统一 go-zero API 从 `api/btw.api` 生成。Search、Recommend、Async 通过进程内公开入口连接，不启动旧内部 RPC。完整来源、物品、关系和任务由 Async 拥有；Search 与 Recommend 读取其公开能力。

Search 包含 fast、balanced、deep 三条真实执行路径。Elasticsearch 管全文，Neo4j 管有依据的内容关系，Milvus 管 Dense、学习型 Sparse 和多向量。PostgreSQL 核对当前可见文章来源；S3 兼容存储供原文和引用回读。问答读取检索证据后运行 Agent；推荐的网站列表保持不调用生成模型。

模型选择和物理推理由 DataCenter 管理。BTW 运行配置只保留 DataCenter 地址，API／Worker 启动时读取当前表示空间、维度、索引集合与逻辑生成别名；部署层另行挂载服务身份。新的表示模型、索引和应用代码在隔离环境从完整来源重建，验收后由部署平台切流；BTW 不维护应用内模型发布指针。来源修订、Worker 租约和反馈属于业务状态。

详见 [搜索算法工程文档](architecture/search.html)、[推荐工程文档](architecture/recommendation.html)、[Benchmark 实验](architecture/benchmark.md)、[运行合同](architecture/operations.md)及[实测范围](architecture/validation.md)。
