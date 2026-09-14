# BTW 消费 RTW 已验证答案历史：局部验收

日期：2026-09-14。RTW 固定权威提交`365cb86`，BTW 基线`af4499e`。本切片只扩充`internal/clients/ridethewind`提供者生成DTO/窄客户端与`internal/app/RTWAcceptedRootHistory`，不修改搜索Graph、RTW源仓、正式产品HTTP或用户身份服务。

提供者`api/knowledge.api`经goctl 1.9.2产生`AcceptedSubjectRef/CommitAcceptedAnswerReq/AcceptedAnswer/ListAcceptedAnswersReq`等类型，BTW的`generate.py`从该固定提交重生`wire.gen.go/source.lock.json`，无手工生成文件修改。客户端`CommitAcceptedAnswer`、按AnswerID+完整SubjectRef/SessionID的`GetAcceptedAnswer`和有界有序`ListAcceptedAnswers`均对RTW回执再次核对。`RTWAcceptedRootHistory`实现现有`search.AcceptedRootHistory`：将已校验`AcceptedRootTurn`原JSON交给RTW，POST成功只在精确AnswerID/SearchID/主体/会话/turn一致时成立；POST回执在RTW提交后丢失时，按原AnswerID+完整scope回查，仅同turn才当成功。`List`按接纳序号分页，跨主体或顺序漂移拒收，超过当前接口上限1000回合显式报错而非悄悄截断。产品turn不是框架可续跑Session事件。

`TestRTWAcceptedHistoryHTTPCommitRecoveryAndScope`让真实BTW生成客户端走隔离HTTP合同fixture：先模拟RTW把`insufficient`合法空证据产品turn提交后POST回503，再由GET原scope回查恢复；按同主体会话读受控turn，跨tenant与损坏的turn响应均拒。`go test -mod=readonly -race -count=1 ./internal/app ./internal/clients/ridethewind`通过。RTW提供者自身`service/knowledge/scripts/acceptance.sh`已在隔离PG16/真实HTTP中证明成功回答与空证据两种终态的原子持久化/幂等/冲突/分页；本消费者测试尚未将BTW与**同一RTW真实PG进程**一起运行。

本切片状态仅`LOCAL_VERIFIED`。真实RTW授权用户到完整SubjectRef、正式搜索/Answer HTTP与SSE、经过实际引用接纳的同进程双方联验、框架跨轮已接纳历史注入、Collector→DataCenter下钻均`NOT_VERIFIED`；H02/H07/WS02-D整体不得标ACCEPTED。RTW按完整scope返回的产品历史不能被BTW误用作未经校验的Agent模型上下文。
