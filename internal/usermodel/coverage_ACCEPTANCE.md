# H08 用户模型覆盖证明与历史折叠验收

日期：2026-09-15。本切片基于 BTW 集成 `584964b`，只在独立树写 `internal/usermodel/coverage_*` 与专属迁移；共享 `internal/sourcecoverage` 的批窗核验修复 `7fe6405` 由其唯一 writer 提交，本树仅快进继承。Docs、WS07 Publisher、H10、旧水位/基线/Bundle 与正式 worker 均未修改。

## Interface、事务与恢复

- `NewCoverageVerifier(Store, CoverageProofSource)` 的 `VerifyPrefix(ctx, GlobalPrefixRef, EventIndexJSONL, BatchEvidenceJSONL)` 首次从 offset 1 重算全局索引根和共享 `VerifyBatchEvidence`，上限 **100000 行/64 MiB**，每行在 PG 事务外经 `CoverageProofSource.VerifyEvent` 复查 DC immutable receipt 与 RTW 冻结 EventSpec/SubjectRef/hash；随后单个 PG repeatable-read 写事务逐行核已接纳 `usermodel_events`、原规范化 hash、来源 offset、动作/修订和同版本 Outbox，原子插入根及全局侧表。缺事实、pending 或缺 Outbox 返回 `ErrCoveragePending`；来源/主体/哈希冲突返回 `ErrCoverageConflict`。禁止以旧 per-subject `Watermark` 代替全局证明。
- 缓存的同 manifest 重放仍先重算传入索引、批窗和 manifest hash，再在一个 PG 读快照核 ref 与侧表 `count=W`；缓存表 UPDATE/DELETE 被迁移触发器拒绝。重放不重复远端或全 PG 历史遍历；后续每次 `CoveredStateAt` 在同一 RR 快照重新核**该主体**的 PG event_body 规范化 hash、accepted/status/Outbox 与缓存稀疏行，若 PG 事实后改返回 `ErrCoverageConflict`。这不宣称数据库管理员绕过触发器的任意外部破坏不可发生，跨主体全量复审需要显式新一轮全局验根。
- `VerifySubject(ctx, SubjectCoverageRef, SparseIndexJSONL)` 从已验**完整**全局侧表重算该主体有序稀疏切片、hash/count，并持久缓存主体收据。空切片为零字节 SHA-256，只有绑定已验全局根后才是可证零事件。只给主体列表或仓库签名无法调用此入口绕过全局核验。
- `Store.CoveredStateAt(ctx, subject, prefix, cutoff)` 返回 `CoveredState{Coverage,StateVersion,ActiveAtPrefix,Tail,CurrentComplete}`。W 内按 DC 全局 offset 重放该用户**全部历史**，不筛当前 `Current.Active`；W 后的 `Tail` 包含 `CoverageTailEvent{EventKey,Offset,Status}`，状态保留 `accepted` 与 `pending_dependency`。`CurrentComplete` 仅指该 PG 快照中没有已知尾部，DC 鲜度与 H10/Serving 的正式启动仍需独立门禁。cutoff 只在当前已固定 PG 快照内判断业务时间，不是任意旧时刻的提交历史。未接纳 v2 根/主体返回 `ErrCoverageUnverified`。

迁移顺序：先按原部署执行 `migrations/usermodel/001_facts.sql`（嵌入为 `usermodelmigration.SQL`），再**显式**执行 `migrations/usermodel/002_coverage_verification.sql`（`usermodelmigration.CoverageSQL`）。构造 Store/Verifier 不运行 DDL。旧 `usermodel_watermarks`、v1 FeatureBaseline、FeatureSnapshot 和 ServingBundle 原样保留且不能自动升格；需要 WS07 从 DC offset 1 完整发布全局工件、此模块重算并核 RTW/PG 后，再由 H10 独立接纳 v2 候选。

## 隔离验收证据

`USERMODEL_KEEP_EVIDENCE=1 bash internal/usermodel/test-postgres.sh` 在随机端口 PG16 上以 `-race -count=1` 退出 0；证据 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T//sea-usermodel-acceptance.8ZtOM3`。固定 Proof Adapter 验证 `[u1:1,u2:2,u1:3]` 后两主体切片分别 `[1,3]`、`[2]`，第三个从未写事实的主体获得空切片且 `StateVersion=0`；同根/主体收据重放幂等，改索引或批证据字节、漏主体事件、未证明旧事实、修改缓存或 PG fact_body 均拒绝。第二反例 offset1 assert、offset2 pending、offset3 retract 已接纳：W3 被拒，W1 历史仍有 assert，尾部包含 accepted retract，而当前 `Current.Active` 已为空；pending 主体尾部也明确保留。

`SEA_DC_PLATFORM_ROOT=<隔离DC> SEA_RTW_FAVORITE_ROOT=<RTW修订冻结树> internal/usermodel/coverage_source_acceptance.sh` 自建 PG16、真 DC `cmd/platform`、RTW `cmd/fact-dispatch`/私有 authority，RTW 共享窗口交付两条高位收藏正反事实，BTW 在独立 PG 中写入同源领域事实后由真实 `FavoriteCoverageHTTPProof` 再向 DC 与 RTW 回查完整 EventSpec、主体和回执。`TestCoverageVerifierRealDCAndRTW` 与 RTW 源用例均 race PASS、脚本退出 0；伪造仓库主体拒收，Verifier 自身不 ACK DC consumer。最终证据 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T//sea-coverage-source.KCXZTG`。该真源用例中的领域 PG 写入是测试映射，**不是**同次正式 FactWorker/WS07 Publisher/H10/Serving 进程联验。

另运行 BTW `go test -mod=readonly -count=1 ./...`、`go vet ./...`、`go mod verify`、`git diff --check` 与脚本语法检查均退出 0。

WS07 提供 `GlobalPrefixRef` 内两份内容寻址 URL、`SubjectCoverageRef` 内稀疏 URL；调用者以对象 Adapter 取原始字节再传此模块，Verifier 独立重算而不信 URL/签名。当前单次上限 100000 行，远端逐行复核为 O(W) 并可能较慢；根持久后主体读取为 O(log W + 该主体事件数)。大前缀分片、真实对象 Adapter 与 WS07→WS08→H10 同次交接是后续切片。WS08/H08/H10 总项仍 `PARTIAL`。
