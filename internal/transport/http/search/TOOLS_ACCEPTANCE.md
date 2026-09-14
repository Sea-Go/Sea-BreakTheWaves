# H02 Tools 搜索 BTW 消费者验收

日期：2026-09-14。工作树 `feat/tools-http-consumer-20260914`，基线 `a72adc0`。`[W0]` 本隔离 BTW 工作树；`[W1]` `internal/search/`、`internal/transport/http/search/`、`internal/app/search_rtw_snapshot.go`、`cmd/api/`；`[R1]` RTW 独立工作树 `feat/knowledge-tools-scope-20260914` 的签发客户端、合同和 PG 验证；`[D1]` 根模块锁定 tRPC-Agent-Go v1.8.1；`[G1]` 无；`[X1]` 本地 test HTTP/OTLP fixture，真实 RTW PG 由 RTW 验收脚本隔离拥有；`[N1]` 原始脏工作树、Sea-Docs、生产、客户端；`[T1]` Go 测试临时目录。主职责 `[C1:TRANSPORT]`，跨 `[C2]` Delivery、`[C5]` Graph/Runner、`[C7]` RTW 签发线、`[C8]` 测试。

## 合同与提交点

`POST /v1/search/tools/search` 只接受规范 Go JSON body 与单值 `X-Sea-Search-Tools-Scope`。BTW 验 rawurl 编码、HMAC、固定字段顺序、未知/重复字段、受众、120 秒以内 TTL、完整主体与父操作 ID、body SHA256、RTW 原始规范快照 JSON 的 `snapshot_ref`，再允许任何 Graph、SourceReader 或引用接纳调用。RTW 固定的 `search_id` 直接用于 `Delivery.SearchWithin`，本次读数/引文上界不会改写共享 Delivery；跨请求父预算、预扣与退还只由 RTW PG 负责。当前进程仅装配 `local-exact/fast/low`，其余已签档返回 503。

Graph 采用 tRPC-Agent-Go v1.8.1 `graph.NewStateGraph`、`graphagent.New` 和项目 `Runtime` 完整消费 Runner 事件；每次调用使用并删除私有 Session。Graph 节点调用现有 `Delivery.SearchWithin`，由 RTW 适配器按固定发布读同版原文并耐久接纳非空 pack。仅通过完整 Runner、pack/收据校验后返回裸 `ToolSearchResult`；`evidence`、`gaps`、`conflicts` 即使空也为 `[]`。`conflicts` 当前固定为空，因为 RTW 耐久包无可核冲突字段。`stop_reason` 非空且≤256；有证据时公开的智能等级和深度与 pack.profile 对齐。空证据没有 pack hash/收据。没有模型总结、没有 `ToolSession` 自生 ID/父预算。

RTW `GetCurrentSearchSnapshot` 的空 `valid_revision_ids` 为 JSON `[]`，现保留这一形状，不再被 BTW 拷贝改成 `null`，否则同版快照在 RTW pack 校验处会错配。

## 验收与限制

`TestSignedToolsScopeAndNativeGraphHTTP` 在隔离子进程中发送漏签、重复头、坏签、非法编码、错受众、错快照 hash、超长时间窗、body hash 错、缺字段与未知嵌套字段；均返回 403，零原文读和零引用接纳。真实已签 `fast/low` fixture 走 Graph、一次原文读与引用接纳，返回裸引用、同一 pack hash/收据和实际用量；有 `trpc.agent.go` 原生根 Agent Span 与统一 JSON stage 日志。已签 `high` 返回 503，不进入 Graph。`cmd/api` 真回环 socket 对无签名 Tools 返回 403。`TestCloneSnapshotPreservesEmptyRevisionArray` 守住空发布集合形状。

