# 收藏数仓SubjectRef v2 ClickHouse/dbt读面候选验收

本切片只增加历史v1事实的**新修订读面**：CH新`ods_favorite_event_subjectref_v2_r1`、dbt新`dwd_favorite_transition_subjectref_v2_r1`与`subject_equals_v2`，旧CH `ods_favorite_event`、旧dbt `dwd_favorite_transition`、原EventSpec/source hash/offset、旧ODS/DWD S3对象及活动指针均不改。新row合同为`contracts/ods-subjectref-v2-r1.schema.json`，显式开关`favorite_subjectref_v2_read`默认关闭。当前PG sidecar的官方Apply只允许有随机门禁的本机PG16，生产旧行预检及新映射导出尚未签收；本机CH验收用**清楚标注的映射fixture**，不能借它宣称完整PG→CH Stage3链。

## 输入与本机真实运行

隔离原RTW收藏assert/retract已从真实RTW业务写→DC投递→BTW PG ODS冻结成2行，来源留档`ch-evidence/manifest.json`记录旧ODS SHA `b63c3bc3a2574da7f49787b17cf08494b3c7a76c84b1d096914d39285a267647`及旧DWD SHA `8a2d1b7ede6a8b1978da625b7417f58115642f26696a65868fc52408a592aed0`。在本分支新建独立CH/dbt/S3 runtime：先用旧源与默认关闭v2运行dbt（2/2），再把该冻结源与显式sidecar映射生成新ODS后运行v2候选dbt（5/5），旧DWD**逐字节**仍为同SHA，原CH旧表`SHOW CREATE`前后逐字节相同；新DWD SHA `8f9df89a0ffe7ef35ad51e4fde05a8c89b48e77df159e658b5fd28b7ff1bec75`，只有2条状态变化，assert→retract活跃数1→0。

同一CH/dbt/S3隔离进程又在新表/新generation执行明确synthetic的第二UID夹具：两UID复用同`favorite_id/folder_id/target`，各自assert/retract共4个offset；旧dbt 2/2、新候选dbt 5/5，v2 DWD四行`1001,1001,1002,1002`、两主体各自活跃状态回0，不以同目标合并用户。这个第二UID的EventSpec及DC receipt是校验格式的本机合成输入，**不是第二个RTW真实业务操作**。两种新ODS/DWD分别固定到新revision URL并在本机S3读回同SHA，旧URL不覆盖。

负例由投影入口在CH写前拒：错issuer、跨主体UID、错旧锚、坏EventSpec/JCS、坏DC receipt；把新CH表同旧offset/EventID的`source_event_hash`改为另一64位值后，dbt`favorite_subjectref_v2_source_integrity`报`FAIL 1`，v2 DWD被`SKIP`，候选不得发布。第一次负例验收顶层exit1仅因脚本匹配`FAIL favorite...`而未匹配dbt实际`FAIL 1 favorite...`，dbt本身已正确拒绝；调整验收判定后完整复轮顶层exit0，红轮不算通过。

最终`report.json` SHA `0f26ff1e2468d9c7e1ef0cb57e59c871e9948f9254616d8d186f88f9cf6afe0d`；父日志SHA `9db81cafceb5dbadeb81ed2b279f537be82cce2058f5e39f701ade90dafa9948`；负例dbt日志SHA `8b7ab11f88b5f3fd051742f25d3b005d5a144f1469a6548f54079457ad702ca1`。本机CH/S3退出后没有相关监听进程。等级为**L2本机真实CH/dbt/S3候选读面**；生产catalog/生产行、官方PG映射导出、两UID真实RTW流、生产读开关、完整离线Dataset/Serving均`NOT_VERIFIED`。

## 数据owner与后续交接

1. PG仓库owner对真实旧ODS、cursor、batch证据、覆盖manifest/subject receipt及原对象全量只读预检，逐异常裁决后按受锁冻结水位应用sidecar；生产Apply当前未实现，须先做在线限流/补投水位合同。官方PG sidecar导出应以旧`producer+offset+EventID+三元槽`为锚，输出`issuer=rtw.identity/subject_uid=规范正BIGINT UID`，交旧ODS原字节与来源对象SHA；本机`fixture_mapping`不可交付为生产映射。
2. CH owner只读盘点目标库`system.tables`、`SHOW CREATE TABLE`、所有依赖视图/物化视图及当前源offset；新表独立schema和revision目录落地，不UPDATE历史分区、不覆盖旧S3对象。`producer+source_offset`必须处于已提交PG cursor/DC收据窗；候选读前再验旧对象字节、映射全量、同键来源hash/目标/receipt和断档。原DWD byteSHA、逻辑状态、两主体同目标归属与新DWD SHA在新generation复核后才给下一owner。
3. Coverage、`warehouse/favorite_feature`、`internal/warehouse/featurebaseline` owner分别将新DWD SHA作为**新候选引用**签收，不复用旧`FavoriteDWDRef`/旧manifest URL、旧coverage root或活动Bundle；跨项目`warehouse/environment`的`event_history`、公共`subject_equals`及12个DWD/DWS/ADS/dataset模型仍另由数据owner处理。本切片仅有收藏子项目一张旧DWD与一张新DWD，无收藏子项目DWS。
4. 训练owner另发布`recommend-engagement` v2 row合同、新dataset revision、Parquet及新`sample_id`定义，旧dataset manifest/checkpoint/model artifact/sample_id继续原schema；收藏撤回不能直接成为推荐负样本，收藏assert不能成为曝光分母。Serving owner只在新Dataset/Feature/Coverage签收及对账后用本领域CAS显式换活动指针，默认读开关仍旧。

