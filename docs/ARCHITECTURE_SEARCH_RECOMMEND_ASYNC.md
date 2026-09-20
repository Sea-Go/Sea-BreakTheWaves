# Search / Recommend / Async 工程结构

本文记录 2026-09-20 的服务拆分。目录以 go-zero 工程结构为骨架，Agent 运行能力按
`tRPC-Agent-Go v1.10.0` 公开能力落位。

## 业务边界

```text
service/search         搜索查询、Knowledge/RAG、证据、总结、搜索 Tools
service/recommend      推荐、召回、排序、重排、质量、解释、实验
service/async          内容同步、事件账本、画像投影、反馈归因、重试、死信
service/common         配置、连接、日志、指标、OTel、模型/Agent 技术适配
```

Search 和 Recommend 不互相引用内部包；Recommend 只能通过 `searchclient` 调用搜索候选。
Async 不引用 Search/Recommend 内部包，只通过 Kafka 事件向两个消费域发布投影变更。

## go-zero 调用链

```text
api/internal/handler
  -> api/internal/logic
  -> api/internal/svc
  -> rpc/<domain>client
  -> rpc/internal/logic
  -> rpc/internal/trpcagent/runner
  -> Agent / Graph / Tool
  -> rpc/internal/model / asyncclient / searchclient
```

## tRPC-Agent-Go 能力归属

| 能力 | 归属 |
| --- | --- |
| `model.Model` | `service/common/trpcagent/model`，适配 DataCenter 模型网关 |
| `Runner` | Search/Recommend 的 `rpc/internal/trpcagent/runner` |
| `Agent` / `Graph` | Search/Recommend 各自 `rpc/internal/trpcagent/{agent,graph}` |
| `Tool` | Search/Recommend 各自 `rpc/internal/trpcagent/tool` |
| `Knowledge/RAG` | Async 生产规范产物；Search 消费并组装检索链 |
| `Session` | common 技术后端 + Search/Recommend 服务内装配 |
| `Memory` | common 只读适配；写路径归 Async |
| `Event` | common Event 桥接；Runner 事件映射为 SSE/trace/cost/steps |
| `Planner` / `Prompt` / `Plugin` / `Skill` / `Telemetry` | Search/Recommend 域内目录 |

`rpc/internal/model` 是数据库模型；`service/common/trpcagent/model` 是框架 LLM
`model.Model` 适配。两者不可混用。

## 兼容

- Search API 保持在 `/api/v1/search`。
- Recommend API 保持在 `/api/v2/recommend`。
- `/api/v2/events` 保留为兼容入口，内部转发 Async。
- 原 `/api/v1/tools` 废弃，搜索工具在 `/api/v1/search/tools`。
- Article sync、重试和回执 topic 不变。
