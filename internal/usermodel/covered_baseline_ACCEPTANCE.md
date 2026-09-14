# WS08-C / H10 v2 历史基线接纳

日期：2026-09-15。独立 BTW 分支 `feat/usermodel-covered-baseline-20260915`，基线集成 `dd36289`。本切片只写 `internal/usermodel/covered_baseline_*`、经授权极窄抽取 `coverage_state.go` 的私有事务 helper、专属 `005_covered_baseline.sql` 和本域测试；不改 Docs、WS07 Publisher、H10 `covered_*`、原脏树或 v1 FeatureBaseline/Serving 表。

## 入口与事务

单入口 `Store.AcceptCoveredBaseline(ctx, CoveredBaselineSubmission{ArtifactURL,ArtifactSHA256,CandidateJSON,FeatureSpec})` 接 H10 `sea.user-feature-baseline.covered.v2` **原始 JSON 字节**。严格解码、限制 32 MiB、逐字节 SHA256 与内容寻址 `/<generation>/covered-baseline/<hash>.json` URL 对齐；URL 必须是无凭据、query、fragment 的 HTTP(S)。H10 `CoveredCandidate` 镜像结构位于本模块，不反向 import `featurebaseline`。只接受 `candidate_default_off`、当前时刻之前的 `AsOf=AvailableAt`，并独立核已规范化 `fact/count` FeatureSpec/hash、W 时全部历史贡献、值、SubjectCoverageRef 与 W 后包含撤回/pending 的 tail。

`coverage_state.go` 只把原 `CoveredStateAt` 的查询和全历史折叠抽成 `coveredStateAtTx(ctx, pgx.Tx,...)`；公开方法仍自行打开同样的 repeatable-read 只读事务并委托。v2 接纳打开一笔 repeatable-read 写事务，先查 `(SubjectRef,revision)` 的不可变旧收据；同修订同 URL/hash/原字节返回 `Replay`，异内容返回 `ErrCoveredBaselineConflict`。新修订在同事务锁既有 `usermodel_subject_state`，复用 `coveredStateAtTx` 重新核已验稀疏引用、PG event_body 规范化 hash、accepted 版本/Outbox、W 时活跃贡献及 W 后完整 tail，再核当前主体版本、FeatureSpec 和 H10 Values；版本变动/serialization 返回 `ErrCoveredBaselinePending`。按主体 revision 连续插入单独的不可变 `usermodel_covered_baselines_v2`，`candidate_raw bytea` 保留 H10 原字节，同时存 `candidate_body jsonb` 供查询。迁移触发器拒绝 UPDATE/DELETE。

收据状态恒为 `accepted_historical_default_off`：即使原候选 `CurrentComplete=true`，也只表示**构建快照中**的已知 PG tail，不声称现在仍完整。新事实发生后，同修订同字节只重放历史收据；新候选必须重新建、重新核主体版本。空主体无既有 `subject_state` 可锁，允许保存历史默认关闭候选，**不**提供当前Serving激活授权。入口不建/移 v1 feature head、v2 active head、FeatureSnapshot 或 ServingBundle；批准 FeatureSpec、v2 Snapshot/Serving 鲜度 CAS 和生产装配是后续门禁。

迁移严格显式：`001_facts.sql` → `002_coverage_verification.sql` → 既有 `003_features.sql` → 既有 `004_serving.sql` → 新 `005_covered_baseline.sql`（嵌入变量 `usermodelmigration.CoveredBaselineSQL`）。命名为 005 是因为 003/004 已被占用；构造 Store 不自动运行任一 DDL。

## 验收证据与级别

`USERMODEL_KEEP_EVIDENCE=1 bash internal/usermodel/test-postgres.sh` 在隔离 PG16/race 下退出 0。`TestAcceptActualH10CoveredCandidateHistoricalAndReplay` 用**当前 H10 `CoveredBuilder`/`UsermodelCoveredReader`** 在同一 PG 生成候选、写/读固定对象，再把其原字节/SHA/URL直接交 v2 接纳：W1 保留 assert 的 count=1，offset2 pending、offset3 已接纳 retract 都留 tail；PG 收据 `bytea` 精确等于 H10 对象，v1 baseline/head 与 Serving 表均为0。已验全局W1中无事件的第三主体也产生 count=0 的H10候选与历史默认关闭收据（不能据此激活Serving）。同修订同字节重放、同修订异 hash 冲突；候选构建后新增 PG 事实使下一修订 Pending，旧修订仍只作历史重放；改 Values/漏 Tail、未来 `available_at` 和改收据均拒绝。

另以 `SEA_COVERAGE_COMBINED_ARTIFACT_DIR=<combined/cross-domain> USERMODEL_KEEP_EVIDENCE=1 bash internal/usermodel/test-postgres.sh` 读 WS07→H10 已完成的 combined 父验收**原始** G1/G3/U1W1 索引和 `candidate_w1_after_tail_sha256.json`。本地 HTTP 对象 fixture 用 Dial Adapter 把归档时原 `127.0.0.1` URL 的连接导向临时监听，但请求 URL/Host/path、ref 与 SHA 均保持原样，逐个 GET 并核 hash；新隔离 PG 重放 G3 来源、Verifier 核 G1 与 U1W1，`CoveredStateAt` 得 W1 assert+已接纳撤回尾部，随后 v2 接纳 combined H10 **原始 candidate bytea**、同哈希重放且 Serving pointers 为0。旧组合的 live S3/PG/RTW/DC 此时已关闭，故这是**原工件跨运行交接**，不是这些进程同次在线服务。来源未来可用时间的旧归档只做 Verifier 层复核，H10/v2 Accept 不提前消费；该分层复核证据为 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T//sea-usermodel-acceptance.vZwgNw`。

最终隔离 PG16/race 证据 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T//sea-usermodel-acceptance.n3in9H`；公开 `CoveredStateAt` 抽取后的 H10 PG Adapter 回归 `internal/warehouse/featurebaseline/covered_test_postgres.sh` 退出 0，证据 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T//sea-h10-covered.SfIcM9`。另运行 BTW `go test -mod=readonly -count=1 ./...`、`go vet ./...`、`go mod verify` 和 `git diff --check` 均退出 0。此切片没有批准 FeatureSpec、发布 v2 Snapshot/Serving，也没有生产请求流量；WS08-C/H10 总项仍 `PARTIAL`。
