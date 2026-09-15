# WS09-F：正式搜索入口的原生 Agent Trace 协议验收

日期：2026-09-15。基线为 BTW `ca5f439`，本切片结论为 **`LOCAL_FIXTURE/PARTIAL`**。它补齐签名搜索身份、tRPC-Agent-Go 原生 Trace 与 OTLP/HTTP protobuf 边界的可复核合同；本机没有 Docker、Podman、`otelcol` 或 `tempo` 可执行文件，因此没有运行仓库中的正式 Collector/Tempo，也没有把测试 HTTP 服务称为 Collector。

## 落地合同

`search.http.summary` 在 RTW HMAC scope 校验成功后，才把 `operation_id`、`search_id`、`release_id`、`generation` 与 `publication_revision` 绑定到已经启动的应用 span。`telemetry.Stage.SetAttributes` 继续经过固定白名单，查询文本、模型内容、凭据和任意调用方字段不会进入 span。Tools 入口使用同一约束。

一次成功 summary 的固定链路为：

1. 应用 scope：`search.http.summary → runtime.run`；
2. tRPC-Agent-Go 原生 scope `trpc.agent.go`：`invoke_agent search_summary_root → workflow execute_graph search_summary_root`；
3. Graph 的直接子节点：`workflow execute_function_node search_and_accept`、`search_summary`、`validate_answer`；
4. 上述 7 个 span 使用同一 32 位 Trace ID，资源身份均为 `service.name=sea-btw-search-api`；
5. `search.http.summary.finished` 结构化 JSONL 的 `trace_id`、`search_id`、`operation_id`、`service`、`component`、`log_source` 与同次请求一致。

## 本地协议证据

`TestSignedSearchExportsQueryableNativeOTLP` 使用真实 `RootSessionBoundary`、Runner、GraphAgent、LLMAgent 与函数节点执行一条 HMAC 签名请求。进程 telemetry 通过正式 OTLP/HTTP exporter 发送 `application/x-protobuf`，本地接收夹具解码 `ExportTraceServiceRequest` 后核对服务、scope、关键 span 和父子关系。显式设置证据目录时，它写出 0600 权限的原始 protobuf、Tempo 形状 JSON、应用 JSONL 与边界报告，供 DataCenter 同轮读取。

```bash
SEA_BTW_OTLP_TRACE_EVIDENCE_DIR=/private/tmp/sea-ws09f-btw-otlp-evidence-final \
  go test -mod=readonly -race -count=1 \
  -run '^TestSignedSearchExportsQueryableNativeOTLP$' -v \
  ./internal/transport/http/search
```

报告固定写明 `evidence_level=LOCAL_FIXTURE`、`collector_runtime=not_run`、`trace_backend_runtime=not_run` 和 `otlp_receiver=in_process_http_protobuf_fixture`。因此本结果可以证明 BTW 生成了 DataCenter 查询端能消费的真实 OTLP protobuf，也能证明结构化日志与签名 SearchID 共用 Trace ID；它不能证明 Collector 已接收、重试或持久化，不能证明 Tempo/Jaeger 已可查询，也不能替代部署后的停止冲刷与 Loki 来源验收。

## 交接顺序

先合入本 BTW 分支并以其提交 SHA 重新生成证据，再合入 DataCenter WS09-F 分支。DataCenter 的 `btw.search.summary.v1` 投影依赖本文件规定的 `search_id` span 属性、服务名、原生 scope 和 7 个 span 名称。上线验收仍须从正式 Collector 的 OTLP 入口发起实际签名请求，再经正式 Tempo/Jaeger 和管理员 API 读取同一 Trace ID；缺任一运行证据时，WS09-F 保持 `PARTIAL`。
