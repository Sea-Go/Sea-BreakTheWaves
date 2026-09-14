# BTW 消费 RTW 固定证据与引用收据：局部验收

日期：2026-09-14。固定输入：RTW 知识 API/生成类型提交 `cb7d29e`；BTW 基线 `77e6029`。本切片只写 `internal/clients/ridethewind` 的生成器 allowlist、生成 wire/lock 与窄客户端，以及 `internal/app/RTWSearchCitationAdapter`/测试。不修改 RTW 提供者、三路算法、搜索Graph或产品 API。

`generate.py` 从固定 RTW `api/knowledge.api` 对应 goctl `types.go` 重生 `wire.gen.go/source.lock.json`，加入 `CitationChunk/Location/Object`、固定原文读取请求、引用接纳/回查 DTO；不手改生成物。共享 `httpclient` 继续使用固定 RTW BaseURL、Bearer、W3C traceparent、大小限制与禁止重定向。新增客户端方法 `ReadSearchSource`、`AcceptSearchCitations`、`GetSearchCitations` 在HTTP成功后核对数据身份，不将状态码200直接解释成领域提交。

`RTWSearchCitationAdapter` 同时满足搜索 `SourceReader` 与 `CitationAcceptor`：按module/release/generation/十进制publication revision/revision/chunk请求RTW同版原文，返回前将所有片段元数据与索引候选逐项对照并重算quote hash；把原 `json.Marshal(EvidencePack)` 字节和SHA256作为RTW接纳输入，只在匹配search_id/pack_hash/durable_ref的收据后返给搜索。若POST响应在RTW已提交后丢失，调用方可带**原固定pack hash**用`Recover(search_id, expected_hash)`查同一耐久收据；错误hash不获恢复。

`TestRTWSearchCitationAdapterHTTPReceiptRecovery` 使用真实 BTW 生成客户端对隔离 HTTP 契约fixture调用，验证原文读取/接纳同一Trace的W3C传播、POST已提交却返回503时不公开引用、GET按原hash恢复、错误hash拒收、正确引用包交付、RTW伪造正文在接纳前拒收。`go test -mod=readonly -race -count=1 ./internal/app ./internal/clients/ridethewind`、`go vet`与根模块回归均通过。这是首轮消费者局部测试；后续与真实RTW进程的联验见下段。

后续两仓同进程链局部联验：在RTW集成树运行`SEA_BTW_CITATION_CONSUMER_ROOT=<固定BTW集成工作树> bash service/knowledge/scripts/acceptance.sh`，实际RTW go-zero HTTP进程和隔离PG16发布固定release/结构性三路IndexManifest，再启动BTW自身的`TestRTWRealProviderCitationAdapter`子进程。BTW生成客户端从该真实RTW实例读取固定chunk/quote，`Delivery`先校验原文再提交新search_id EvidencePack，GET回查同一RTW数据库耐久Ref；RTW原文读取/引用接纳JSON终态与BTW父Span共享W3C Trace ID，引用计数在原fixture与新search_id共两次唯一提交、重放不多计。完整RTW脚本含全仓知识服务race/vet退出码0；BTW子测试PASS。此处输入三路IndexManifest仍是合成结构fixture，不证明真实模型/三引擎数值链。

状态：H07**引用子合同**达到隔离环境`INTEGRATED`；实际三路同代索引、RTW权威Answer历史的BTW消费与正式搜索API/SSE、客户端、Collector→DataCenter仍`NOT_VERIFIED`。不能把同版引用成功替代整个H07或OBS接纳。
