# BTW 消费 RTW 已验证答案历史：局部验收

日期：2026-09-14。RTW 固定权威提交`365cb86`，BTW 基线`af4499e`。本切片只扩充`internal/clients/ridethewind`提供者生成DTO/窄客户端与`internal/app/RTWAcceptedRootHistory`，不修改搜索Graph、RTW源仓、正式产品HTTP或用户身份服务。

提供者`api/knowledge.api`经goctl 1.9.2产生`AcceptedSubjectRef/CommitAcceptedAnswerReq/AcceptedAnswer/ListAcceptedAnswersReq`等类型，BTW的`generate.py`从该固定提交重生`wire.gen.go/source.lock.json`，无手工生成文件修改。客户端`CommitAcceptedAnswer`、按AnswerID+完整SubjectRef/SessionID的`GetAcceptedAnswer`和有界有序`ListAcceptedAnswers`均对RTW回执再次核对。`RTWAcceptedRootHistory`实现现有`search.AcceptedRootHistory`：将已校验`AcceptedRootTurn`原JSON交给RTW，POST成功只在精确AnswerID/SearchID/主体/会话/turn一致时成立；POST回执在RTW提交后丢失时，按原AnswerID+完整scope回查，仅同turn才当成功。`List`按接纳序号分页，跨主体或顺序漂移拒收，超过当前接口上限1000回合显式报错而非悄悄截断。产品turn不是框架可续跑Session事件。

`TestRTWAcceptedHistoryHTTPCommitRecoveryAndScope`让真实BTW生成客户端走隔离HTTP合同fixture：先模拟RTW把`insufficient`合法空证据产品turn提交后POST回503，再由GET原scope回查恢复；按同主体会话读受控turn，跨tenant与损坏的turn响应均拒。`go test -mod=readonly -race -count=1 ./internal/app ./internal/clients/ridethewind`通过。RTW提供者自身`service/knowledge/scripts/acceptance.sh`已在隔离PG16/真实HTTP中证明成功回答与空证据两种终态的原子持久化/幂等/冲突/分页；本消费者测试尚未将BTW与**同一RTW真实PG进程**一起运行。

后续两仓真实进程联验：RTW集成树设置`SEA_BTW_CITATION_CONSUMER_ROOT=<BTW固定集成树>`运行完整`service/knowledge/scripts/acceptance.sh`。它启动实际RTW go-zero HTTP与隔离PG16，BTW子进程先从固定已发布源生成并接纳一条引用，再用`RTWAcceptedRootHistory`提交**同一search_id/证据包/耐久引用**的`succeeded`产品turn；精确重放只留一条历史，按完整SubjectRef/SessionID列表读回引用，跨主体按AnswerID读取被RTW拒绝。RTW侧对同一AnswerID实际PG计数为1；原文、引用与答案接受的JSON终态共用BTW父Trace ID。双方race及RTW全知识服务vet通过。该测试仍使用结构性三路IndexManifest，不含真实模型/索引。

本切片的**答案历史子合同**已在隔离环境达到`INTEGRATED`。真实RTW授权用户到完整SubjectRef、正式搜索/Answer HTTP与SSE、框架跨轮已接纳历史注入、Collector→DataCenter下钻均`NOT_VERIFIED`；H02/H07/WS02-D整体不得标ACCEPTED。RTW按完整scope返回的产品历史不能被BTW误用作未经校验的Agent模型上下文。

## 2026-09-15 当前开发头的 AnswerID 回读

上段是 2026-09-14 固定阶段的状态。当前 BTW 正式 `cmd/api`/HTTP 已只接受 `RootSessionBoundary` 并从本适配器访问 RTW 产品历史；RTW 产品 façade、真实 UserCenter/JWT、知识 PG、同版引用和 BTW 原生根 Graph 在后续开发头已有本机跨仓联验，跨轮模型上下文与生产仍未接纳。

本适配器新增 `search.AcceptedRootLookup.Get`，按完整 SubjectRef/SessionID/AnswerID 调用 RTW Worker GET。只有 RTW scoped HTTP404 返回“无已接纳答案”；503、错误 Scope、缺接纳序号/时间、损坏 `TurnJson`、非 `succeeded/insufficient` 终态、Answer/Search/主体/会话/Status 任一回执漂移都拒绝。`RootSessionBoundary` 在启动 SourceReader/Agent 前校固定请求整体一致，再复制已接纳结果；相同 AnswerID 异输入不能以新结果覆盖老 turn。RTW 产品 façade 仍负责不可变 operation 请求 hash/当前发布和引用状态的最终复核，框架裸 Session 不参与回读。内存测试验证同键重试模型调用数不变、关闭时在途历史提交取消并等待；RTW Worker HTTP 夹具验证错主体 scoped404、损坏/503 不误作“无记录”。RTW 410652b 本机 PG16 完整知识/UserCenter 低并发脚本退出0，真实历史与旧 v1 `TurnJson`/GET 原字节保持一致。该联验仍不是生产部署或 12 组合模型质量验收。
