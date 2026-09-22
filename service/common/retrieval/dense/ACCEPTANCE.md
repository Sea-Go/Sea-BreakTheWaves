# Dense 实施与验收记录

2026-09-14，WS06-B 功能切片。**局部功能和真实本机联验通过；WS06-B 整体仍未验收通过。** 统一结构化日志、trace 与运行关联字段尚未接入新观测标准；完整失效事件消费者、正式大语料质量评测和生产规模后端仍有待交接。

## 已执行

| 范围 | 证据与结果 |
| --- | --- |
| 固定编码与工件 | 真正读取 ChunkManifest；检验必需修订/片段覆盖、正文/向量哈希、配置/模型/空间/维度/归一化，按最多 128 输入的配置批次调用 DC。原向量与定位写不可变分片；坏值、缺片、错代和取消不能返回成功。 |
| 独立精确召回 | 手算 `[1,0]` 对 `[1,0]、[3,4]、[0,1]、[-1,0]` 得 cosine `[1,.6,0,-1]`；dot 对 `[3,4]` 为 3。先筛选本次有效修订，空集合返回空；module/release/generation 不匹配拒绝。 |
| 消费者与恢复 | 真实 HTTP fixture 证明 query/document 配置固定和响应乱序 ID 关联；并发快照查询、取消、坏分片/错配置、文件重开、显式 ResumeIndex 均通过。 |
| SDK 协议 | 真 Milvus Go SDK 2.6.2 对本地 gRPC fixture，核验强一致、前置范围过滤、显式 metric/ef、错误不回落 exact、外域结果拒绝。此项独立于真实引擎证据。 |
| 真实 HNSW COSINE | 官方 Milvus Lite 3.2.1 固定提交 `43d1257774e629bc9f66873977ab7c320d5bf5a7`，Go SDK 实际建集合、批量 Upsert、Flush、Load、全覆盖计数、逐向量回读、探针与失效过滤通过。后端 float32 分数误差 ≤ 1e-5，原 float64 重算误差 ≤ 1e-12。 |
| 引擎进程重启 | 首次进程关闭后启动新进程，沿同一文件库只 Load/verify/search，不重新构建或 Upsert，全部通过。直接 `faiss.read_index` 确认落盘为 `IndexHNSWFlat`、M=16、efConstruction=128，不能把其类名中的 Flat 误作纯 FLAT 检索。见 [引擎证明](acceptance/lite-proof.json)。 |
| 真实 DC 编码 | 实际 DC 用户请求、已发布固定 configuration、真实 BGE-M3 `5617a9f61b02`、1024 维 FP32/L2/dot。4 个片段分别完成文件 exact 与 HNSW IP 建/读/查，4 个 self-query 均首位回收原片段；后端 IP 与原 float64 重算误差 ≤ 1e-5。两次编码工件 hash 相同，见 [exact](acceptance/real-dc-exact.json) 与 [HNSW](acceptance/real-dc-hnsw.json)，以及 [1024 维原生索引证明](acceptance/real-dc-hnsw-native.json)。 |
| 共享 SDK 修复 | 首轮真实 DC 返回 400 `invalid-idempotency-key`，暴露旧消费者漏传 key。独立提交 `5e8d39b` 修复权威 SDK：原 Represent 每次调用使用独立 key；RepresentWithKey 供持久化与显式重试。覆盖相同 body 不合并、503 后沿原 key 重试、非法 key 不发 HTTP；修复后沿原 DC 实例续验成功。 |

Go 检查：`go test ./...`、`go test -race ./internal/retrieval/dense ./internal/clients/datacenter`、`go vet ./internal/retrieval/dense`、`go mod verify`。根模块数据库门禁测试没有因本次普通 Go 回归就获得真实数据库验收；真实 DC 的专属 PG 由其独立测试 owner 验证。

## 本机引擎兼容核对

