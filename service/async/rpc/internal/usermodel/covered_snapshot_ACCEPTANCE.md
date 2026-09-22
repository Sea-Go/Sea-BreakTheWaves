# WS08-D v2 历史快照与固定规则 Bundle 候选

本切片只为已接纳的 `usermodel_covered_baselines_v2` 增加历史版本与未激活表示。部署显式执行 `001_facts.sql` → `002_coverage_verification.sql` → `003_features.sql` → `004_serving.sql` → `005_covered_baseline.sql` → **`006_covered_snapshot.sql`**；构造 Store 不运行迁移。006 只新建 `usermodel_covered_snapshots_v2` 与 `usermodel_covered_bundle_candidates_v2`，两表保存原始 `bytea`、SHA-256 和查询镜像 `jsonb`，触发器阻止 UPDATE/DELETE。没有 v2 活动头、Pair 批准或服务指针，旧 v1 FeatureSnapshot、ServingBundle、指针与授权规则保持原样。

`FreezeCoveredSnapshot(ctx,SubjectRef,revision,FeatureSpec)` 在一个 PG repeatable-read 事务中读取 005 冻结的 `candidate_raw`、artifact hash、覆盖 receipt 和版本列，复核完整 FeatureSpec/hash，按 W 重放**历史**贡献再形成 `CoveredHistoricalSnapshot`。它保存候选产生时的 Tail/CurrentComplete 和值，不从最新 Active 反推 W1：W1 建立值 1 在其后撤回已接纳时仍可按 ID 回读。返回 `historical_default_off`，重复同字节得到相同 ID 与 Replay，不提供 Ready 方法。

`BuildFixedCoveredBundleCandidate(ctx,SubjectRef,snapshotID,PairRef)` 使用另一个可写 PG repeatable-read 事务锁主体状态行，核该快照对应**最新已存 v2 revision**、005 原始 `bytea` 与覆盖 receipt、当前主体 stateVersion 和当前 `CoveredStateAt`。已知尾部非空、状态过期、贡献或值变化均 Pending；错 Pair、Spec hash/空间/encoder/维度/metric 冲突。初片 `CoveredFixedCandidatePair` 仅支持同版 `favorite_active_count` 的 1 维 dot 固定规则，向量是当前有效收藏计数，状态始终 `candidate_default_off`。缺正式 FeatureSpec/PairRelease 和 DC 用户塔时不能把该候选称作线上用户表示；旧 `ReadyServingBundle` 不读取 v2 表。空主体也须先有 WS08 验过的完整全局根及零字节稀疏收据，才能生成计数 0 的候选。

隔离 PG16 `-race` 验收同时执行现有 v1 关联测试：W1 后有撤回 Tail 时历史快照值 1、Bundle Pending；W3 全覆盖后历史快照值 0、未激活 Bundle 向量 0；空主体、同 hash 重放、旧版回读、错 Pair 七字段、后续主体事实、触发器不可变和 v1 指针不变均逐项验证。另以既有同次 WS07→WS08→H10 父门禁实际归档的 H10 候选**原始字节**验 005→006：并非重造一个内容相似的 JSON。该旧根 W1 带已接纳撤回尾部，仍只能历史回读，不能建当前 Bundle。

本切片的状态是 **LOCAL_VERIFIED / default-off candidate**。它未进行正式 DC 用户塔推理、recommend PairRef 授权、item 同空间索引、Accept/Activate 到线上 Serving 指针或真实 RTW 多主体生产流量联验；`CurrentComplete` 仅表示 PG 中无已知尾部，不是 DC 全局鲜度证明。

最终 `SEA_COVERAGE_COMBINED_ARTIFACT_DIR=<既有同次父门禁>/cross-domain USERMODEL_KEEP_EVIDENCE=1 GOFLAGS=-p=2 GOMAXPROCS=2 bash internal/usermodel/test-postgres.sh` 于 2026-09-15 退出 0；隔离 PG16/race、全包关联测试与迁移证据目录 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T/sea-usermodel-acceptance.3FkWrV`。原始 H10 W1 候选 bytea SHA-256 为 `2e50f8bc2b41fc6212b2fbaf99a95e1a12ca10fae82d281e203dc71d380949e1`，派生历史快照 ID 为 `87cea523e2fc4b02072a52684770caee0e60e0e9b1b300d64c19be2af9b77ad0`；其已接纳撤回尾部始终阻断当前 Bundle。此结果只证 PG 组件及同源工件消费，完整真 RTW/DC/S3→006 的同次父链留待集成验收。
