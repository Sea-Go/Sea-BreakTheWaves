# Wiki 编制模型 CallPoint 适配与交接

状态：**本机真实官方 SDK 请求 L1 PASS，DC 有效模型配置和全产品仍 NOT_VERIFIED**。
提供方为 BTW 内容固定基头 `447b556446f01615de19dc3649cbdb19650d2b67`
上独立的 `feat/wiki-compile-callpoint-model-20260915`。DC 参考固定头
`161d2184255ccdf8333b8953c761a34a68856c8a` 已登记
`knowledge-wiki-compiler` Chat CallPoint，但模型候选配置为空；登记
本身不能证明后台 Wiki 编制有可用模型。

## 写入边界与能力所有权

`[W0]` BTW 独立工作树；`[W1]` 只写 `internal/runtime/` 的模型适配、
测试及本页；`[R1]` 只读 DC `internal/llmgateway/`、
`internal/modelcontrol/`、BTW `internal/content/wiki_compile*`、
worker owner 的应用与入口；`[D1]` 锁定
`trpc-agent-go v1.8.1`、`openai-go v1.12.0`；
`[G1]` 无手改生成文件；`[X1]` 每轮新开的回环 HTTP 模型夹具；
`[N1]` DC/RTW/Docs、BTW 集成树、真实云端与其它服务；
`[T1]` 本机测试日志。主职责为
`[C5:AGENT_ADAPTER]`，跨 `[C7:CONTRACT]` 的模型调用账本与
`[C8:VERIFY]`。GraphAgent、Runner、Session 和 Wiki 候选验收由
`internal/content/`、`internal/app/` 的既有 owner 装配。

## 注入合同

worker 在读取 RTW 固定 Compile 后，按
`source_revision_ids` 的权威顺序调用
`GetRevision` 并核原内容 `ContentHash`，再构建：

```go
sourceSHA, err := runtime.WikiCompileSourceRefsSHA(
    []runtime.WikiCompileSourceIdentity{{RevisionID: revisionID, ContentHash: contentHash}})
modelRef := runtime.WikiCompileModelInvocationRef{
    CompileID: compileID, InputHash: inputHash, Generation: generation,
    SourceRefsSHA: sourceSHA, ModelStage: runtime.WikiCompileModelStageWikiDraft,
}
model, err := runtime.NewWikiCompileCallPointModel(
    runtime.WikiCompileCallPointModelConfig{
        BaseURL: dcGatewayRoot,
        NativeBearer: currentNativeAccessToken,
        CallPoint: runtime.WikiCompileCallPoint,
    }, modelRef)
```

`WikiCompileSourceRefsSHA` 对固定顺序
`[{revision_id,content_hash}]` 做真正的 JCS 变换再 SHA256；
JCS **只重排对象键为 `content_hash,revision_id`**，不重排
RTW 的数组顺序。双源固定 golden 为
`05ff2bbbe82f0503f5ced13557cd20f9b9019b640c906e22513b7d6a350e52d5`；
旧 Go struct 字段顺序直接 marshal 的
`3551dbc842fa54b29b29c9137b65529b15e1d706a90b92828e0d4e9fa61bf908`
明确拒作跨仓模型键。模型键若晚改会破坏已记录模型收据，
所以此 JCS 字节合同是 worker 与适配的源锁条件。

这里 `currentNativeAccessToken` 必须由 DC native app_user Session owner
当前签发，是原始 `wh_access_` + 32 字节规范 RawURL token；
构造器先拒技术 jobs service token、RTW JWT 或坏形状，DC Gateway
继续实时核 Session、用户 UUID、auth epoch 和 active。同一逻辑
Compile 的重试须保持同一个 DC app_user；access token 可由该账号
换代，不能切换另一用户空间再把相同文本幂等键当成原收据。模型候选未配置
时 DC 返回 503，本适配只送失败终态，不生成 Wiki 候选。工厂应为
**每逻辑 Compile** 创建一份 `model.Model`，不能让共享模型实例
携带下一个用户/Compile 的引用；同 CompileID/InputHash/Generation、
已核源修订 hash 和 `WikiDraft` 的租约/进程重试保持一个
`Idempotency-Key`，不把 DC attempt 编进模型幂等键。
同键新正文在同一实例的官方 SDK middleware、跨重启在 DC 持久幂等
记录前拒，不能偷偷得到另一份 Provider 结果。更换模型 Prompt 合同
若导致旧逻辑 Compile 正文变化，应给新 ModelStage/新业务修订，而
不能覆盖旧键。

