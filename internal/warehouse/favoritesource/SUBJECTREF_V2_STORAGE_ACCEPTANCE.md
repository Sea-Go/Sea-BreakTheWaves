# 收藏数仓 SubjectRef v2 PostgreSQL 侧映射候选

状态：**阶段三两张 PG 表的本机局部候选；生产、ClickHouse 和 Dataset 尚未迁移**。从 BTW 开发集成 `ad85f5a58725b3da00c49193c092bcb9a8a00832` 出发，本切片只在 `internal/warehouse/favoritesource` 扩展侧映射。`Initialize()` 和 `InitializeCoverage()` 继续加载旧 `schema.sql`、`coverage_schema.sql`，从不自动应用新 DDL。旧 ODS `producer/source_offset` 主键、`producer/event_id` 唯一键、冻结 `event_spec/source_event_hash/technical_receipt`、主体收据 `receipt_sha256` 主键与旧 manifest+三元组唯一键均保留。

## 数据与运行契约

正式 `ApplySubjectRefV2StorageCandidate` 只接受测试 owner 在新建 PG16 库预写、时效≤1小时的随机 nonce 标记。它从**同一单连接 RR 事务**先锁 `consumer_cursor/ods_event/coverage_batch_evidence/coverage_publication/coverage_subject_receipt` 五张旧表，再复用现有逐行 `SubjectRefV2Preflight` 核旧 ODS EventSpec/JCS/source hash/技术收据、连续 cursor、favorite/folder 目标和同 manifest 主体撞键，并从本机 S3 重新验证原内容寻址 manifest/receipt/稀疏索引对象 SHA。仅 `Clear` 才在该事务内设置快照标记、执行没有 `BEGIN/COMMIT` 的 `migrate_subjectref_v2_storage.sql` **事务 body**，最后由 Go 持锁提交。先跑一次普通只读报告却在锁前增改旧行，不能拿旧报告给迁移背书；受锁报告和回填对应相同旧 PG 行。SQL 单独运行无标记在首 DDL 前拒；测试脚本的合成事务标记只准做结构 smoke，不是正式应用资格。

body 增加仅供精确 FK 使用的旧行复合唯一锚及两个 sidecar：`ods_event_subject_ref_v2` 按原 producer+offset 保留一份主体映射，`coverage_subject_receipt_subject_ref_v2` 按原 receipt SHA 保留一份映射，并对 `(manifest_sha256,issuer,subject_uid)` 唯一。新键 `issuer=rtw.identity`，`subject_uid` 为正 BIGINT，读面规范投影成十进制 `subject_id`；旧 `tenant_id=platform` 只做 FK 兼容槽。CHECK 同时核旧 authority、槽和 `old.subject_id=subject_uid::text`，不会把 `01`、0、超 int64 或别的发行者归并。ODS 不对 `favorite_id` 建机械唯一键，因为同一收藏的 assert/retract 合法版本链要并存；旧槽跨主体的 favorite、folder+target、同 manifest receipt 撞键由受锁完整预检阻断。

两个 FK 先 `NOT VALID` 再 `VALIDATE`，全旧表行回填后双向 `EXCEPT` 要求没有缺行、额外行或错映射。重复应用须核完整 PG16 列序/NOT NULL/无默认值、generated、identity、关系形态、旧 PK/UQ 和新 CHECK/PK/UQ/FK 的实际所属表、定义、validity；不能由外表同名伪约束蒙混。预检异常、非规范旧行、投影撞键或弱同名对象会让 DDL 和回填整笔回滚，旧事件、收据和 manifest 不执行原地 UPDATE。这个受锁整表作业只适合**新建隔离库离线候选**，不构成生产在线 expand 作业。

运行期选项是给 `Consumer`、`CoverageConsumer` 和 `CoveragePublisher` 显式注入同一个 `SubjectRefV2StorageCandidate`；字段为 `nil` 时原 v1 写/读/ExportODS/S3 工件路径不变。候选构造器另核当前测试库 nonce/时效、重跑原 ODS/coverage 完整只读预检 `Clear`，并核已有 sidecar 的两表列形、14 个真实原主键/唯一锚/新约束与已验证 FK，注入 BTW `telemetry.Bundle` 后才 ready；测试脚本合成 SQL 标记不满足此门槛。ODS 在原仓库 cursor 事务中写旧行及 sidecar；丢 DC ACK 的原 offset 重放只核旧事件 hash/ID/主体并补缺 sidecar，不新增另一 offset 或双发事件。`CoverageConsumer` 沿用此事务，批窗收据在 PG 提交后、DC ACK 前固定。

Subject receipt 的来源索引、manifest、稀疏索引和 receipt 对象仍先按旧内容寻址合同固定上传；随后**一笔 PG 事务**核旧 receipt SHA、manifest、主体与计数，写旧 receipt+sidecar并一起提交。PG sidecar 失败会回滚旧 receipt；已上传但尚未挂载的固定 S3 对象可以按同 URL/字节重试，不能说外部 PUT 与 PG 原子提交。内部 `ReadODSEventV2/ReadSubjectReceiptV2` 只从严格 FK JOIN 返回原旧行和 `issuer+规范UID`，缺映射明确报 pending；它们不是新的对外 EventSpec、coverage 或 Dataset wire，也不改变旧 manifest/Parquet `sample_id`。

