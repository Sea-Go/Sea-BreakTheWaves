# Wiki编制Worker应用候选与交接

状态：**本机可逆L1应用接口，正式DC作业/RTW对象与模型请求未接纳**。本仓独立分支从BTW内容集成`447b556446f01615de19dc3649cbdb19650d2b67`建立，只新增`internal/app/wiki_compile_worker*`；`internal/content/wiki_compile_{contract,graph}.go`原生编制合同、RTW/DC生成客户端、其它worker和原工件未改。产品/跨部门职责见Sea-Docs《WS02-C与WS06-A-Wiki编制执行交接》。

应用Interface为`WikiCompileWorker.ProcessClaim`与`RunOnce`，内部一次处理当前DC job attempt。它固定RTW/DC提供方的`content.wiki-compile.v1`/`cpu`/`ridethewind.knowledge`，先核`Job.InputHash=整8字段Submit的JCS/SHA`、单版本十二键`rtw.wiki.compile-ticket.v1`、规范十进制generation/cancel、当前worker/attempt/lease/deadline及原`source_event_id`/hash形状。DC技术输入哈希与RTW `Compile.InputHash`是**不同域**；`source_event_jcs_sha256`因RTW私有GetCompile当前不回Outbox原EventSpec，仅可核形状，**不宣称独立原事件字节回读**。随后`GetCompile`核module/page/base/有序SourceRevisionID集、原Guidance UTF-8 SHA、RTW业务input_hash/generation/cancel；每份`GetRevision`必须同module/source/ID、未撤回、原UTF-8正文hash=`ContentHash`及`ObjectKey=sha256/<hash>`。坏票据/原文在RTW Claim前拒。

DC当前job仍同attempt/epoch/cancel后才向RTW `ClaimCompile`送完全相同的lease栅栏。已注入的`WikiCompileRunFactory.Open(frozen WikiCompileInput)`为**每逻辑Compile**造已授权`tRPC-Agent-Go model.Model`/现成Graph/Runner与私有Session，应用自己消费Graph completion及Runner完整EOF、终态错误、模型JSON/原来源`paragraph:N`和ticket锚；应用不硬编码某个模型或使用DC jobs service token调用模型Gateway。当前DC登记Chat CallPoint `knowledge-wiki-compiler`却无有效模型配置；模型适配由单独owner提供DC native app_user Bearer与每逻辑Compile稳定幂等Ref，尚未合入本分支或以真实DC账号验。应用用技术Session内部键分隔Compile/attempt，**不签外部RTW用户SubjectRef**。

Runner通过后再次核RTW/DC lease与取消，再将候选Markdown**原字节**写内容寻址`artifacts.Store`并同Store Get逐字节读回；当且仅当RTW接纳与该Store共享同根或同桶，RTW `AcceptCompile`才能自己用其`ObjectStore.Get(key,hash)`证明原字节。再次核当前job/compile后送`ObjectKey,ContentHash,Title,SourceRefs`，只认RTW `State=ACCEPTED`、原Compile/attempt/epoch/cancel及非空RevisionID/ResultHash；`ResultHash`是RTW对**整个Accept请求**的摘要，不能当Markdown内容SHA。未知RTW HTTP回复先`GetCompile`核未变栅栏，再用**完全同一Accept请求**重投让RTW自身核同体ResultHash；错栅栏/取消/同键异文不自动恢复，也不覆盖人工Wiki修订。

DC `CompleteJob`只能在RTW业务接纳之后。提供方明确尚无跨仓成功`ResultRef{URI,Hash,MediaType}`冻结合同，构造器的`CompletionRef=nil`为默认：RTW已接纳时返回`ErrWikiCompileTechnicalPending`和结构化`partial/rtw_accepted=true/dc_completed=false`，**不会调用DC Complete/ACK**。本机仅通过显式fixture `CompletionRef`验证顺序及丢RTW/丢DC回复的`GetCompile`/`GetJob`同键恢复；它所用`fixture://`URI只是测试私有值，不能交正式任务。若进程在RTW已接纳后重启却失去原候选/Accept请求，本模块不伪造结果Ref或技术ACK，交后续持久候选收据/RTW可回读结果合同owner设计。

## 本机证据与等级

`TestWikiCompileWorkerNativeAcceptedBeforeOptionalTechnicalACK`在隔离子进程使用**真正**框架Runner/Graph/LLMAgent、固定模型、同一个任务专属Local对象Store给BTW写和RTW权威fixture读。测试先签默认nil CompletionRef的RTW Accepted/零DC Complete，再签explicit fixture Ref的RTW未知回复→GetCompile/同体Accept及DC未知回复→GetJob状态与结果Ref核对；DC在模型EOF后取消、模型伪造locator、技术Submit哈希坏行分别零对象Put/RTW Accept/技术ACK。原生`workflow execute_graph wiki_compile_candidate`的`trpc.agent.go` Scope与应用`content.wiki_compile.process`在同一Trace，JSON日志区分partial/rejected/succeeded，不以fmt直接输出阶段或把源码正文/模型口令写日志。

本机聚焦`GOMAXPROCS=2 go test -mod=readonly -race -p=1 -count=1 -run '^TestWikiCompileWorker' ./internal/app`退出0，父日志SHA=`bae2422befd5a9ef631613a291f79c712d19e590a27e85724963d95fb3abebff`；`go vet -mod=readonly ./internal/app`退出0。提供方真RTW×DC隔离PG16冻结的无令牌票据JSONL内容SHA=`ca8a69439c18fbe921277da38951f5e0101b7f48e632966e0c54ba28c1601298`，本仓**只读**在其原Claim lease窗口重播十二键/八字段/完整Submit JCS且race同轮通过；它不是当前在线租约证明，也没有重跑该跨仓PG。本机可用盘约12Gi时暂停大PG/CH/race套验，保留已有证据与停止实例。

签收仅**应用Interface/来源拒错/原生Runner与对象同Store夹具/业务先于技术提交的L1**。正式RTW Outbox原事件字节私有读、RTW/DC真同轮Claim→实际Wiki候选原字节、DC成功ResultRef合同及持续恢复账本、正式S3同桶权限/生产对象读回、DC native模型app_user会话/有效配置/真实Provider请求和`cmd/worker`默认关闭装配门禁均欠签。RTW接纳WikiRevision也不自动建立Release/发布或改人工修订。WS06-A及47项仍`PARTIAL`。
