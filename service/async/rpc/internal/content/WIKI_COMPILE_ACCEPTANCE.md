# Wiki编制原生Agent候选：局部交接

此切片从BTW内容开发集成`159ba72`的独立树实现一款**未接纳的**Wiki编制候选，锁定`tRPC-Agent-Go v1.8.1`。RTW现在可`CreateCompile/GetCompile/ClaimCompile/AcceptCompile`并在接纳后插入不可变Wiki修订；本仓生成的RTW客户端已有这些HTTP方法，正式`cmd/worker`却只有内容准备、索引构建与事实消费。当前代码只在`internal/content/wiki_compile_{contract,graph}.go`：业务输入与固定原文纯验证、原生GraphAgent→无Tool LLMAgent→候选验证节点；它不写BTW对象、不Claim/Accept RTW、不ACK DC或发布Release。产品、调度与worker交接见Sea-Docs《WS02-C与WS06-A-Wiki编制执行交接》。

## 一次受控运行

`WikiCompileInput`由下一应用层从同一RTW Compile票据和当前DC技术attempt构造：`compile_id,module_id,page_id,base_revision_id,source_revision_ids,guidance,input_hash,generation,attempt_id,lease_epoch,cancel_version`。每个SourceRevision由RTW固定ID读取正文并核原始ContentSHA；输入必须含全部且仅有选定source ID，当前局部夹具上限16修订/总正文64KiB。Wiki页的`page_id`是RTW产品实体，可为合法UTF-8中文名称；技术ID则按有限ASCII合同。框架`WikiCompileRunOption`复制输入后将它放入当前Runner Graph state，Prompt只提供固定RTW段落及引用`paragraph:N`/原来源hash、模块/页/指导。LLMAgent以当前调用方注入的DC配置模型跑一次、`Stream=false`/输出MaxTokens1024，Tools为空，不从旧Session混入非本轮正文。

模型只可提出`title,markdown,source_refs[{revision_id,locator}]`。服务器按原始JSON Token逐字面校键，重复/大小写/Unicode逃逸同义键、未知/多余键、假修订或越界段落在候选工件出现前拒；RTW原文仅CRLF→LF后按空白行分段、空白块跳过，**不trim非空段落的四空格代码缩进或行尾空格**，与RTW`validLocator/citationParagraph`一致。输出自身带固定Compile与DC执行锚、Markdown原字节及SHA、已核来源引用，`WikiCompileCandidateFromCompletion`再次核全部技术锚/hash/ref；Graph completion仍须等Runner完整EOF且无终态错误，不能当RTW业务接纳。人工编辑与Release手动发布均留在RTW。

## 红绿和限定验收

独立测试writer对真实框架Runner/Graph/LLMAgent/Session和固定模型fixture运行：有效两份来源得到一个Graph完成/一个Runner完成，模型one call、Tools0、固定UID/逻辑scope不变；`trpc.agent.go`原生Agent、Graph、两函数节点Span在同Trace。假来源hash/引用、重复/未知JSON、大小写/Unicode键变形、伪造Graph完成、坏模型输出和取消均没有可交付候选。首轮合法三键JSON因`json.Decoder.InputOffset()`在第二键前含结构逗号被字面scanner错误拒，race红日志SHA=`078b5e0803abad47e2e83a31a85e08ca533508dbd08e08452c8cec7c64cf5647`；单笔`9bd8fe9`修后定向Wiki测试race0日志SHA=`eddadfd6bbebda8859c2aec301f629b94d574a3ad6879270da3a662c0e6b36c2`。

独立peer继而在RTW真实引用代码找到旧`sourceParagraphs`会把合法四空格Markdown代码块trim为普通文本的P2；`6b557b8`按RTW段落原字节规则修，测试writer以原CRLF bytes/SHA、前导四空格与尾空格/空白块建立真实Graph回归`81fcdc6`，定向native race日志SHA=`4e4c2d67a73666f30b5391928722fb05c2f960b001ea3f3ea97caf10639452c8`，全部Wiki测试raceSHA=`1ecf69256067eccafa6faf7774ed98658ffd207cb5ebd50fd430e804a24f5e43`。最终固定树`81fcdc62527ee228e7c9af2e98e01e6752e1f498`从**当前源码**再跑整包`GOMAXPROCS=2 go test -mod=readonly -p 1 -race -count=1 ./internal/content`退出0，日志SHA=`7db8d275be4c9685a5a49ced4c7b9e14ff81763613120879f356b7ef234c3d24`；vet/mod verify/diffcheck退出0，源码/测试独立只读peer在修后无剩余可操作P0–P3。

这是固定来源短文本与确定性模型夹具的**本机框架L1候选**，不表示RTW `GetRevision/ClaimCompile/AcceptCompile`已经在一轮调用、DC真实模型route/usage或Wiki编制作业已派发，也不表示候选对象原字节由RTW保存、新版Wiki可手动发布、浏览器或多轮学习对话已验。下一writer须保持一份实际RTW编制票据、独立技术job与candidate对象/RTW业务收据的执行权对账；RTW接纳成功后再给DC原job-local ACK，未知回复先按RTW GetCompile查原ID/结果hash，不重跑模型或改写已维护Wiki修订。WS06-A/H03及47任务总体仍`PARTIAL`。
