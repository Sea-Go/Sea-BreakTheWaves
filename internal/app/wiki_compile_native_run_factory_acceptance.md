# Wiki 编制官方模型 Factory 本机候选与装配交接

本切片从 BTW 集成固定头 `247030c96d153061cacfb0ce2a5fc9c7a99f4b68` 独立实现 `WikiCompileRunFactory`，只补齐 **每次 Compile 的官方模型/Graph/Runner/Session 构造**。正式 `cmd/worker` 仍默认没有 `wikiCompileStartDeps`，启用门禁会在 Claim 前拒绝；本切片没有替它配置 DC 模型账号、结果对象合同或生产调用。

## 工作区域

- `[W0:ROOT]` 本独立 BTW worktree 根 Go module。
- `[W1:WRITE]` 仅 `internal/app/wiki_compile_native_run_factory*.go`、局部测试与本交接。
- `[R1:READ_ONLY]` BTW `internal/content/wiki_compile*`、现有 Worker/`cmd/worker`、RTW Compile/GetRevision 与 DC Jobs/native Gateway 合同、Sea-Docs 既有计划。
- `[D1:DEPENDENCY]` `go.mod` 实际锁定的 `trpc.group/trpc-go/trpc-agent-go@v1.8.1` 与已存在的官方 OpenAI adapter；用 `go doc` 和对应版本源码核 GraphAgent、Runner、inmemory Session、请求级 NodeCallbacks。
- `[G1:GENERATED]` RTW/DC generated wire 只读；无生成文件手改。
- `[X1:EXTERNAL]` 测试使用回环官方模型 HTTP 夹具；真实 DC app_user Session/已配置 Wiki Chat CallPoint、RTW PG、共享对象存储和 Collector 未在本切片写入。
- `[N1:OUT_OF_SCOPE]` Worker、`cmd/worker`、RTW/DC/Docs、其它并行 worktree、业务主分支与生产配置。
- `[T1:TEMP]` `/private/tmp/sea-btw-wiki-native-*.log` 保留本机红绿证据，不进入 Git。

主职责 `[C5:AGENT_ADAPTER]` 为官方模型和 Graph 装配；跨 `[C2:APPLICATION]` 的 Worker Run/Proof/Close、`[C7:CONTRACT]` 的来源指纹及账号、`[C8:VERIFY]` 的原生 Span/Runner 完成。

## 最窄装配接口

```go
factory, err := app.NewWikiCompileNativeRunFactory(
    app.WikiCompileNativeRunFactoryConfig{DCGatewayRoot: dcGatewayRoot, HTTPClient: dcHTTP},
    nativeSessionProvider, // 仅 DC native app_user AccountID + wh_access_ bearer
)
// 同一 Runs 对象提供 CurrentNativeSession(ctx)；cmd 冻结其
// AccountID/NativeBearer/TokenDigest，且每次 Open 仍传入这份会话。
// cmd 创建自己的 telemetry Bundle，InstallGlobals 后唯一一次：
err = factory.BindTelemetry(bundle)
// 然后向 NewWikiCompileWorker 传入 Runs=factory、ModelSession=frozen。
// 入口停机先 factory.Close()，再 bundle.Close(ctx)。
```

`CurrentNativeSession` 仅向注入的 native provider 读取，不把原始 bearer 保存在 Factory 中；首次调用固定 `{AccountID,TokenDigest}`，不同合法账号 B 或同账号令牌换代都需新 worker/factory 实例。`BindTelemetry` 只允许账号已冻结、Bundle 已安装 framework Globals 时调用一次，双绑、错顺序、Bundle 已关闭均拒。Factory 借用 provider、HTTPClient 和 Bundle；每次 `Open` 自己建立独立的官方 Wiki CallPoint model、`content.NewWikiCompileGraphAgent`、`runtime.New` Runner 与私有 `inmemory.SessionService`。Factory/Run 的 `Close` 只关闭其拥有的 Runner/Session，等待本轮活跃调用；关闭后再 Open 在模型 HTTP 前拒。

`Open` 对 RTW 选定的 `SourceIDs[i] == Sources[i].RevisionID` **有序**重核，再使用 Content 的原正文 SHA 与 16 个修订/64 KiB 边界校验。它按该数组顺序将 `{revision_id,content_hash}` 交给 `runtime.WikiCompileSourceRefsSHA` 做真正 JCS：本机双源固定 sourceRefsSHA `ca62247e0a98fa486adc45c598162c692e219582da316ac8a9bab2a337f3f9c3`，`CompileID=compile-17`、原 InputHash、Generation7、`WikiDraft` 的模型键固定 `wiki-compile-03b39ad897b47c032a547603fc58b6396da18ef0dd3d0ad240f9559f1be4e7a0`。新的 DC attempt/lease 不进入模型幂等 Ref，来源换序或正文 hash 不符在网络前拒。官方 model 的 `NativeBearer` **只能取传入的已冻结 `session.NativeBearer()`**；`ModelSessionProof` 在 HTTP 前后均返回同会话的账号与 TokenDigest，jobs service token/RTW JWT 不作模型身份。

