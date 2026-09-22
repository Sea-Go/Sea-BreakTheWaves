# 本地内容准备与索引 Worker

`cmd/worker` 另有显式 `BTW_JOB_TYPE=usermodel.favorite-facts.v1` 的收藏事实消费者。该模式使用独立用户事实 PostgreSQL、RTW 私有收藏权威读、DC 事件批次与现有 tRPC-Agent-Go FactGraphRuntime，不初始化内容工件或内容 Session；下文内容准备/索引配置与行为保持原样。收藏模式仅在所有专用字段齐备时启动，不能因为内容模式的默认值而意外开启。验收及运行边界见 [收藏事实进程验收](FAVORITE_PROCESS_ACCEPTANCE.md)。

此入口真实装配 DataCenter 技术任务客户端、RideTheWind 固定版本读取客户端、BTW 内容 Postgres Store、本地 SHA256 工件、tRPC-Agent-Go GraphAgent/Runner 与框架 Postgres Session。一个进程只领取一个明确的技术任务类型：默认 `content.prepare.v1` 生成 chunk manifest；显式 `content.build.v1` 使用三路真实本地 exact 索引实现、各自的 `Build` 与 `VerifyAndProbe`，并在固定代通过协调器、Reconciler、本地 PostgreSQL READY 后请求 RTW `AcceptBuild`，再给 DataCenter 技术回执。RTW READY 不移动人工发布指针。`BTW_ARTIFACT_STORE=local` 是当前硬限制，工件目录必须由本地 RTW 联调进程共享；尚无生产对象存储适配，因此不得将此二进制解释为生产发布入口。

所有配置均从环境变量读取；启动缺字段即退出，且不会打印 DSN 或令牌。布尔迁移开关必须明确为 `true` 或 `false`：

| 变量 | 含义 |
| --- | --- |
| `BTW_MODE=local`, `BTW_ARTIFACT_STORE=local` | 明确本地模式和本地工件实现 |
| `BTW_JOB_TYPE` | 留空或 `content.prepare.v1` 为兼容准备入口；索引进程必须显式指定 `content.build.v1` |
| `BTW_INDEX_BACKEND=exact`, `BTW_INDEX_CONFIG_FILE` | 仅索引进程需要。文件为固定三路 typed JSON 配置，当前只接受 `exact`；Milvus 尚未装配 |
| `BTW_WORKER_ID`, `BTW_RESOURCE_PROFILE`, `BTW_LEASE_SECONDS` | 固定 worker 身份、资源型和 5–3600 秒租约 |
| `BTW_POLL_INTERVAL`, `BTW_HTTP_TIMEOUT` | Go duration，例如 `500ms`、`10s` |
| `BTW_DC_URL`, `BTW_DC_TOKEN` | DataCenter API 与技术令牌 |
| `BTW_RTW_URL`, `BTW_RTW_TOKEN` | RideTheWind 知识 API 与 worker 令牌 |
| `BTW_CONTENT_POSTGRES_DSN`, `BTW_CONTENT_SCHEMA`, `BTW_CONTENT_MIGRATE` | 内容执行账本的独立数据库、已有 schema、是否显式执行 GORM `content.Migrate` |
| `BTW_ARTIFACT_DIR` | 与本地 RTW 共享的工件目录 |
| `BTW_CHUNK_PROFILE_ID`, `BTW_CHUNK_SIZE`, `BTW_CHUNK_OVERLAP` | 仅准备进程需要。固定 chunk profile；参数改变需要新 profile ID |
| `BTW_SESSION_POSTGRES_DSN`, `BTW_SESSION_SCHEMA`, `BTW_SESSION_TABLE_PREFIX`, `BTW_SESSION_INITIALIZE` | tRPC-Agent-Go 框架 Session 数据库及是否初始化 |
| `BTW_OTLP_TRACES_URL` | 完整 OTLP HTTP traces URL，例如 `http://127.0.0.1:4318/v1/traces` |
| `BTW_METRICS_ADDR` | 仅监听回环地址的 `/metrics`，例如 `127.0.0.1:9091` |
| `BTW_SERVICE_VERSION`, `BTW_ENVIRONMENT`, `BTW_INSTANCE_ID` | 完整 40 位 Git SHA、`local`/`test`、实例标识 |

索引配置文件顶层严格包含 `dense`、`sparse`、`multivector` 三个对象，分别解码为对应包的 `Config`；每路需固定 `document` / `query` 的 callpoint、configuration UUID 与 physical model，以及 representation `contract`、`space`、batch/probe 上限。Multi-vector 另需 `token_top_k`。所有字段都由各路构造器验证，且构建时必须与 RTW 固定 Release 对象中的三路 profile 完全匹配；文件不是临时指定 Release 或覆盖权威 profile 的途径。完整可运行的测试配置见 `index_test.go` 的 `fixedIndexSettings`。配置文件不含 DC/RTW token，令牌只经环境变量注入。

先在专属数据库建立 `BTW_CONTENT_SCHEMA` 与 `BTW_SESSION_SCHEMA`，或使用其已有 `public` schema。首启时按部署步骤显式设置两个初始化开关，后续运行设为 `false`。准备进程和索引进程应使用不同的 `BTW_SESSION_TABLE_PREFIX`，并分别运行 `go run ./cmd/worker`。进程启动后先检查 JSON `content.worker.started` 中的 `job_type`，再抓取 `/metrics`；收到 SIGINT/SIGTERM 后停止轮询并依次关闭 Metrics、Runner/Session、内容连接池和遥测 Bundle。DataCenter 无待领取任务是正常空轮询，不生成 `READY`。

本目录 `acceptance.sh` 创建临时 PostgreSQL 16 的 content/sessions 两库，并在 Go 测试中启动本地 DC/RTW/OTLP HTTP 端点，构建并运行两个真实 worker 子进程。准备进程验证固定 `content.prepare.v1` 任务的 chunk 回执、RTW claim、框架原生 Span/指标、工件、本地账本、`/metrics`、`traceparent`、JSON 与 SIGTERM。索引进程从已准备的固定 chunk 运行真实三路本地 exact 实现，检查 DC typed representation 调用、三路索引工件/独立探针、本地 READY、RTW READY、DC 技术 ACK 与框架原生 Graph Span。普通包内测试的 HTTP 端点是隔离协议 fixture；`TestRTWRealBGEWorkerProcesses` 需由 RTW `bge_worker_process_acceptance.sh` 提供真 RTW/DC/锁定 BGE，验证两个命令进程和一次 DC ACK 故障后重启补投。`TestRTWRealBGEWorkerLeaseExpiry` 由 RTW `bge_worker_lease_expiry_acceptance.sh` 驱动，验证真 DC PG 租约过期、旧 attempt/fence 被拒与替代进程恢复。`TestRTWRealBGECancelNewRelease` 由 RTW `bge_worker_cancel_new_release_acceptance.sh` 驱动，验证显式双端取消、新 Release ordinal、新 Build generation 2、旧复投拒收及已有发布指针不变。三者仍使用 local-exact 和共享本地工件，未验证 Milvus、生产对象存储或新 Release 的人工发布。详细边界见 [索引入口验收](INDEX_ENTRY_ACCEPTANCE.md)。

取消/新代测试的 0600 报告另交出与索引进程同源的三路 `api_index_settings`、索引 manifest 和 lane Ref，供 RTW `bge_worker_publish_search_acceptance.sh` 在测试管理员显式激活后装配正式 `cmd/api`。RTW 的签名 SearchSnapshot 仍是在线选择发布版本的权威；报告中的配置不授予发布权，也不替代生产配置管理。
