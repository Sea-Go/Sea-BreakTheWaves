# DC 技术租约与 RTW 构建 fence 分离验收

日期：2026-09-14。BTW 分支 `feat/build-fence-handoff-20260914` 基于 `8e1f56a`，RTW 配套分支 `feat/knowledge-build-fence-20260914` 基于 `776d39b`，DC 现有 job 服务基线 `e8c8cd68`。本轮只验 **H04/H06 的跨 job build-fence 及固定来源三路 READY 子链**，不将 H06 整体标为完成。

工作区：`[W0:ROOT]` 三仓各自独立工作树；`[W1:WRITE]` BTW `internal/app/`、`internal/content/`、`internal/clients/ridethewind/`、`migrations/content/` 及关联 `cmd/worker` 测试/本文，RTW 知识服务 build 模型与隔离测试；`[R1:READ_ONLY]` Docs、原始脏树、三仓集成树和 DC job 源码；`[D1:DEPENDENCY]` Go module 与 tRPC-Agent-Go v1.8.1；`[G1:GENERATED]` 无直接改写；`[X1:EXTERNAL]` 测试自建回环 RTW/User Center/DC cmd/platform、专属 PG 与本地对象目录；`[N1:OUT_OF_SCOPE]` 生产 DC/RTW、Milvus、正式 Collector；`[T1:TEMP]` 随机测试目录和一次性数据库。主职责 `[C2:APPLICATION]` 与 `[C4:PERSISTENCE]`，跨 `[C1:TRANSPORT]`、`[C5:AGENT_ADAPTER]`、`[C7:CONTRACT]`、`[C8:VERIFY]`。

DC `lease_epoch` 只在单个 job 内递增。RTW `ClaimBuild` 现在接受 `lease_epoch=0` 作为**请求 RTW 分配 build-wide epoch**，在模块/build 行锁下对新的 attempt 严格加一；同 attempt 重投保持原代，当前租约未过期时可在原代单调延长 expiry，不允许缩短或过期复活。显式正数旧客户端只允许当前同 attempt 或严格下一代，不能跳号/倒退。RTW `AcceptBuild` 仍按当前 attempt、全局 epoch、generation、manifest、cancel 及 lease 有效性接纳；后续 grant 会使先前 attempt 的迟到 READY/FAILED 无法提交。新 job 即使本身从 DC epoch 1 开始，BTW 也只发 RTW `lease_epoch=0`，**不能自行把 1 算成 RTW 下一代**。

BTW 的 PrepareWorker 与 IndexWorker 共同经 `claimAllocatedRTWBuildFence` 请求并核对 RTW grant；HTTP 回执丢失时按 BuildID 读 RTW 当前行，只有与本次 attempt、expiry、固定 build 身份和应分配代次完全一致才继续。内容 Build、Graph/Runner 和 RTW READY 使用 RTW 返回的 fence；DC CompleteJob/技术收据保持原 DC job attempt、epoch、cancel version。版本化迁移 `003_separate_dc_rtw_fences.sql` 将 `content_index_dispatch` 的旧 `lease_epoch` 保留为 RTW build fence，另持久记录 `dc_attempt_id,dc_lease_epoch,dc_cancel_version,dc_lease_expires_at`，对旧行幂等回填。父行与 `claim_epoch` 排斥并发/重启扫描；更新到新 RTW fence 时清空旧 `rtw_accepted_at`，同 RTW READY Ref 的新 DC 技术 attempt 可以独立重试 ACK，但必须重新查 RTW 当前 READY。RTW 同 Ref 接纳早于 DC 技术 ACK，后者成功后才交付 outbox；本地 READY 从不等于 RTW 发布。

真实隔离联验先由 RTW 原有完整脚本启动 go-zero HTTP、真实 User Center 与独立知识 PG，创建未 claim Build。BTW 消费子进程先以 Prepare attempt 从 RTW 获 build epoch 1、用真实修订生成 chunk；新索引 DC job 从本身 epoch 1 Claim，却从 RTW 获该 build 的 epoch 2。BTW 的真实 IndexWorker/Graph 在本地 exact 三路数值夹具下构建并提交 READY/outbox，再由 RTW 真实 PG `AcceptBuild` 核同 Ref，最后向 DC 真实 `cmd/platform -migrate` HTTP/PG job 服务报告技术成功。RTW 父测试直接核对 DC 专属 `jobs.job`/`jobs.attempt` 的 epoch 1 与 sha256 Ref、RTW Build epoch 2/IndexManifest、恰一次接纳 Outbox、未被自动人工发布。DC `/v1/jobs/*` 只经透明回环转发，实际 Submit/Claim/Complete/Get 与 PG 状态由 DC 服务维护；`/v1/representations` 仍是显式固定 2 维 HTTP 夹具。

复验命令：

```bash
# BTW 独立树：本地 PG、Graph/worker 与三路 exact 夹具
bash service/async/rpc/internal/content/test-postgres.sh
bash cmd/worker/acceptance.sh

# RTW 独立树：真 User Center/RTW PG/BTW 子进程/真 DC jobs 服务
KNOWLEDGE_KEEP_EVIDENCE=1 KNOWLEDGE_REAL_USER_GATE=1 \
SEA_BTW_INDEX_CONSUMER_ROOT=<本BTW独立工作树绝对路径> \
SEA_DC_JOB_PLATFORM_ROOT=<DC独立工作树绝对路径> \
bash service/knowledge/scripts/acceptance.sh
```

最终三仓脚本**退出码 0**，真实 User Center 与同协议替身两个 `TestRealHTTPKnowledgeWorkflow` 均 PASS；BTW content 脚本与实际 worker 进程脚本均退出 0。RTW PG 单元覆盖新 job epoch 1→同 build epoch 2、同 attempt 续租/丢回执重投、缩短与过期复活拒绝、并发 grant 唯一递增及旧 fence 结果拒收。BTW 隔离 PG 覆盖新 DC job epoch 1 与 RTW epoch 3 分离、outbox 不变、旧扫描器失效、已接纳 READY 的新技术 ACK。RTW/BTW 两个独立分支仍须按依赖顺序集成后再跑当前集成基线，不能把独立树结果充作已部署证据。

第一次三仓脚本的业务链已真实完成，但父测试误把 DC 结果 JSON 读成 `ref.hash`，实际合同为 `result_ref.sha256`，因此最后一个 PG 断言收到 NULL 并退出 1；修正后原业务断言保持不变且完整脚本退出 0。更早的一次两仓测试在链接真实 User Center race 二进制时遇到本机 ENOSPC，未进入业务；清理可再生 Go 构建缓存后按原条件重跑成功。这两个中途失败不作为 H04/H06 业务失败或成功替代证据。

限制：三仓链中的 DC jobs/RTW Build/BTW worker 与 PG 是真实隔离进程和事务，但 Dense/Sparse/Multi-vector 表示仍是固定 2 维 HTTP 夹具，非 BGE-M3；三路索引为 local-exact，非 Milvus。BTW IndexWorker 在测试子进程内实际运行，尚未与 DC 真 job、RTW 真 PG **同次**使用正式 `cmd/worker` CLI；PrepareWorker 的真 DC prepare job也未并入该三仓链。生产对象存储、外部 Collector、取消后的全量补偿与规模质量验收另属后续门禁。
