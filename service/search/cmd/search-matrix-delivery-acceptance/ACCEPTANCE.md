# 十二格矩阵：RTW 持久 Tool 交付子集

日期：2026-09-15。状态：**fast/low/tools 一格实际交付，本轮其他 11 格未执行**。这是 H02/WS09-E 的默认关闭验收，不是十二条完整产品链路，也没有人工相关性分数。

BTW 的可选 RTW 跨仓工具测试在收到 RTW 签发的原始工具请求后，读取当前已发布 snapshot；对 RTW 测试指定的已发布 chunk，`RTWSearchCitationAdapter`按同修订重新取原文并提交 `EvidencePack`。新增的 `SEA_BTW_TOOLS_MATRIX_WITNESS_DIR` 仅在测试进程中启用：记录签发主体/会话/查询、source read 与 citation write 的**实际**次数、pack hash、RTW Worker GET 耐久读回、原生 tRPC-Agent-Go Tools Root/Graph/function spans，退出时写 0600 的单次 witness。没有模型调用；`model_token_cost=null`。工具结果本身仍由原有正式 Handler/Graph/Runner 和 RTW 父端 PG 事务承载，见原 [Tools 交接](/Users/edy/Sea/.codex-worktrees/sea-btw-search-matrix-delivery-20260915/internal/transport/http/search/TOOLS_ACCEPTANCE.md)。

在 RTW 集成 `f244cdb` 与 BTW 此分支上，执行 `GOFLAGS=-p=2 GOMAXPROCS=2 KNOWLEDGE_KEEP_EVIDENCE=1 KNOWLEDGE_REAL_USER_GATE=1 SEA_BTW_TOOLS_CONSUMER_ROOT=<本工作树> SEA_BTW_TOOLS_MATRIX_WITNESS_DIR=<新建0700目录> bash service/knowledge/scripts/acceptance.sh` 退出 0。真实 User Center 版与普通版本各产生一次有证据工具搜索，witness 均确认 `fast/low/tools`、一次 SourceReader、一次 CitationAcceptor、一条 RTW 耐久引用、pack/GET/readback 相等及三层原生 span。RTW 父测试另外以自身 PG 核相同 SearchID 的引用行、公开证据和收据、同幂等键 POST/GET 不重做、跨主体拒读与父读数/引文预算结算。完整测试日志 `/private/tmp/sea-search-matrix-delivery-rtw-20260915.log`，两份 witness 在 `/private/tmp/sea-search-matrix-delivery-witness-20260915/`，字节 SHA 分别 `1766ed1503b3b0bb0b534a34a9201879c72297e36193a4d2ecbf2b0ca3a688c4`、`9c9d939fba95b3560ad279851d20b50e4287d233cea6fd0a3a472eb93a4ecb58`。

`cmd/search-matrix-delivery-acceptance` 对**一份 witness 对应一个已发布快照**单独生成十二格报告。它在 `fast/low/tools` 写真实候选与已核的 durable receipt，其余每格写 `delivery_execution=not_executed` 与 `failed` case，并调用现有 `EvaluateSearchMatrix`。两份报告分别为 `/private/tmp/sea-search-matrix-delivery-witness-20260915/matrix-162c780.json` 与 `matrix-0cab844.json`；各为 observed1/unexecuted11，正式三项 S09/S11/S12 全部 `not_evaluable`、矩阵推荐恒 `incomplete`。两个 RTW 测试实例的 snapshot 不同，绝不合并为同一固定语料矩阵。评测 CaseIdentity 的真实主体/会话来自 RTW 签名；单查询 family/split 是显式 fixture 分组，不能充当发布质量验证。

未执行的原因有明确代码边界：正式 BTW `cmd/api` 配置与 planner 仅支持 `fast_low`，Tools HTTP 对其它档位返回 503；详搜和中高智能缺产品策略与查询改写。已有跨仓 summary 测试使用固定 OpenAI 响应替身，其 token 不是 DataCenter 网关成本，故本轮不把它记为真实 summary 格；没有同次 RTW 来源→DataCenter 本机模型网关→总结 Agent→RTW 耐久答案的成本/追踪证据。已观察的 fast/low/tools 候选由 RTW 已发布 chunk **确定性选入验收夹具**，不是正式三路检索召回。没有冻结 qrel 或候选判断覆盖收据，Recall/MRR/nDCG 不能计算。

本仓 `go test -mod=readonly -race -count=1 ./cmd/search-matrix-delivery-acceptance ./internal/transport/http/search ./internal/evaluation`、相同包 `go vet` 与 `git diff --check` 均退出 0。后续交接需在同一发布快照和真模型成本收据下启动 DC 本地网关，落地中高智能与详搜策略，再由 RTW 发出剩余 11 条产品请求；届时按真实已执行格替换 `not_executed`，仍须独立完整 qrel 证明才有正式相关性指标。
