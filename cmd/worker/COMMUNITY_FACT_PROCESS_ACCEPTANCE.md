# 评论与点赞事实进程验收

## 已实现链路

RTW 评论事务写入 create、comment-like、comment-unlike、delete 四条冻结事实；目标点赞事务写入 like、unlike 两条冻结事实。两个独立 dispatcher 按各自 producer 的聚合版本顺序把原体交给 DataCenter，确认 `receipt_id/input_hash/offset/received_at` 完全匹配后才落回 RTW Outbox。两个 authority 进程只返回已获 DC 回执且仍能由 RTW 当前领域状态与完整前驱链证明的事实。

社区 authority wire v2 的主体只有 `issuer=rtw.identity` 与 UserCenter UID `subject_id`，没有 realm 或 tenant。源业务 payload 仍保存既有规范字符串 `rtw.identity/platform/<UID>`；BTW 先核对该字符串，再将常量 `platform` 写入 usermodel 旧数据库中名为 `tenant_id` 的兼容槽。调用方不能提供或改变这个值。旧收藏 Binder 与新社区 Binder 的同主体测试证明 UID `1001` 都落到内部 `{AuthorityID:rtw.identity,TenantID:platform,SubjectID:1001}`，没有形成第二个用户。

BTW 使用正式 `cmd/worker` 的显式 `usermodel.comment-facts.v1` 与 `usermodel.like-facts.v1` 作业。每个 producer 只有一个 consumer cursor；`FactWorker` 在一个批次内允许固定 EventType 集合，并由 RTW 权威 old/new state 把 interaction 映射为有限的 `assert/correct/retract`。每条事实均经真实 tRPC-Agent-Go Graph/Runner、PostgreSQL `usermodel_events`、state version 与同事务 Outbox 后才允许 ACK。测试第一次让 ACK HTTP 返回 503，确认 DC cursor 未前进；停止进程后用同 schema 重启，六条已提交事实全部 replay，随后分别 ACK 到 comment offset 4 和 like offset 2，没有重复 Outbox。

## 同次 L3 验收

2026-09-15 的 `cmd/worker/community_fact_acceptance.sh` 创建独立 UTF-8 PostgreSQL 16 集群，实际构建并运行固定 DataCenter `f59a676a3439f66122e0ec579cd22f030719e058`、RTW 运行代码 `58468c1d4d0fd5708bc0c9726a94ef252cb588eb` 与 BTW 运行代码 `6050ab8ca84ac8225442571a985c071113336041`。最终退出码为 0；报告位于本机临时证据目录 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T/sea-community-facts.8jpKfd/report.json`，SHA-256 为 `269cf7d12c1b01f3181bfb38636c4623f838bedfa8177e74e913876a6002eeb8`。

真实 BTW PG 六行逐条核验如下：

| producer offset | subject_id | action | predicate | value_ref | supersedes |
| --- | --- | --- | --- | --- | --- |
| comment 1 | 1101 | assert | comment | comment/9101 | 无 |
| comment 2 | 1301 | assert | comment_like | comment/9101 | 无 |
| comment 3 | 1301 | retract | comment_like | comment/9101 | comment offset 2 的 EventID |
| comment 4 | 1101 | retract | comment | comment/9101 | comment offset 1 的 EventID |
| like 1 | 1401 | assert | like | article/article-community | 无 |
| like 2 | 1401 | retract | like | article/article-community | like offset 1 的 EventID |

每行同时要求 `authority_id=rtw.identity`、旧兼容槽值为固定 `platform`、`item_id=article-community`、`semantic_kind=product_action`、`status=accepted`、正 accepted version、对应的全局 source offset，且 `impression_id` 为空。规范化 usermodel `event_body` 不复制 RTW 原始载荷字段；测试要求其中没有 `target_revision/revision_status/search_evidence`。这三个源精度字段由同次 RTW 源 PG 六行直接证明：全部 `target_revision=null/revision_status=unknown`，评论全部 `search_evidence=false`。因此本验收没有猜文章修订，没有把评论/点赞冒充曝光或负样本。

RTW dispatcher、authority 的每行真实进程日志均经 JSON 解析，要求 `timestamp/level/service/environment/service_version/instance_id/component/log_source/event/message`，终态另有 `outcome/duration_ms`。go-zero signal 日志也进入同一 Writer。authority 日志携带 BTW HTTP Context 的 trace/span；BTW OTLP fixture 收到 tRPC-Agent-Go 原生 `trpc.agent.go` 与 `usermodel_fact` Span。观测状态为 `LOCAL_VERIFIED`，未证明 RTW OTLP 导出或线上 Collector 接收。

## 尚未覆盖

验收没有连接真实 Kafka、Redis、线上数据库、数仓 ODS/DWD 或生产部署；也没有把全局 producer offset 解释成每主体连续序列。评论和点赞事实可被用户模型可靠接纳，不表示推荐特征覆盖已经完整，更不表示模型效果已改善。物理移除 usermodel 历史 `tenant_id` 兼容槽的全仓影响与迁移顺序见 `REALM_SCHEMA_MIGRATION_IMPACT.md`。
