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