适配复用官方 `model/openai` 请求与完整响应转换：
SDK 请求必须是 `POST /v1/chat/completions`、
`model=knowledge-wiki-compiler`，带**单值**
`X-Sea-Model-Callpoint: knowledge-wiki-compiler`、
`Idempotency-Key` 与 DC native `Authorization`。
Wiki Graph 原配置一轮无 Tools、非流式、输出 1024：
`model.Request.MaxTokens=1024` 经当前 SDK 发
`max_completion_tokens=1024`。DC 头
`X-Sea-Model-Output-Tokens: 1024` 与正文一致；
`X-Sea-Model-Input-Tokens` 及
`X-Sea-Model-Context-Tokens` 均按实际 SDK body 字节
`len(body)+512+64*message_count` 声明，正好覆盖 DC 当前
`estimateChatBudget` 的文本上界并满足 Context≤Input。
DC 对 `max_tokens` 和 `max_completion_tokens` 都认同一输出 cap；
Header 与任一实际正文 cap 不一致会在路由前拒绝。SDK
`WithMaxRetries(0)`、HTTP `CheckRedirect=ErrUseLastResponse`
与 POST `GetBody=nil` 阻止隐形重投/跨目标跳转；父 Context
取消到达 SDK 和 HTTP 请求。现有 `completeModel` 继续要求
完整非部分终态有 `finish_reason`，普通模型 delta/Graph
生命周期不在此重造。现有 OTel propagator 将 native Graph
Context 的 W3C `traceparent` 注入 DC 请求，不另建非框架 Agent Span，
也没有 `fmt.Print*` 阶段日志。

## 本机证据与未签项

当前 `internal/runtime` 全包 `go test -race -count=1`
通过。真实官方 adapter 对新开的回环 HTTP 模型夹具发出的
Header、`model`、`max_completion_tokens=1024` 和预算与
实际 body 字节逐项匹配。夹具先耐久保存一条模型响应却丢 HTTP
回复，SDK自动请求仍只有一次；GET 本机夹具收据后用原键显式
重放，Provider计数保持 1，实例重建也复用原键；同键异文在
网络/Provider 前报错。技术 token/RTW JWT、错 CallPoint、非法源
指纹、1023 token、Tools/非文本/流式在调用前拒；模拟 DC
空候选 503 没有候选，已到达 HTTP 的取消让 handler 退出，
307 Redirect 不跟随；固定 OTel TraceContext 的
`traceparent` 在夹具端与父 span 精确相同。
全包 race 父日志 `/private/tmp/sea-btw-wiki-runtime-optin-final-race.log`
SHA256 `640f09c07566ea448765814858d8f3d112c60abec39c3dbaabe9c0e1fc0e7817`；
五项新 Wiki adapter/JCS 行为均 PASS，真 DC opt-in 因没有
明确 runtime 文件按合同 SKIP；
当前 `internal/runtime` vet、`go mod verify` 与差异空白检查也退出 0。

这仍是**协议模型夹具 L1**：它没有 DC 的真实 native Session
或有效 CallPoint 候选，也没有真实提供商模型配置、UserRPC/
RTW Wiki Worker/Graph 的完整同轮产品链。DC 当前已登记的
`knowledge-wiki-compiler` 配置空候选会让真实 Gateway
`NotConfigured`，不能把回环夹具的 200 称为部署成功。
worker owner 后续交接需固定自身 HEAD 与本适配 HEAD，
验证 `WikiCompileRunFactory.Open(ctx,frozen input)` 注入实例
所用的 nativeBearer 来源、源修订读水位、ModelStage/SDK版本、
一条 Provider/收据/失败重放、真 DC Gateway 503→已配置→正常
链及 native tRPC Graph/Model/HTTP spans 同 Trace；真实配置启用
仍由 DC 模型控制面 owner 签收。

可选真 DC 模型边界测试
`TestWikiCallPointTrueDCGatewayCandidate` 缺
`SEA_WIKI_CALLPOINT_DC_RUNTIME_FILE` 时跳过；跨仓 owner
提供独立本机 Gateway 的私有 regular 0600 文件，仅含
`{base_url,native_bearer,run_id}`，且 `base_url` 必须是
`http://127.0.0.1:<port>` 根 URL。测试按新 `run_id`
生成一条未用过的固定 Compile/model key，自算 RTW 形状的
InputHash/JCS SourceRefsSHA，经本构造器和官方 adapter 只调用
**一次**真实 DC Wiki Chat 候选，并消费到有
`finish_reason` 的唯一非部分终态；文件内容、bearer 和模型
原文不进入测试输出。它只证明真实 DC native/CallPoint/
模型路由与响应，不能代替 RTW 源修订、Wiki Graph、
候选保存或产品发布联验。DC 当前空候选或 `ErrLogicalModel`
故障仍会失败；只有跨仓 owner 的 WS05A 模型控制门禁
配置与实际 Gateway 运行结果同时签收，才升级该边界。
