# Favorite v1/v2 权威消费与局部验收

状态：**消费者先行的开发分支候选**。RTW Favorite 的新业务事实使用外层与 payload 的 `schema_version=2`、`subject_ref={"issuer":"rtw.identity","subject_id":"<positive RTW UID>"}`；旧冻结 v1 事件继续保留外层版本 1 与原三元主体。BTW 正式 `FactWorker` 的 allowlist 改为 `(EventType,schema_version)`，对建立/撤回两种 EventType 同时声明 v1/v2 四种精确绑定。未知版本不做宽松解析，不 ACK DC 游标。

`FavoriteAuthorityBinder` 根据 **DC 冻结外层版本** 选择 RTW `/internal/v1` 或 `/internal/v2` 私有事实读口，先核 RTW 原 EventSpec 与 DC 批次的 JCS 字节/hash、`producer,event_id`、原 DC `receipt_id,input_hash,offset,received_at`；再以同版严格解码核 RTW 读口与 payload 的主体形状。v2 仅 `issuer/subject_id`，旧 `platform` 是内部兼容投影；它不作为事实来源，也不从客户端、DC UUID 或昵称推 UID。领域事件仍交给现有 tRPC-Agent-Go `FactGraphRuntime`，经 PG 领域事实和同事务 Outbox 接纳后才 ACK。当前 Graph 原存储以旧 `authority_id/tenant_id/subject_id` 表示规范化主体；v2 影子存储、coverage 与 ServingBundle 的正式读切换是别的里程碑，本切片未声称完成。

## 真隔离跨仓验收

固定源码：RTW 当前独立开发头 `89e06fc`（Favorite 业务 `a1e2af8`）× 本 BTW 候选 `0aa5f96` × DC 已推开发头。脚本只使用隔离开发检出，本机临时 PostgreSQL 16、随机测试服务令牌和 loopback 服务；ready 文件权限为 `0600`，令牌不打印到日志。原 v1 进程验收脚本的默认入口保持原配对。

```bash
SEA_DC_PLATFORM_ROOT=<isolated-DC-checkout> \
SEA_RTW_FAVORITE_ROOT=<isolated-RTW-checkout> \
bash cmd/worker/favorite_subjectref_v2_acceptance.sh
```

2026-09-15 上述脚本顶层退出 0；RTW `fact-dispatch`、私有权威 HTTP、实际 DC `cmd/platform` 与 BTW `FactWorker`/tRPC Graph/PG16 同一次运行。RTW 源同一 producer 的 offset 1–3 固定为 UID1001 v2 建立、UID1002 原 v1 建立、UID1001 v2 撤回，各只发一个 `(producer,event_id)` 与一个 DC receipt。BTW 错令牌、缺读口、改源 hash、改 RTW 主体均在 Graph/ACK 前拒绝且 ACK仍0；随后三条事实与三个领域 Outbox 完整提交，但首 ACK 故障不动 DC 游标。同键重读均为 replay、无第二份事实，并只 ACK 到 offset3。UID1001 的当前领域状态 version2 且无活跃收藏/两个 Outbox；UID1002 version1 且一活跃收藏/一个 Outbox。测试刻意让 DC 技术受理未知 schema3 为 offset4；BTW 固定版本门禁拒 ACK，游标留在4，不跳号。该合成行用于验证拒错，**不是可上线的恢复机制**；恢复须由来源 owner 制定 v3 合同/隔离处置，并以 DC 全局连续收据保证不丢事件，不能靠 BTW 扫描跳过。

日志是结构化 JSON，v2 事件终态带稳定 event/run 与 trace/span 关联；同一事实运行存在 instrumentation scope 为 `trpc.agent.go` 的原生 Graph span，Prometheus 含框架原生指标但无 UID 标签。`go vet ./cmd/worker`、`go mod verify`、`git diff --check` 退出 0。从当前 RTW 头重新联验后的 RTW 子测试日志 SHA-256 `9948010c2fa06660baa1443f1689e26f03b4fbec8d053e673cbcc76736499c66`，BTW 子测试日志 SHA-256 `967274c22e9da24a03c79ce26d2e2693770f90f332e02cbb0f16880aa83d9124`；PG 停机日志 SHA-256 `ca19178a35ab4153b75b494963b66ce8243e1b94c173107c87b44d09db23652d`，停机后 `pg_ctl status` 退出3。

```bash
SEA_DC_PLATFORM_ROOT=<isolated-DC-checkout> \
SEA_RTW_FAVORITE_ROOT=<isolated-RTW-checkout> \
SEA_EXPECT_FAVORITE_REVISION=article-shared-authority:r1 \
bash cmd/worker/favorite_acceptance.sh
```

原 v1 默认进程配对也在变更后顶层退出 0，RTW 源与 BTW `cmd/worker` 真进程两端 PASS，历史 r1 建立/撤回照旧联验。原 v1 RTW 子测试日志 SHA-256 `427dbc8dde082316d5b7e9995b48ef051186f37329880a032106f5bb2cd88908`，BTW 进程测试日志 SHA-256 `3cbb4c237aed9d9cd6d94e11e9646843ba87aac67f415630ef37d795ea929485`。

## 仍需交接

- RTW 新 v2 Favorite Fact owner 是 dev/test 默认关闭候选；旧生产数据库可能有未建立 Outbox 的业务收藏。RTW 当前开发候选新增004显式 legacy marker、受锁只删业务旧行和全量只读 CLI 预检，但生产合法旧行枚举/批准、无前驱撤回例外裁决与冻结水位报告未签收，发版仍可能阻断用户撤回，故本链不具生产准入。
- BTW `warehouse_favorite.ods_event` 当前正式 writer 仍只认原 v1 `schema_version=1`；v2 新写 ODS/CH/dbt/Dataset/watermark 与两 UID 稀疏覆盖不由本 `FactWorker` 测试签收。不得把领域 Graph 成功写成数仓已更新或 ServingBundle 激活。
- 本机 UID 来自 RTW 固定事实夹具。实际 UserCenter/JWT、H01 同人关联、真实生产 DC 与高层产品流量均未运行；HMAC `Scope` 只属于知识搜索 Summary/Tools，不属于 DC Favorite 事件。
