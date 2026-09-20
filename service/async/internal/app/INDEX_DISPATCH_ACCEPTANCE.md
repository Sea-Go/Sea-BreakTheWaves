# 索引 READY 向 RTW 接纳的持久交接

状态：**LOCAL_VERIFIED / H06 整体 PARTIAL**。这里的接纳是 RTW build `READY`，不是人工发布指针，也不是 DC 技术任务的成功别名。

## 权威顺序和状态

1. Worker 在任何 Graph 构建前把当前 DC `job_id`、`worker_id`、`attempt_id`、`lease_epoch`、`cancel_version` 与租约写入 `content_index_dispatch`；不保存 DC/RTW 认证令牌。`IndexCoordinator` 在同一 PostgreSQL 事务提交本地 `READY`、不可变 IndexManifest Ref 和 `content_outbox` 事件。只有 outbox 出现后可扫描交接。
2. `RunOnce` 先扫描未 delivered 的 READY outbox，`FOR UPDATE SKIP LOCKED` 领取 30 秒 dispatch lease，`claim_epoch` 拒绝旧扫描器确认。外部 RTW、DC 和本地确认共用 `claim_until-1s` 的 context 截止；超时后必须重新领取。RTW `BUILDING` 必须与当前 DC fence、固定 build 身份及 lease expiry 完全一致，才请求 `AcceptBuild(READY, IndexManifestRef/Hash)`。RTW 回执不确定时用 `GetBuild` 回读；只有精确同 build/代/取消版本/Ref/hash 的 `READY` 可记录 `rtw_accepted_at`。
3. 已核 RTW `READY` 后，才用同一 DC attempt 送技术 `succeeded + sha256 Ref`；DC 回执不确定时 `GetJob` 验证完整 worker/fence/Ref。两端都确认后，本地事务把 dispatch 标为 `complete`、outbox 标为 delivered。崩溃在任一边界后，未 delivered 事件仍可被扫描；重复 RTW/ DC 结果按各自同 fence/同 Ref 幂等合同对账。
4. `needs_new_attempt` 表示旧 RTW claim/ DC lease 已过期或 DC job 进入可重领状态，扫描器不拿旧 fence 冒认成功；同一 DC job 的新 attempt 可用更高 epoch 重新 Attach，保留本地 READY 和既有 `rtw_accepted_at`，但仍必须用 RTW `GetBuild` 再核当前同 Ref READY。`manual` 表示 RTW 终态冲突、DC attempt 耗尽/取消或无法继续自动接纳。RTW 的 build epoch 单调，DC epoch 却以 job 为单位；**新建另一个 epoch 从 1 开始的 DC job 不能当作原 build 的合法更高 fence**。操作方须查对 RTW build、DC job 和 outbox，由权威调度决定可继续的同 job attempt 或新 build/generation，BTW 不自造 epoch。

RTW 可能已接纳 `READY` 而 DC ACK 暂时失败，此时 RTW 仍未发布，outbox 保持 pending/待新 attempt。`delivered_at` 只代表两端回执均确认，不代表产品可检索。若 RTW 在接纳前因修订撤回拒绝索引，DC 不获成功 ACK；若接纳后发生产品撤回，后续发布/检索由 RTW 当前指针门禁决定。

## 证据和限制

| 验证 | 结果 |
| --- | --- |
| `bash scripts/test-content.sh` | PASS。隔离 PG16、迁移重复应用、并发唯一领取、租约过期重领、旧 claim_epoch 拒收、同 Ref 新 DC epoch 恢复及 outbox 最终 delivered。 |
| `go test -mod=readonly -race -count=1 -run 'Test(IndexWorker\|IndexDispatch\|DecodeIndex)' ./internal/app` | PASS。RTW 已提交但 HTTP 回复丢失、未提交后重启扫描、旧 claim、RTW 取消/终态冲突、DC attempts 耗尽、旧 RTW 接纳标记与当前 Ref 冲突、单次成功顺序。 |
| `bash cmd/worker/acceptance.sh` | PASS。真实子进程、PG16、框架 Graph/Runner、三路 exact 数值索引和探针；RTW/DC HTTP 为协议替身，断言 RTW 同 fence/Ref READY 先于 DC 技术 ACK，最终 outbox delivered。 |
| `go test -mod=readonly -race -count=1 ./...`、`go vet ./...`、`go mod verify` | PASS。根模块 race/vet/依赖完整性。 |

仍须由 RTW 集成 owner 以**真实 go-zero HTTP + 独立 PG16 + 共享内容寻址工件**联验 `AcceptBuild`，包括同 Ref 重复调用、HTTP 回执丢失后 `GetBuild`、过期/旧 fence、撤回拒绝和进程重启扫描。当前的 HTTP 替身不能代替 RTW 权威事务证据。正式 BGE-M3/稠密/稀疏/多向量引擎同代、人工发布、Collector/Tempo/Loki 链路和容量验收仍未通过，不能将此切片记作 H06 整体验收。
