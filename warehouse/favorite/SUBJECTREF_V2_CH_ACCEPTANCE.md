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
