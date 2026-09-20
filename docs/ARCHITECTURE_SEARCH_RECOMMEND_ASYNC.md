# Search / Recommend / Async 工程结构

目标结构只有一套业务服务目录。根目录不再保留 BTW 的 `internal/` 或 `cmd/`。
`agent_v3/internal` 是独立旅行 Agent 应用的内部包，不属于 BTW 搜广推服务结构。

## 顶层

```text
service/
  common/          跨服务技术设施、客户端、契约和共享检索算子
  search/          搜索 API、核心 Search、评测、验收工具、searchclient
  recommend/       推荐 API、核心 Recommend、推荐内部模型与客户端
  async/           内容同步、Worker、用户模型、事件账本、投影与治理
api/               对外 HTTP 契约源
proto/             内部 RPC 契约源
contracts/         JSON Schema 等非 Go 契约
```

## 服务内部

```text
service/search/
  api/                  Search API 进程
  internal/app/         RTW 搜索适配
  internal/search/      Search 核心与 tRPC Graph
  internal/transport/   HTTP transport
  internal/evaluation/  搜索评测
  internal/warehouse/   Search source 投影
  rpc/                  稳定 searchclient 与 tRPC 能力装配

service/recommend/
  api/                  Recommend API 进程
  internal/app/         RTW 推荐适配
  internal/recommend/   推荐核心
  rpc/                  稳定 recommendclient 与推荐运行时

service/async/
  api/                  Async 管理 API
  worker/               Async Worker 进程
  internal/app/         Worker 编排
  internal/content/     内容编译与索引账本
  internal/usermodel/   用户模型 owner
  internal/warehouse/   社区、收藏、特征基线、Wiki 质量投影
```

## 共享设施

```text
service/common/artifacts
service/common/clients
service/common/corpus
service/common/retrieval
service/common/runtime
service/common/telemetry
service/common/sourcecoverage
service/common/trpcagent
service/common/usermodelcontract
```

`service/common` 不依赖 Search、Recommend、Async 内部包。
`service/async` 不依赖 Search/Recommend 内部包。
Search 和 Recommend 不互相依赖内部包。
Recommend 通过 `service/common/usermodelcontract` 消费 Async 拥有的用户模型契约。

## tRPC-Agent-Go

`service/common/trpcagent` 提供框架 `model.Model`、Session、Memory、Telemetry 和 Event 适配。
Search/Recommend 的 `rpc/internal/trpcagent` 装配各自 Runner、Graph、Tool、Prompt、Plugin、Skill。