候选 PG 写面在注入的 Bundle 中以结构 JSON Stage 记录 offset/manifest 和固定错误分类，未直接用 `fmt` 打印阶段输出，不把 UID 放入日志字段或 Prometheus label。普通 RTW v1 EventSpec/签名和一份 DC offset 继续存在；新 RTW SubjectRef v2 事件签发属于后续生产者迁移。

## 验收证据

在新建的本机 PG16 上，`bash internal/warehouse/favoritesource/test_subjectref_v2_storage.sh` 退出 0。此脚本用**合成事务标记只做结构 smoke**，并有无标记直接执行在首 DDL 前拒的负例；其旧 placeholder EventSpec/技术收据或稀疏高位都不满足完整源预检，不能声称获得正式迁移准入。旧源行字节摘要文件前后 `cmp=0`，`ods_event` 两笔同 favorite assert/retract、subject receipt 一笔首次/重放镜像；两个精确 FK validated，错旧槽/超 int64/同 manifest 降维撞键、弱同名表/CHECK/FK/父锚及错 event_id/manifest 均阻断并回滚。高位 `source_offset=9007199254740993` 和同高位 UID 的 BIGINT/规范十进制原值、旧两种唯一键均实库对照；**该稀疏高位行不可发布 coverage**。事务 body SHA-256=`8cd159c7dbb78aa3bd7cc8fa6d1ce4c70dc67d110e94d0d3e1cc3e9c4dfe976b`；本轮结构首迁移日志 SHA=`b5e07419b4fee6109a012a8331e0fd9e8e1d2406387721a19ccc5c2454d89295`、重放 SHA=`eacb5d065b8f86602a2aca18e539484a8b3f880900f8afca4951d0d69985d91e`，PG 已停止。

另一新建 PG16 的 `FAVORITE_V2_TEST_FILTER='.' bash internal/warehouse/favoritesource/test_subjectref_v2_runtime.sh` 退出 0；受影响包 `-race -count=1` **7 PASS/3 环境 SKIP/0 FAIL**，vet/diff check 0，PG stop 后 status 退出 3。真本机 HTTP 对象/RTW authority 夹具先用**旧 schema**写 ODS3、3 份主体 receipt和1份内容寻址 manifest，完整只读 SubjectRef v2 preflight `findings=0`、verified root1。夹具预置新库64hex随机 marker后，**先跑普通RR报告Clear，再分别造同规范UID但坏 EventSpec/JCS、source hash、技术收据、cursor 或 S3 manifest 的旧行/旧对象**；正式受锁Apply每一轮皆报阻断且新sidecar/锚无DDL，恢复原旧字节后同一受锁快照首次/重放成功。随后默认关闭新 offset4/receipt 不投影；开启后同原键核对重放各补一份，新 offset5/receipt 原业务事务一次提交。额外 sidecar CHECK 分别在 ODS offset6、主体 receipt 的 PG 插入中报错，旧行/仓库 cursor 回滚、DC ACK 不推进，解除后同操作只存一行/一投影。旧三笔 ODS JSONL 前缀、原 manifest/receipt 对象逐字不变；启动弱 UID 默认值或删原 producer+offset PK 均拒。父 race 日志 SHA=`8e49abd6ae6e1044c6d6df4819a118d3e6d2842134fd6e137b628ba1d73a8aab`，PG stop SHA=`ca19178a35ab4153b75b494963b66ce8243e1b94c173107c87b44d09db23652d`。

第一次新 runtime test 曾以 PostgreSQL `inet::text` 错读 loopback 为纯 IP，得到 `127.0.0.1/32` 后在首业务操作前退出 1；该 PG 已停止。测试改用 `host(inet_server_addr())/host(inet_client_addr())` 后，以上完整 runtime race 复轮通过。三个环境 SKIP 分别是真 RTW/DC 接口、独立两主体 coverage 旧专用 DSN、真 RTW coverage 链；不挪旧头的外部 E2E PASS 给本候选。

## 后续交接

RTW/BTW DB owner 须在**真实生产旧行**另做只读全量预检，签名异常 favorite/folder目标/manifest主体冲突，并设计不阻塞普通 writer 的在线 expand 水位、默认关闭期间补投和新旧读面对账；当前预检上限 10000，不可把本机 0 finding 当生产全量结果。ClickHouse `ods_favorite_event`、dbt `dwd_favorite_transition`、Coverage 工件版本、Dataset revision/Parquet `sample_id`、Serving 指针、RTW 新 v2 签发、真 RTW→DC→BTW 产品流与生产部署仍未验。回退只停**后续**候选映射写并继续旧键读取，旧不可变工件不重算、不覆盖。
