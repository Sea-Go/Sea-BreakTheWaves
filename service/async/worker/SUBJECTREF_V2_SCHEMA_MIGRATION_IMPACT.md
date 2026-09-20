# 移除 usermodel `tenant_id` 兼容槽的全仓影响清单

## 最终身份模型

RTW 是单一全局用户域。对外 SubjectRef v2 只有 `{issuer,subject_id}`，其中 `issuer=rtw.identity`，`subject_id` 来自 RTW UserCenter UID；没有 tenant 或可选 realm。当前 BTW `usermodel.SubjectRef.TenantID` 与 PostgreSQL/数仓 `tenant_id` 只承载常量 `platform`，是历史兼容槽，不是产品租户。最终迁移应删除该维度，而不是把它改名后继续暴露。

本分支在 2026-09-15 以 `rg -l '\bTenantID\b|tenant_id'` 盘点：72 个 Go 文件（其中 37 个测试）、32 个 SQL 文件、6 个 Markdown 文件命中。下面是交接时的完整文件级范围；生成物和不可变历史事件不能直接批量替换。

## Go 编译与运行边界（72）

### 核心模型、存储和投影

`internal/usermodel/{facts,graph,projection,receipt,features,features_store,ontology,ontology_store,serving,coverage_state,coverage_subject,coverage_verifier,covered_baseline_accept,covered_serving_candidate,covered_snapshot_store}.go`。这些文件的主键、查询参数、receipt、投影和覆盖证明都以三段 SubjectRef 为接口，必须在同一版本迁移，不能只改结构体字段。

### 应用、协议、搜索与推荐

`internal/app/{community_authority_binder,favorite_authority_binder,fact_worker,index_worker,prepare_worker,recommend_rtw_real,search_rtw_history}.go`、`internal/clients/ridethewind/{client,wire.gen}.go`、`internal/runtime/runtime.go`、`internal/evaluation/evaluation.go`、`internal/recommend/{neighbors,ranking}.go`、`internal/search/model_invocation.go`、`internal/sourcecoverage/contract.go`、`internal/transport/http/search/tools_scope.go`，以及 `cmd/search-matrix-acceptance/main.go`、`cmd/search-matrix-delivery-acceptance/main.go`、`cmd/search-summary-matrix-witness/main.go`。`wire.gen.go` 属于生成物，须先改权威契约再重生成。

### 数仓消费者与基线

`internal/warehouse/favoritesource/{consumer,coverage_publisher}.go`、`internal/warehouse/featurebaseline/{covered_build,favorite_candidate}.go`。这些路径把 SubjectRef 写入 ODS、覆盖 manifest、特征候选和 serving receipt，必须与 SQL/对象 manifest 版本一起升级。

### 测试（37）

`cmd/api/idempotent_model_test.go`、`cmd/worker/{community_process_integration,favorite_process_integration}_test.go`；`internal/app/{community_authority_binder,fact_worker,fact_worker_favorite,favorite_authority_binder,favorite_authority_real,recommend_rtw_real,search_rtw_history,search_rtw_real_integration}_test.go`；`internal/evaluation/evaluation_test.go`；`internal/recommend/{neighbors,pair_store}_test.go`；`internal/search/{delivery,graph}_test.go`；`internal/transport/http/search/{handler,otlp_trace_acceptance,signed_scope,tools_handler,tools_matrix_witness}_test.go`；`internal/usermodel/{covered_baseline_accept,covered_baseline_combined,covered_snapshot_store,facts,features,graph,ontology,receipt,serving}_test.go`；`internal/warehouse/favoritesource/{coverage_fixture,coverage_publisher}_test.go`；`internal/warehouse/featurebaseline/{covered_build,covered_pg,favorite_candidate,runner,serving_consumer}_test.go`。迁移验收必须保留旧收藏与新社区同 UID 不分裂、重放 hash 不变和跨表 FK 完整性。

## SQL 物理边界（32）

### usermodel 事务库与内部数仓（13）

`migrations/usermodel/001_facts.sql`、`002_coverage_verification.sql`、`002_ontology.sql`、`003_features.sql`、`004_serving.sql`、`005_covered_baseline.sql`、`006_covered_snapshot.sql`；`internal/usermodel/testdata/features_baseline.sql`；`internal/warehouse/favoritesource/{schema,coverage_schema}.sql`；`warehouse/environment/landing.sql`、`warehouse/favorite/landing.sql`、`warehouse/favorite/models/dwd_favorite_transition.sql`。

### 通用仓库模型、宏和质量门禁（19）

`warehouse/macros/runtime/subject_equals.sql`；`warehouse/models/dwd/{dwd_behaviors,dwd_candidates,dwd_events,dwd_feature_snapshots,dwd_impressions,dwd_requests}.sql`；`warehouse/models/dws/{dws_impression_features,dws_labels}.sql`；`warehouse/models/marts/ads/{ads_positive_evidence,ads_recommendation_exposures}.sql`；`warehouse/models/marts/datasets/ds_recommendation_interaction.sql`；`warehouse/tests/{ads_exposure_grain,ads_mature_source,no_event_conflicts,no_join_multiplication,required_payloads,unique_fact_grains}.sql`；`warehouse/favorite/tests/favorite_transition_integrity.sql`。

这些表和模型的唯一键、主键、外键、窗口分区、join 宏、manifest 哈希与对象列顺序都会变化。旧 Parquet/JSONL 是不可变证据，需保留旧 schema version 并由新版 reader 显式读取，不能原地改列名后复用旧 hash。

## 文档与公开合同（6）

`contracts/README.md`、`docs/runtime.md`、`internal/evaluation/ACCEPTANCE.md`、`internal/usermodel/README.md`、`internal/usermodel/ontology_acceptance.md`、`warehouse/README.md`。还需同步 RTW 收藏 authority 的旧 `{authority_id,tenant_id,subject_id}` wire；已冻结的历史 favorite EventSpec 只能由兼容 reader 解释，不能重写并冒充原 hash。

## 推荐迁移顺序与验收

1. 先冻结 SubjectRef v2 契约与新表 schema version，审计所有在线行 `tenant_id='platform'`，并按 `(authority_id,subject_id)` 检查去掉兼容槽后的冲突；任一其他值或冲突都阻断迁移。
2. 建立以 `(authority_id,subject_id)` 为键的 v2 shadow 表及对应 Outbox/coverage/ontology/features/serving 表，保持 v1 表只读兼容。按原 normalized hash、accepted version、source position 和 supersedes 链复制，逐主体核对行数、active projection、Outbox 与水位。
3. 更新 Go `SubjectRef`、Runner request、RTW SDK、搜索作用域、推荐与数仓消费者；对旧收藏 authority 和旧不可变对象使用显式 v1 adapter。双写阶段比较 v1/v2 receipts，不把常量槽继续放进 v2 API 或新对象。
4. 切换 BTW worker、API 与 warehouse 单写入者到 v2；对每个 producer 重放并证明同 UID 不分裂、EventKey/hash 幂等、ACK 不越过失败 offset。完成 dbt 全量与增量对比后再切 serving pointer。
5. 停止 v1 写入，保留有界回退期；确认无读流量后删除 FK、索引和 `tenant_id` 列。验收至少包含 PG FK/唯一键、DC 全局 offset、对象 manifest、CH/dbt 行数、旧收藏兼容、评论/点赞撤回链、tRPC-Agent-Go trace 与回滚演练。

当前分支只在新社区 wire 消除了 realm/tenant，并将 `platform` 固定在旧存储适配器内部。上述物理迁移尚未实施，状态为 `PLANNED`。
