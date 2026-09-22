# 收藏事实常驻 Worker 的隔离验收

2026-09-15，独立 BTW 分支 `feat/favorite-fact-worker-process-20260915`。工作范围：[W0] 本隔离 BTW 工作树；[W1] `cmd/worker` 入口、配置、进程验收与本记录；[R1] 已交付的 BTW `internal/app`、RTW 收藏来源与 DC `cmd/platform`；[D1] 当前 Go 模块锁定的 tRPC-Agent-Go v1.8.1；[G1] 生成的 DC wire 文件只读；[X1] 脚本自建的 PG16、RTW 私有 HTTP、DC 回环服务与 OTLP 测试收集器；[N1] 原业务脏树、其他集成分支、Docs、生产服务；[T1] 随机临时数据目录。主职责 [C2] 进程装配，跨 [C1] DC/RTW 协议、[C4] 用户事实 PG/Outbox、[C5] tRPC Runner/Graph、[C8] race 进程验收。

## 正式入口合同

只有显式 `BTW_JOB_TYPE=usermodel.favorite-facts.v1` 才进入收藏模式。其配置为 `BTW_MODE=local`、`BTW_DC_URL`、`BTW_DC_TOKEN`、`BTW_FAVORITE_AUTHORITY_URL`、不少于 32 字节的 `BTW_FAVORITE_AUTHORITY_TOKEN`、`BTW_FACT_POSTGRES_DSN`、`BTW_FACT_SCHEMA`、`BTW_FACT_CONSUMER`、`BTW_FACT_BATCH_LIMIT`（1–128）、`BTW_POLL_INTERVAL`、`BTW_HTTP_TIMEOUT`、`BTW_OTLP_TRACES_URL`、回环 `BTW_METRICS_ADDR`、完整 SHA `BTW_SERVICE_VERSION`、`BTW_ENVIRONMENT`、`BTW_INSTANCE_ID`。缺任一专用字段均拒启动；该模式不要求内容工件、内容 PG、索引或框架 Postgres Session 配置。默认或原 `content.prepare.v1`、显式 `content.build.v1` 保留原检查与装配。

事实库必须**事先**在指定 schema 执行 `migrations/usermodel/001_facts.sql`；入口只验证 `usermodel_events` 与 `usermodel_outbox` 已存在，不自动迁移。进程固定 DC producer `rtw.community.favorite`、schema v1 以及 `rtw.favorite.assert/retract` 双类型白名单。每个类型共享真实 RTW `FavoriteAuthorityBinder`：读取 RTW 冻结 EventSpec/SubjectRef/前驱与来源技术回执，逐项与 DC batch、JCS hash、immutable receipt 对照；SubjectRef 不从 DC payload 自报值取得。已迁移用户事实 Store 在 tRPC-Agent-Go Runner/Graph 节点内原子提交接纳版本与 Outbox；整个有界批均有当前 accepted receipt 后才 ACK。未知类型、来源缺失/错配、缺前驱或提交未证实时不跳过 offset。

轮询每次最多取配置上限；空批按固定间隔等待，失败采用有界指数退避，成功有新批则继续读取下一连续窗口。来源暂不可用记 `retryable`，合同错配或缺前驱记 `blocked` 并保留原 offset，需修复来源后原位重试。SIGINT/SIGTERM 取消请求 Context，停止轮询，并依次关闭指标 HTTP、Graph/Runner、HTTP 空闲连接、PG 与 Telemetry。`/metrics` 只监听回环；应用阶段用结构化 JSON 日志，原生 Agent/Graph span 经进程 OTLP exporter 导出。令牌与 DSN 不写入日志。该进程是 opt-in 的可运行交付，尚未在生产调度中启用。

## 同次真实进程验收

```bash
SEA_EXPECT_FAVORITE_REVISION=article-shared-authority:r1 \
SEA_DC_PLATFORM_ROOT=/path/to/isolated/DataCenter \
SEA_RTW_FAVORITE_ROOT=/path/to/RTW-favorite-revision-checkout \
cmd/worker/favorite_acceptance.sh
```

脚本自建 PG16、真实 DC `cmd/platform -migrate`，构建 RTW `cmd/fact-dispatch`/私有 `cmd/fact-authority` 和 **`go build -race ./cmd/worker`**。RTW `TestFavoriteDeliverySharedAuthorityFixture` 从收藏业务事务产生超过 2^53 的同一收藏建立/撤回，冻结 `target_revision=article-shared-authority:r1`，通过其真实派发进同一 DC producer，保持源库/authority 存活至 BTW 测试释放；凭证仅由权限 0600 的临时 ready 文件传给子进程环境，不回显。

BTW 测试先以未迁移 schema 启动 race worker，进程拒绝并未建表。随后预先迁移独立用户事实 schema：第一轮真实 worker 经测试 DC 代理令 ACK 返回 503，真实 Graph/PG 已接纳两版、Outbox 两条而 DC 游标不前进；SIGTERM 后进程正常退出。第二轮在同 schema、同 consumer 上重启实际 worker 并直接访问 DC，重读两条均为 replay，Outbox 仍两条，最终 ACK 到撤回 offset。PG 两行均 `accepted`、版本 1/2，撤回 `supersedes_event_id` 为建立事件；两行 `ValueRef` 都精确等于 `article/article-shared-authority/revision/article-shared-authority:r1`。测试 OTLP HTTP 收到 `trpc.agent.go`/`usermodel_fact` 原生 span，回环 `/metrics` 同时可见应用和原生 Agent 指标；两次进程结构日志含启动、退避与优雅停止，且不含服务令牌。

RTW `2c5c22e`、DC `e8c8cd6` 与本 BTW 代码/测试 HEAD `84bf85b` 在隔离环境的脚本退出 0，两个跨仓用例均 `-race -count=1` PASS；脚本另跑 BTW `go vet ./cmd/worker`、`go mod verify` 与 `git diff --check`。另独立运行 BTW `go test -race -count=1 ./cmd/worker`、`go test -count=1 ./...`、`go vet ./...` 均退出 0。本轮最终证据目录：`/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T//sea-fact-process.bhYEZm`。RTW 另一个隔离链已验文章公开 r1→发布 r2→旧收藏撤回仍保留 r1；它与本 BTW 常驻进程链**不是同次运行**，这里仅验 RTW 共享源冻结 r1 到 BTW Graph/PG/ACK 的后半链。

本交付尚未覆盖 WS07 数仓入仓、用户特征/ServingBundle 生效、真实 Collector 查询后端、更多来源类型或生产进程部署；WS08-A/H09 总项继续 `PARTIAL`。
