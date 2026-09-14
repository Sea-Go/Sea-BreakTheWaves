# WS06-D 验收记录

日期：2026-09-14。固定基线 `dd7fd6c34a8d061364259ce582b444acb6cfc3ba`。本记录只证明 `internal/retrieval/multivector/` 的独立能力；搜索编排、正式 worker、Collector 下钻与整体 WS06-D 均尚未 `ACCEPTED`。

| 验收层 | 结果与可复核证据 |
| --- | --- |
| H05 数学和 mask | 手算 `sum_maxsim=1.8`、`mean_maxsim=0.9`；masked 非零、空有效矩阵拒收。独立 query token 行候选与完整矩阵 MaxSim 排序、修订过滤、空集合零编码、预算过低的严格 TopK 拒收、取消、聚合漂移、缺片和 token 目录损坏用例通过。 |
| 固定工件/恢复 | 4 个 chunk，2 个内容寻址分片，7 个有效 token 行；`Load` 在新 Service 中从工件恢复相同结果，`ResumeIndex` 零次 document 重编码并保留原用量，错代无法复用。 |
| 原生索引 | 官方固定来源 Milvus Lite 3.2.1、Go SDK 2.6.2：真实建 7 行 HNSW/IP、逐值回读、有效修订 ANN 前过滤、完整 MaxSim 对照、两个引擎进程之间只 `Load` 不重建。另建污染集合，把一行向量由 `[1,0]` 改成 `[2,0]` 而元数据/hash 不变；`Load` 和查询都拒收。FAISS 文件确认原集合 7 行、M=16、efConstruction=128；污染集合产生独立 delta 文件。见 [原生证据](acceptance/lite-proof.json)。 |
| 真实模型与网关 | 固定官方 BGE-M3 `BAAI/bge-m3@5617a9f61b02` 经 DataCenter 真 callpoint 返回 1024 维 `mean_maxsim` token 矩阵；4 个固定 chunk 编码用量 32 tokens、28 条有效 document token，两个分片逻辑矩阵值共 229376 bytes。将同一编码索引零新增编码投影至独立 Milvus HNSW/IP；query 3 个有效 token、4 个独立候选，84 个返回 token 行、84 次完整评分点积，与全部 chunk 的精确 MaxSim 结果逐候选一致。见 [真实联验结果](acceptance/real-dc-report.json)。 |
| 统一观测 | `WithTelemetry` 注入已有 BTW Bundle，真实 JSON 行有固定 service/version/component、Trace ID 和 Span ID；build 的两个 encode 子 Span 和 project 子 Span 共享父 Trace。成功 6 次、合同拒收 1 次出现在同一 Prometheus registry 且无 build ID 维度。仅包内 `LOCAL_VERIFIED`；正式 Agent Runner/Tool→worker→检索及 Collector→DataCenter 下钻未验收。 |
| 模块回归 | `go test -race ./...`、`go vet ./...` 通过；它覆盖 BTW 根模块，不覆盖旧 recommendation 独立子模块，也不把跳过的外部环境测试算通过。 |

原生两进程验收脚本为 `scripts/lite_acceptance.py`，本轮使用隔离构建的 `milvus_lite-3.2.1` wheel（SHA256 `0742fb4c54858bcc6fc3c2142ec651e2b0f4ae30f19e21d4fbc6db65b9e16c7b`）、PyMilvus 2.6.11、FAISS CPU。最终原始 Go 日志在 `/tmp/sea-multivector-verified.3cTynN/go-0.log`（SHA256 `913c9d4e99d1532081b33477fc4d6a45e438c54ce646c780e1f01904a0a7e8da`）与 `go-1.log`（`9fd6dab80c294411db68fb5ddfab96094d3eac0afc9dc1ccf60bdb816bd0d4fc`）；原生证明 JSON SHA256 `4f04d9d984db5d2e739d941cd664397a9b8ba2e971f31c7921d34e144dbe63fa`。

真实 DC/BGE 在只绑定任务回环端口的隔离 PostgreSQL、提供者、网关和 Milvus Lite 上执行；提供者与 DC 的 owner 自身 `go test` 通过后才发布一次性 runtime。最终报告 SHA256 `2b9f880ab9c72decaf8b8a78db06d2ea300c6f67653e6344247324c0c37fe133`；DC 测试日志 SHA256 `a66e99bc716129f46d8230bf149bb9ced4d0adc1cf4b915948bc898eedb0b175`。一次性 runtime 中的访问 token 没有提交，联验结束后测试 owner 已释放 BGE/DC/PostgreSQL，Milvus 进程也已停止。只复用已缓存且锁定 hash 的 2.30 GB 模型权重，没有再次下载或写生产。

仍需 WS06-E 以本路 `Query/Result` 接通正式搜索运行，WS06-A 在真实三路构建中调用 `VerifyAndProbe` 并交 RTW 接纳；还需有标注 query 集的预算/漏召回、内存峰值、磁盘和延时测试，独立 Standalone Milvus 验证，以及 OBS-r2 的完整框架业务链和实际 Collector 下钻。当前 `observability_status=LOCAL_VERIFIED`、`ws06d_status=PARTIAL`；测试报告的四个固定句子不能替代大规模相关性或上线效果验收。