如果新CH构建/前缀或跨项目签收失败，停止后续v2候选读取并继续旧DWD；已写新对象按原hash保留，旧source/对象/指针没有回写或清理。本切片不签全12模型、生产CH Catalog、正式PG sidecar→CH导出、RTW v2 wire、H01身份绑定或线上迁移。

## 第二切片：官方本机PG Apply到CH/dbt同一父链

首切片审查发现P1：当新CH表少一行sidecar时，新DWD原先会从旧行fallback产出`representation_version=1`，看起来像完整v2候选。现新增强制冻结`favorite_subjectref_v2_through_offset=W`，DWD仅读取`1..W`，dbt来源测试核该旧前缀无缺口/重复及**每一旧offset必须有匹配新映射**；新行也必须反向匹配原EventID、hash、receipt、原主体和目标。当前本机同父链在W=3上分别删除新映射的offset3、删除原ODS和映射的**中间offset2**、注入同键不同hash，三个候选dbt各报`FAIL 1 favorite_subjectref_v2_source_integrity`并`SKIP`新DWD；完整正式映射旧dbt 2/2、新dbt 5/5。入口JSONL又拒根/嵌套重复键、NaN以及bool/浮点offset，防止导出腐败被Python JSON解析悄悄归一化。

同次执行`WAREHOUSE_FAVORITE_RUNTIME=<锁定本机运行时> bash warehouse/favorite/acceptance_subjectref_v2_pg_ch.sh`顶层exit0。脚本开全新回环PG16，随机64hex nonce由专用本机测试owner写门禁，原`TestFavoriteSubjectRefV2StorageContinuousWritersAndFrozenArtifacts`用已保存旧ODS/coverage批证据和旧Coverage五对象完成官方受锁只读完整preflight/Apply；先对坏spec/hash/receipt/cursor/S3 manifest逐轮拒，恢复原对象后官方Apply首次/重复成功。Go测试暂停保活同PG和原localhost对象，从PG真实sidecar以`READ ONLY REPEATABLE READ`联旧键导出**三行**0600映射，再由本仓Python用旧Go `ExportODS`原字节及该映射导出新修订，逐一GET原五对象SHA。本链没有调用fixture映射代替PG侧表；其源仍是隔离DC+RTW权威HTTP**夹具**，不代表两UID真实RTW业务写入。旧PG主体receipt、旧S3 manifest/receipt及前缀在测试末原字节校验，Go race单测`PASS`耗时36.88s、vet/mod verify/diff通过；PG stop日志`server stopped`、`pg_ctl status`退出3，旧Coverage对象服务、CH/S3端口60864/60929关闭。

本链旧Go ODS前缀字节SHA `072cc62df121a3b2c61ca2c7750bf2b762ad29d515719c7c3faca70acaa6f0c0`，官方PG映射JSONL SHA `553d57012cc476307ef8ee54c1a4b8663ea28ff8e303f4de4f5737a5cad61c7d`；完整旧快照SHA `1a98d8faa497be9826c8dab5cd37c211221f48d3e3f6d6c818855e74aa748847`，旧manifest SHA `4508750e7da2d9cb8f5588ebcba9696693245b2dce91af598367cca83e840065`、旧主体receipt SHA `85be91a1812483718f1f450b2ad57dfa94f7ad3e7e487a4fbea4336c81e139e9`。同一夹具新generation里旧DWD字节前后SHA `b44fc9e48201b517b7f562718800dfdd7ccb26c63d30d4633d2d5bb00f174122`，新v2 DWD SHA `a0845419c264875f19fd3c38eefb583bcb981a7093e7b20a37aeaa0385cf2b3b`，三条逻辑转换[u1 assert、u2 assert、u1 retract]无翻倍。此前**真实RTW/DC两事件**的旧DWD SHA `8a2d1b7ede6a8b1978da625b7417f58115642f26696a65868fc52408a592aed0`是另一冻结源，继续原字节保留，不能拿两份不同来源SHA互相对比。

本链最终`report.json` SHA `8f8c06684bb507b47ab773744ea0c27dc028bc0ed0b8253b7e6762c2e5361a6d`、父日志SHA `3477b70b4a594395395d97e2b4d0aa9dd2062485b23d3a660fbfaa8f7d7d17f7`、官方PG race日志SHA `185cd21ccc954dcc241cc561d614ed435ecd80e9e7fc81fe90655de754026721`。带0600原ODS/真PG映射、ready、五个dbt构建/拒验日志、CH新旧DWD字节和PG stop/status的可复核目录为`/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T/sea-favorite-v2-pg-ch.jbZWcO`；原Coverage测试对象在已停止的内存localhost服务中，五对象SHA由官方preflight及同次CH脚本GET核对，未作为独立持久对象镜像留盘。签收等级仅**Stage3本机PG完整前缀→新CH/dbt/S3候选读面L2**；真实生产旧历史超过10000/对象catalog、生产在线水位/CH依赖视图、12跨项目模型、新Dataset revision/Parquet sample_id、Coverage新修订、FeatureBaseline/Serving指针、RTW v2 wire与真双用户业务流均欠签，47任务整体继续`PARTIAL`。
