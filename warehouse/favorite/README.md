# RTW 收藏事实的数仓独立消费切片

`rtw.community.favorite` 的事实所有者是 RTW Favorite。DC 只提供有序的技术投递、不可变输入哈希与收据。此切片使用独立的 `btw-warehouse-favorite` consumer，不借用 BTW FactWorker 的 ACK，也不将收藏写入推荐曝光、成熟正负标签或用户负反馈。

## 写入边界与交接

- RTW 在业务事务中冻结 assert/retract 两版 EventSpec、原始 `target_revision` 与 SubjectRef；RTW 权威接口按 `(producer,event_id)` 回读 EventSpec、前驱与 DC 投递收据。
- 数仓先读 DC 连续 batch 与逐事件收据，使用现有 RTW 权威核验适配器比对 JCS EventSpec、哈希、收据、主体及前驱。核验失败或版本缺口时不写 PG、不 ACK。
- PG `warehouse_favorite.ods_event` 按 `(producer,source_offset)` 保存不可变事件、主体、目标修订、`event_time`、业务 `available_at` 与独立 `dc_received_at`。`consumer_cursor` 与整批 ODS 同事务提交；DC ACK 只在 PG 提交后发送。ACK 不确定时重读并按事件 ID/哈希复验，避免重复写入。retract 必须找到同一主体、目标和修订的 assert。
- 从 PG 按 offset 导出固定 ODS JSONL，S3 以内容哈希归档；独立 ClickHouse/dbt 项目形成逐版 `dwd_favorite_transition`。assert 的状态增量为 `+1`，retract 为 `-1`，最终活跃状态为 0；这只是收藏状态变化，不能当作点击、曝光或推荐负例。

目前核验适配器与 BTW Graph Worker 同处 `internal/app/favorite_authority_binder.go`，但数仓不复用 Graph worker、状态或 ACK。正式服务装配前可将仅负责 RTW 冻结来源校验的适配器抽到共享来源契约包；不能把 BTW 用户画像模型变成数仓 ODS 的权威源。

## 隔离验收

需要隔离的 DC、RTW 和锁定的 ClickHouse/SeaweedFS/dbt 运行时：

```sh
SEA_DC_PLATFORM_ROOT=/path/to/isolated/datacenter \
SEA_RTW_FAVORITE_ROOT=/path/to/isolated/rtw \
WAREHOUSE_FAVORITE_RUNTIME=/path/to/locked/warehouse-runtime \
warehouse/favorite/acceptance.sh
```

脚本创建自己的 PG16、DC platform、RTW Favorite、CH、S3 与证据目录。RTW 测试源是**真实业务写入与真实投递的隔离 fixture**，因此验收等级 L2；不代表生产 H09 流量、线上效果、模型训练输入已启用。`ch-evidence/manifest.json` 记录输入哈希、两条原始 EventSpec 哈希、S3 固定地址、DWD 哈希及手算状态。脚本最后打印证据目录；故障时先看 `warehouse-test.log` 或 `ch-acceptance.log`。

下游交接：WS07 离线作业可按 `producer + source_offset` 消费本 ODS/DWD；正式来源覆盖与水位必须另接 DC consumer 运行收据。WS07-E 效果 ADS 的曝光分母及成熟标签仍只来自独立的推荐展示/归因来源，本收藏切片不得填补缺失的归因来源。

## SubjectRef v2 阶段一只读预检

`SubjectRefV2Preflight.Run` 在 PG `REPEATABLE READ READ ONLY` 快照中读取现有 `warehouse_favorite.ods_event`、`coverage_batch_evidence`、`coverage_publication`、`coverage_subject_receipt`，不执行 DDL/DML、DC ACK、CH UPDATE/分区写或对象 PUT。旧 ODS 键为 `(producer,source_offset)` 和 `(producer,event_id)`；RTW 收藏业务键为 `favorite_id` 与 `uk_folder_target=(folder_id,target_id,target_type)`。旧主体 `(authority_id,tenant_id,subject_id)` 只有精确为 `rtw.identity/platform/规范正 int64 UID` 才可直接投影到 issuer+UID。即使另一旧 slot 的数字 UID 可解析，预检也只用它反证投影撞键，并以 `exceptional_legacy_slot_requires_review` 阻断，保留旧 slot 供来源所有者裁决。

