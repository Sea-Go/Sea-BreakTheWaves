# Go-zero Search / Recommend / Async 架构

本项目现在以 go-zero 作为唯一 HTTP/zrpc 服务框架。数据生产工程已迁至外部数据侧归档，本仓库只保留搜广推确定性服务代码和必要共享技术设施。

## 顶层

```text
api/                    HTTP API 契约源，goctl 生成 API 层
proto/                  gRPC/zrpc 契约源，goctl 生成 RPC 层
service/search/         搜索域
service/recommend/      推荐域
service/async/          异步与用户模型域
service/common/         无业务语义的共享技术设施
deploy/                 Docker Compose、Prometheus、OTel Collector
```

每个业务域采用标准 go-zero 结构：

```text
service/<domain>/
├── api/
│   ├── <domain>.go
│   ├── etc/<domain>-api.yaml
│   └── internal/
│       ├── config/
│       ├── handler/
│       ├── logic/
│       ├── svc/
│       └── types/
└── rpc/
    ├── sea.<domain>.v1.go
    ├── etc/
    ├── <domain>service/       # 生成 zrpc client
    ├── pb/
    └── internal/
        ├── config/
        ├── logic/
        ├── server/
        ├── svc/
        ├── model/
        └── trpcagent/         # Search/Recommend 的 tRPC-Agent-Go 能力
```

## 调用链

```text
HTTP request
  → go-zero handler
  → api/internal/logic
  → api/internal/svc 持有的 zrpc client
  → go-zero zrpc server
  → rpc/internal/logic
  → tRPC-Agent-Go Runner / Graph / Tool
  → rpc/internal/model / common client / storage
```

API 层不直接访问数据库，也不直接启动业务 runtime。跨服务同步调用只通过生成的 `<domain>service` client；异步边界通过 Async RPC 和 worker/mq 处理。

## PostgreSQL schema ownership

应用表结构不使用根目录 SQL migration。各业务 owner 在 `rpc/internal/model` 或域内 schema 文件中声明 GORM Model，并由 `service/common/database.AutoMigrate` 初始化：

- `service/async/rpc/internal/content/schema.go`
- `service/async/rpc/internal/usermodel/schema.go`
- `service/recommend/rpc/internal/model/schema.go`
- `service/common/infra/schema.go`

PostgreSQL trigger、视图和 trigram 索引属于数据库专用 guard/查询增强，在 GORM 表结构初始化后安装。tRPC-Agent-Go 管理的 Session schema 仍由框架生命周期负责，不纳入应用 GORM schema。

## 端口

| 服务 | HTTP | zrpc |
| --- | ---: | ---: |
| Search | 20731 | 30881 |
| Recommend | 20721 | 30882 |
| Async | 20741 | 30883 |

## 启动

```sh
# Search
cd service/search/rpc && go run .
cd service/search/api && go run .

# Recommend
cd service/recommend/rpc && go run .
cd service/recommend/api && go run .

# Async API / RPC / worker
cd service/async/rpc && go run .
cd service/async/api && go run .
cd service/async/rpc/cmd/worker && go run .
```

## tRPC-Agent-Go 边界

`service/common/trpcagent` 提供应用侧 GORM 之外的框架技术适配：

- `model.Model`
- Session 后端
- 只读 Memory
- Telemetry
- Runner Event 桥接

Search / Recommend 的 `rpc/internal/trpcagent` 分别拥有：

- Agent
- Runner
- Graph
- Tool
- Prompt
- Plugin
- Skill
- Event

tRPC-Agent-Go 自管理的 Postgres Session 存储继续由框架初始化，不纳入应用 GORM 管理范围。

## 依赖边界

```text
search/api        → search/rpc/searchservice
recommend/api     → recommend/rpc/recommendservice
async/api         → async/rpc/asyncservice
search/rpc        → common, 本域 internal
recommend/rpc     → common, 本域 internal
async/rpc         → common, 本域 internal
common            → 不依赖任何业务域
```

禁止 Search 与 Recommend 互相引用 internal；禁止 API 层直连数据库；禁止业务域复制 go-zero generated client 后手改生成文件。
