# RTW 签发的 BTW 搜索 API

`go run ./cmd/api` 是 `/v1/search/summary` 的正式进程入口，目前**仅允许隔离环境中的 `local-exact` + `fast/low`**。它使用 RTW 的签名范围、当前人工发布状态、同版原文/耐久引用/已接纳答案 HTTP 合同，使用 DataCenter 的 typed representation API 为本地三路 exact 索引编码查询；有证据时由显式配置的 OpenAI 兼容模型经 tRPC-Agent-Go v1.8.1 单根 Graph/Runner 总结。非 `fast/low` 且未获 RTW 明确降级许可的请求返回不可用，不伪装成高层智能搜。

进程缺任何必填项或配置文件非法时退出，既不会启动监听，也不会提供测试模型/假索引。`GET /livez` 只表明进程存活；单独回环地址的 `GET /metrics` 暴露统一 OTel/Prometheus 指标。依赖的实际可用性在请求执行中检测，`/livez` 不证明 DC/RTW/模型、索引或 Collector 可用。SIGINT/SIGTERM 后先停止 HTTP，再关闭 Root Runner 与 telemetry。服务仅监听显式回环 IP；正式远程流量应由受控 RTW 代理在同机转发，当前不提供公网部署模式。

| 环境变量 | 合同 |
| --- | --- |
| `BTW_SEARCH_MODE=local-exact` | 唯一已装配模式；不启用 Milvus 生产集合 |
| `BTW_SEARCH_API_ADDR`, `BTW_SEARCH_METRICS_ADDR` | 不同的 `127.0.0.1:端口` 或 `[::1]:端口`，无隐式默认 |
| `BTW_SEARCH_SCOPE_KEY` | 与 RTW 签发者相同的独立 HMAC 原始字节，至少 32 字节；不入库、不打印 |
| `BTW_RTW_URL`, `BTW_RTW_TOKEN` | RTW Worker HTTP 与访问令牌，读取当前发布/原文并提交引用和答案 |
| `BTW_DC_URL`, `BTW_DC_TOKEN` | DataCenter typed representation HTTP 与令牌 |
| `BTW_SEARCH_REPRESENTATION_MAX_IN_FLIGHT` | 本进程共享模型表示调用并发上限，显式设为 1–32；单并发 BGE-M3 Provider 使用 `1`，避免三路并发查询返回 429。多实例的全局容量仍由 DataCenter/部署调度保证 |
| `BTW_SEARCH_MODEL_URL`, `BTW_SEARCH_MODEL_KEY`, `BTW_SEARCH_MODEL_NAME` | 真实 OpenAI 兼容模型 URL、令牌和固定模型名；建议 URL 指向 DC 网关的 `/v1` |
| `BTW_ARTIFACT_DIR` | 已有本地内容寻址索引工件目录，与构建 worker 共享；本进程绝不构建/发布索引 |
| `BTW_SEARCH_INDEX_FILE` | 与 `cmd/worker` 的三路 `indexSettings` 相同的严格 JSON：`dense`、`sparse`、`multivector`；document/query 配置需与已发布工件一致 |
| `BTW_SEARCH_POLICY_FILE` | 只含 `version` 与 `fast_low` 的严格 JSON，示例如下 |
| `BTW_SEARCH_MAX_QUOTE_RUNES` | 单次可交付引用的总原文字符预算，1–4096 |
| `BTW_SEARCH_HTTP_TIMEOUT` | RTW/DC 调用超时，1–95 秒；搜索本身另受 policy `wall_time` 控制 |
| `BTW_OTLP_TRACES_URL` | OTLP HTTP traces 完整 URL，例如 `http://127.0.0.1:4318/v1/traces` |
| `BTW_SERVICE_VERSION`, `BTW_ENVIRONMENT`, `BTW_INSTANCE_ID` | 40 位 Git SHA、`local`/`test`、实例 ID |

政策示例（需由部署者按实测容量冻结版本，不能把示例当产品默认）：

```json
{
  "version": "local-fast-low-v1",
  "fast_low": {
    "max_batches": 1,
    "max_subqueries": 1,
    "top_k_per_lane": 8,
    "max_evidence": 4,
    "wall_time": "10s"
  }
}
```

`fast/low` 只按原问题做一批固定查询。RTW 签发的 `AllowLowerIntelligence=true` 可把较高等级显式降到 low，并在内部结果保留请求等级和降级原因；本模式不承诺详搜、多轮规划、重排或高智能效果。它不提供独立 Tools HTTP 协议或公开 SSE。索引构建/发布的目录和权限保持在 Worker/RTW；进程只读取已有工件，只有 RTW 当前发布中仍有效的修订可形成引用，接纳收据后才可能返回答案。RTW/模型/Collector 的跨进程验收及生产 Milvus/对象存储仍需单独完成，见 [验收记录](ACCEPTANCE.md)。