已发布覆盖键为 `coverage_publication.manifest_sha256`（`generation` 唯一）及 `coverage_subject_receipt.receipt_sha256`；主体收据另有 `(manifest_sha256,authority_id,tenant_id,subject_id)` 唯一键和 manifest FK。预检按 `(issuer,UID,favorite_id)`、`(issuer,UID,folder_id,target_type,target_id)`、`(manifest_sha256,issuer,UID)` 查投影撞键。它复核 ODS 原 EventSpec 的 JCS SHA、DC 收据/offset、业务字段、assert/retract 前驱及供 CH 导出的事件类型、aggregate 和三个时点，再对隔离 localhost 对象只做 GET，检查旧 manifest、完整 event index、PG batch evidence、主体 receipt、sparse index 的**原字节**、内容哈希和固定 URL/PG 引用。旧发布根的 `BindingPolicyID=rtw.favorite.authority.v1`、旧 receipt SHA 与固定对象字节应继续保留；通过预检也不代表能原地改旧覆盖。无对象读取器时明确给出 `coverage_objects_unverified`，不宣称引用已验。

隔离 PG16 与测试内 localhost 对象 fixture：

```sh
warehouse/favorite/subjectref_preflight_acceptance.sh
```

脚本只输出通过状态、快照 SHA 与已核对根数；临时 PG 数据退出即清理。`TestSubjectRefV2*` 是 L1 确定性反例，隔离 PG/对象及原字节篡改是 L2；生产连接、生产覆盖字节、线上 ClickHouse/DWD/DWS 和流量均未验（L3 未做）。报告的 `snapshot_sha256` 是本次只读快照指纹，不是新 EventSpec、已发布根或迁移清单的替代 SHA。

### ClickHouse 与下游迁移依赖及负例

当前仓内 `warehouse/favorite` 项目只有 CH `ods_favorite_event` 和 dbt `dwd_favorite_transition`，没有该项目的 DWS 表；运行库是否另有视图、物化视图或 DWS 必须在独立迁移前只读盘点。以下是源码可确认的依赖，不能用本预检代替运行库的 `SHOW CREATE`、`system.tables` 与依赖查询：

| 依赖 | 当前旧主体用途 | 迁移门禁 |
| --- | --- | --- |
| `warehouse/favorite/landing.sql`、`models/sources.yml` | CH `ods_favorite_event` 的三列 `String` 旧主体，按 `(producer,source_offset)` 排序 | 原 S3 ODS JSONL、原 EventSpec/hash/offset 不可覆盖；另行决定新读模型及全链回放 |
| `warehouse/favorite/models/dwd_favorite_transition.sql`、`tests/favorite_transition_integrity.sql` | DWD 逐版保留旧三元组；retract/assert 按完整旧主体、原目标与修订联结 | 新 issuer+UID 联结前先过旧 slot 撞键、事件前驱和来源哈希负例 |
| `warehouse/coverage/acceptance.py`、`warehouse/favorite_feature/acceptance.py` | 按 DWD 固定 JSONL SHA 归档和验收 | 新 DWD 字节必有新 SHA；旧归档及已发布 coverage 不可改址或改字节 |
| `internal/warehouse/featurebaseline/favorite_candidate.go` | `FavoriteDWDRef` 固定 SHA，`verifyFavoriteDWD` 按完整旧主体与连续 offset 核对 | 候选/基线消费者另切，不把全局 offset 或旧 DWD hash 伪称新主体覆盖 |

必须拒绝的反例：非规范 UID（零、前导零、溢出）；两个不同旧 slot 投影后占同一收藏或文件夹目标键；同一 manifest 下两个旧 receipt 投影到同一 issuer+UID；`through_offset` 超出已提交 ODS 或前缀缺口；原 EventSpec/收据/hash/offset 或 ODS 派生列不一致；assert/retract 前驱不属于同一旧 slot/目标/修订；旧 manifest 指向不同 index/batch、PG batch evidence 与对象字节不同、主体 receipt 指向不同 sparse 字节。不同旧 slot 即使被业务确认合法，也保持阻断并留在原 slot；本阶段不自动合并、不创建 v2 版本表，也不触碰 CH 行或分区。
