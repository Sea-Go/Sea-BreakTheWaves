# BTW 消费 RTW 固定证据与引用收据：局部验收

日期：2026-09-14。固定输入：RTW 知识 API/生成类型提交 `cb7d29e`；BTW 基线 `77e6029`。本切片只写 `internal/clients/ridethewind` 的生成器 allowlist、生成 wire/lock 与窄客户端，以及 `internal/app/RTWSearchCitationAdapter`/测试。不修改 RTW 提供者、三路算法、搜索Graph或产品 API。

`generate.py` 从固定 RTW `api/knowledge.api` 对应 goctl `types.go` 重生 `wire.gen.go/source.lock.json`，加入 `CitationChunk/Location/Object`、固定原文读取请求、引用接纳/回查 DTO；不手改生成物。共享 `httpclient` 继续使用固定 RTW BaseURL、Bearer、W3C traceparent、大小限制与禁止重定向。新增客户端方法 `ReadSearchSource`、`AcceptSearchCitations`、`GetSearchCitations` 在HTTP成功后核对数据身份，不将状态码200直接解释成领域提交。

`RTWSearchCitationAdapter` 同时满足搜索 `SourceReader` 与 `CitationAcceptor`：按module/release/generation/十进制publication revision/revision/chunk请求RTW同版原文，返回前将所有片段元数据与索引候选逐项对照并重算quote hash；把原 `json.Marshal(EvidencePack)` 字节和SHA256作为RTW接纳输入，只在匹配search_id/pack_hash/durable_ref的收据后返给搜索。若POST响应在RTW已提交后丢失，调用方可带**原固定pack hash**用`Recover(search_id, expected_hash)`查同一耐久收据；错误hash不获恢复。

`TestRTWSearchCitationAdapterHTTPReceiptRecovery` 使用真实 BTW 生成客户端对隔离 HTTP 契约fixture调用，验证原文读取/接纳同一Trace的W3C传播、POST已提交却返回503时不公开引用、GET按原hash恢复、错误hash拒收、正确引用包交付、RTW伪造正文在接纳前拒收。`go test -mod=readonly -race -count=1 ./internal/app ./internal/clients/ridethewind`、`go vet`与根模块回归均通过。RTW提供者自己的`service/knowledge/scripts/acceptance.sh`已在独立仓真实PG16/HTTP证明发布/撤回/引用事务，但本切片**尚未在同一进程链同时运行真实RTW服务与BTW客户端**。

状态：提供者与消费者各自 `LOCAL_VERIFIED`，H07双方真实联调、实际三路同代索引、RTW `AcceptedRootHistory`、正式BTW API/SSE和Collector→DataCenter仍 `NOT_VERIFIED`。不能以生成DTO、fixture的200或当前Trace作为整个H07接纳。