`TestRTWRealToolsSearchServer` 是可选 RTW 真 User Center/PG 联验子进程，要求 RTW 测试提供 0600 JSON fixture（`rtw_base,worker_token,scope_key,ready_path,module_id`），启动前读取真实 RTW 当前发布，并将服务 URL 写入 `ready_path`。不带 candidate 时，收到一条签名空证据子搜索后验收 1 次原生 Graph 搜索、零原文读和零引用接纳；带 `candidate:{revision_id,chunk_id,quote_hash}` 时，先验证该 chunk 属于 RTW 当前发布，再由真实 `RTWSearchCitationAdapter` 重读同版原文并接纳引用，验收 1 次 Graph/Reader/Accept。候选片段只用于确定性跨仓夹具，尚未证明本地 exact 三路召回。

空证据跨仓子链已在 RTW 独立分支 `30a9083` 与 BTW 本独立树上验证：`GOFLAGS=-p=2 GOMAXPROCS=2 KNOWLEDGE_KEEP_EVIDENCE=1 KNOWLEDGE_REAL_USER_GATE=1 SEA_BTW_TOOLS_CONSUMER_ROOT=<BTW独立树> bash service/knowledge/scripts/acceptance.sh` 退出 0；`TestRealHTTPKnowledgeWorkflowWithUserCenter` 31.55s、普通工作流 12.52s 均通过。RTW 父测试断言真实签发请求原样进入 BTW、空证据在 PG 只提交一个子操作、没有 citation 行、同键 POST/GET 回放不重复执行，父搜索次数扣一次而读数/引文上界退款。首次未限制并行度时，真实 User Center 第二次注册 RPC 触发固定 2 秒超时，未到达 BTW；重试通过。日志在 `/tmp/sea-rtw-tools-btw-cross-retry-20260914.log`，保留的隔离 PG 证据目录为 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T/sea-knowledge-acceptance.WWKUe5`。有证据跨仓子链及父预算实际用量退款仍待 RTW 父测试运行验证。

本消费者还未证明真实本地 exact 三路工件对某一已发布 chunk 的召回、生产 BGE/模型、详搜/中高智能、RTW 发布撤回并发竞争、WhaleHall Bun 父 Agent 多次搜索或 Collector 跨进程下钻。H02 Tools 整体为 `PARTIAL`，不能标记 `ACCEPTED`。

## 有证据跨仓子链追加验收

RTW集成树追加第二个独立BTW子进程夹具后，以`GOFLAGS=-p=2 GOMAXPROCS=2 KNOWLEDGE_KEEP_EVIDENCE=1 KNOWLEDGE_REAL_USER_GATE=1 SEA_BTW_TOOLS_CONSUMER_ROOT=/Users/edy/Sea/.codex-worktrees/sea-btw-runtime-content-20260914 bash service/knowledge/scripts/acceptance.sh`完整重跑，退出码0；真实User Center版和gRPC替身版工作流分别PASS（48.86秒、23.26秒），其余Knowledge/User Center race、vet也通过。首次联验在新增真实引用后只因旧总指标期望仍按原引用数失败；补计本次**一笔新引用**后通过，同键重投不增加提交。

RTW父端只交给第二个BTW进程已发布chunk的`revision_id/chunk_id/quote_hash`。BTW先从RTW当前发布重读并核哈希，原生tRPC Graph/Runner执行固定候选，`RTWSearchCitationAdapter`再次读同版原文并在RTW PG接纳pack/收据；RTW父端核引用行、公开证据/收据、固定ID的POST/GET与跨主体拒读，父预算只收1次实际读及公开引文字数、未用预留退回。再通过RTW产品`evidence-reads`从其耐久pack重读，同键只扣一次。BTW子进程退出断言一次根Agent Span、一次SourceReader和一次CitationAcceptor。本次候选仍是验收指定的**真实已发布chunk**，没有由Tools正式三路local-exact索引召回；也未经过正式`cmd/api` socket或WhaleHall父Agent。故只把“签发→BTW有证据Graph→RTW耐久引用/预算/重读”子链标`INTEGRATED`，H02 Tools整体仍`PARTIAL`。