官方 [固定源码](https://github.com/milvus-io/milvus-lite/tree/43d1257774e629bc9f66873977ab7c320d5bf5a7) 的 gRPC 不完整复现 Standalone：自定义 index name 被忽略，构建参数只读取 nested `params`，schema description 不回传，表达式模板未展开。采用字段同名索引、同时提供可回读的 nested 参数、固定字段与 JSON 编码字面量过滤；`Engine:"lite"` 显式允许缺 description，但仍校验完整 digest 集合名、字段类型、索引参数、总数、每行修订/hash/原向量。没有修改依赖源码或取消域过滤。

最初未显式 metric 的 Lite gRPC 会返回距离；现每次 SDK 查询显式传 `metric_type`，核验原始结果为标准相似度/点积。Candidate 同时返回 `Score`、`BackendScore`、`BackendScoreKind`，没有用重算掩盖不明数值语义。首次编译没有捕获 `ColumnFloatVector.Get` 返回 `entity.FloatVector` 的类型差异；真实回读已捕获并修复。

## 使用与交接

- `New` 创建小语料文件 exact 服务；`NewMilvus` 借用显式 SDK client，client 创建者负责关闭。没有默认远端地址，也没有 drop collection 能力。
- `Build` 返回 `corpus.LaneIndex`/ref、编码数量和 usage。投影失败可从 `ProjectionError.IndexRef` 获得完整编码产物，再用 `BuildRequest.ResumeIndex` 显式续建；这一 ref 不能直接当 READY。
- `Open` 校验文件并创建可复用快照；`Load` 在此基础上加载并校验已存在的后端投影，不修复或重建。常驻检索进程应持有选定 Snapshot，避免每次重复读取全部向量。当前快照在内存中保存原向量，大语料资源预算尚未验收。
- `Snapshot.Search` 要求 `IndexRef/ModuleID/ReleaseID/Generation/ValidRevisionIDs/Text/TopK`。有效集合需由权威失效状态提供，空集合不是全域；失效事件消费与最终发布指针属于上游 content/search 集成。
- `VerifyAndProbe` 直接实现 content 的既有方法签名，实际读分片、回查后端全覆盖和数值、独立 query 编码并搜索。content 最终租约/fence/READY 提交不归本包。
- 历史 `--lite` 验收曾由包内脚本启动独占 Lite 进程；该临时验收脚本已删除，当前仓库不提供生产脚本入口。脚本输出属于验收证据，不是业务日志实现。真实 DC 检查用 `DENSE_DC_URL`、`DENSE_DC_CONFIG`、本次 token；`DENSE_REAL_BACKEND=milvus` 另需显式 Milvus 地址，不得默认使用共享生产。

## 尚未达到的门禁

1. **OBS 未达标**：构建/编码/分片验证/召回阶段没有统一结构化日志与 trace/span、operation/run/job/generation/fence 关联。包内没有 fmt.Print 业务输出，但仅返回 error/Usage 不能替代观测。等待权威运行时装配与字段标准，不自行新增另一套 logger。
2. 真实 Standalone/Distributed、规模内存/时延、ANN Recall@K 与正式 qrels 仍未验证；本次 4 片段 self-query 仅证明真实协议、数值和检索连通，不能证明语义相关性质量。
3. 从 RTW/DC 失效事件到查询有效集合的生产消费者、完整三路 content Reconciler/发布链联合验收由后续集成完成。
4. 所有实现留在开发分支，未推送、未合并主分支、未部署生产。

依赖的精确增量见 [dependencies.json](acceptance/dependencies.json)。Milvus SDK 必要 MVS 提升经过根模块回归；tRPC、pgx、OpenAI 原显式版本保持。

后续消费者成本对齐：Dense恢复不再把历史工件用量计入本次调用；第二批模型结果未知时保留首批已知用量并标记UsageUnknown。此项为成本口径回归，不改变原功能/OBS未完成边界。
