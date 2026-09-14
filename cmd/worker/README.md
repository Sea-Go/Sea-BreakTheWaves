# 本地内容准备 Worker

此入口真实装配 DataCenter 技术任务客户端、RideTheWind 固定版本读取客户端、BTW 内容 Postgres Store、本地 SHA256 工件、tRPC-Agent-Go GraphAgent/Runner 与框架 Postgres Session。它只完成 chunk manifest 的技术准备与 DataCenter 任务回执；Dense、Sparse、Multi-vector 三路检索和 RTW `READY` 提交由独立任务负责。`BTW_ARTIFACT_STORE=local` 是当前硬限制，工件目录必须由本地 RTW 联调进程共享；尚无生产对象存储适配，因此不得将此二进制解释为生产发布入口。

所有配置均从环境变量读取；启动缺字段即退出，且不会打印 DSN 或令牌。布尔迁移开关必须明确为 `true` 或 `false`：

| 变量 | 含义 |
| --- | --- |
| `BTW_MODE=local`, `BTW_ARTIFACT_STORE=local` | 明确本地模式和本地工件实现 |
| `BTW_WORKER_ID`, `BTW_RESOURCE_PROFILE`, `BTW_LEASE_SECONDS` | 固定 worker 身份、资源型和 5–3600 秒租约 |
| `BTW_POLL_INTERVAL`, `BTW_HTTP_TIMEOUT` | Go duration，例如 `500ms`、`10s` |
| `BTW_DC_URL`, `BTW_DC_TOKEN` | DataCenter API 与技术令牌 |
| `BTW_RTW_URL`, `BTW_RTW_TOKEN` | RideTheWind 知识 API 与 worker 令牌 |
| `BTW_CONTENT_POSTGRES_DSN`, `BTW_CONTENT_SCHEMA`, `BTW_CONTENT_MIGRATE` | 内容执行账本的独立数据库、已有 schema、是否显式应用 `migrations/content.SQL` |
| `BTW_ARTIFACT_DIR` | 与本地 RTW 共享的工件目录 |
| `BTW_CHUNK_PROFILE_ID`, `BTW_CHUNK_SIZE`, `BTW_CHUNK_OVERLAP` | 固定 chunk profile；参数改变需要新 profile ID |
| `BTW_SESSION_POSTGRES_DSN`, `BTW_SESSION_SCHEMA`, `BTW_SESSION_TABLE_PREFIX`, `BTW_SESSION_INITIALIZE` | tRPC-Agent-Go 框架 Session 数据库及是否初始化 |
| `BTW_OTLP_TRACES_URL` | 完整 OTLP HTTP traces URL，例如 `http://127.0.0.1:4318/v1/traces` |
| `BTW_METRICS_ADDR` | 仅监听回环地址的 `/metrics`，例如 `127.0.0.1:9091` |
| `BTW_SERVICE_VERSION`, `BTW_ENVIRONMENT`, `BTW_INSTANCE_ID` | 完整 40 位 Git SHA、`local`/`test`、实例标识 |

先在专属数据库建立 `BTW_CONTENT_SCHEMA` 与 `BTW_SESSION_SCHEMA`，或使用其已有 `public` schema。首启时按部署步骤显式设置两个初始化开关，后续运行设为 `false`。例如在填写完整环境变量后运行 `go run ./cmd/worker`。进程启动后先检查 JSON `content.worker.started`，再抓取 `/metrics`；收到 SIGINT/SIGTERM 后停止轮询并依次关闭 Metrics、Runner/Session、内容连接池和遥测 Bundle。DataCenter 无待领取任务是正常空轮询，不生成 `READY`。

本目录 `acceptance.sh` 创建临时 PostgreSQL 16 的 content/sessions 两库，并在 Go 测试中启动本地 DC/RTW/OTLP HTTP 端点，构建并运行真实 worker 子进程。它投递一笔固定 `content.prepare.v1` 任务，验证 DC 领取与成功回执、RTW build claim、框架 GraphAgent/Runner 的原生 Span 与指标、chunk 工件及本地账本、`/metrics`、跨服务 `traceparent`、单行 JSON 和 SIGTERM 退出。测试还验证 RTW build 维持 `BUILDING`，不凭 chunk 回执制造三路 `READY`。HTTP 端点是隔离契约 fixture，不能代替真实 DC/RTW 服务与实际 Collector 查询后端的跨仓验收；生产对象存储仍未实现。