单次 `Run` 固定技术 SubjectRef/SessionID、来源 RunOption 和用户消息，忽略调用方提供的其它 Graph RunOption；Graph 仍以官方无 Tool、非流式、1024 输出 token 的一次 LLMAgent 请求运行。Factory 内部消费所有原生事件，确认唯一服务器校验的 Graph candidate、完整 Runner EOF、无终态错误且 Context 未取消后，**才向 Worker Sink 投递一次候选**。锁定版框架曾在缺 `finish_reason` 的模型回复后先发 Graph 候选 completion、随后于 EOF 报 `stream_error`；若在事件时就转交，Worker 会看见无效候选。现在坏终态、丢 HTTP 回复、Sink 异常或关闭中的 Sink 都不能使本轮 `Run` 成功，因而 Worker 的对象 Put/RTW Accept/DC Complete 不会由该结果触发。Worker 在接受前仍独立复核 DC job、RTW BUILDING 栅栏和引用/结果对象合同。

锁定版 `Runtime.Close` 在取消时可能早于 Graph 生产者最后的节点事件发送日志返回。Factory 使用公开、请求级 `graph.NodeCallbacks` 记录真实 Prompt/Agent/Validate 节点 Before/After；`Run.Close` 先取消并等 Runtime EOF，再等所有已进入的节点退出，最后关私有 Session。成功夹具至少 3 个节点开始/结束一一匹配；Factory.Close 若与已校验候选 Sink 并发，会等 Sink 结束且该轮返回失败。框架 **AfterNode 后的 post-send barrier 日志仍可能异步到达**，所以本机日志出口使用并发安全 Writer；正式入口的 `os.File` Writer 也支持并发。它还需要 Collector 同 Trace/结束时冲刷验收，不能把局部节点屏障称为所有框架尾部事件已同步落盘。

## 证据和跨仓下一验收

初始缺 Factory 红轮日志 `/private/tmp/sea-btw-wiki-native-factory-red.log` SHA256 `a6b7de52e3ff9a1463b5e474c11d02a453bc8749cd76ab58d7fbaba0c7b19238`；缺 finish_reason 候选先于 Runner 错误的红轮日志 SHA256 `e860b0bc9164bd992e4370712763f30a7684ab970e6ed46089825a416c33f1b7`；原不安全测试日志 Writer 与锁定版 Graph 尾部发送并发的 race 红轮日志 SHA256 `7efed9f37fefd3b438e41e73f294d0c10dd7d637a419b4cb1a505fd442e15c00`。最终源 `GOMAXPROCS=2 go test -p 1 -mod=readonly -race -count=1 ./internal/app ./internal/runtime` 两包退出 0，日志 `/private/tmp/sea-btw-wiki-native-factory-app-runtime-race-v2.log` SHA256 `7f2cfb9cdaf4336f244f5afee32cd80decc310c8d84a0d62ff3057594f841b16`；必要全模块 `go test -p 1 -mod=readonly -count=1 ./...` 43 包/0 FAIL，日志 `/private/tmp/sea-btw-wiki-native-factory-full.log` SHA256 `f8c0bc9ba82216b392d5e71a8532dd35a0798da61eb4502e13503907cbfc5892`；全仓 vet 空日志 SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`，mod verify 为 `all modules verified`，diff check 退出 0。夹具使用真实 `trpc-agent-go v1.8.1` Graph/Runner、官方 OpenAI SDK 回环 HTTP、原生 Agent/Chat/Graph 节点 Span；验冻结 A/B/token、Bind 顺序/双Bind/关闭、来源 JCS 键、第一次模型回复丢失而同逻辑新租约回放一条保存回复、错 `finish_reason`、模型 HTTP 中取消、关闭等 Sink 完成。这些只能签 **本机 L1**，没有真实 DC native app_user 活跃会话/已配置 `knowledge-wiki-compiler`、Provider token 成本收据、RTW 真修订与 Wiki 接纳或共享对象成功写入。

下一交接由 Worker/入口 owner 只在 `bundle.InstallGlobals()` 后、`NewWikiCompileWorker` 前调用这一个 `BindTelemetry(bundle)` port，并把同一个 Factory 配给 `Runs`；主任务再从真 RTW UserCenter/PG 来源和 DC Jobs holder 固定头，按同一租约测试真实 `GetRevision` → model → 对象 Put/readback → RTW Accept → DC ResultRef Complete。DC model owner 要先为 `knowledge-wiki-compiler` 配置有效候选并提供真实 native Session/account、幂等与用量收据；当前已登记空候选只会是 503。Collector owner 在同一次运行核 HTTP/Worker → native Graph/Chat → DC Gateway 的 Trace ID 和退出冲刷。正式 main 仍无这些依赖，产品链、生产启用和 47 项总体继续 `PARTIAL`。
