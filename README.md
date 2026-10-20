<div align="center">
  <img src="./logo.png" alt="Sea-BreakTheWaves Logo" width="160" />


# Sea-BreakTheWaves

**基于 Go 构建的生成式推荐系统引擎**  
面向内容理解、候选召回、智能决策、用户记忆与可观测链路的一体化推荐服务工程实践。
</div>

---

## 当前工程入口

根 Go module 正在按 Sea 的最终职责划分实现内容与搜索运行基础，云端 Agent 采用 tRPC-Agent-Go。已实现部分及真实验收见以下入口：

| 目录 | 当前职责 | 说明 |
| --- | --- | --- |
| `service/search` | go-zero API/zrpc、tRPC-Agent-Go 搜索运行时 | [架构总览](docs/ARCHITECTURE_SEARCH_RECOMMEND_ASYNC.md) |
| `service/recommend` | go-zero API/zrpc、tRPC-Agent-Go 推荐运行时 | [架构总览](docs/ARCHITECTURE_SEARCH_RECOMMEND_ASYNC.md) |
| `service/async` | go-zero API/zrpc、Worker、用户模型与异步投影 | [架构总览](docs/ARCHITECTURE_SEARCH_RECOMMEND_ASYNC.md) |
| `service/common` | 共享检索算子（wholeindex/retrieval）、trpcagent 适配、遥测 | 各包内 README |
| `deploy` | 本地依赖编排和 Docker 配置 | [部署](deploy/README.md) |

这是持续实施中的工程：三路数值索引的真实模型编码与物理向量库接入、Wiki 编制 Agent 产品化、完整推荐闭环与生产部署尚未全部完成；当前交付以可集成组件、评测脚本与 CI 验收为准。

```sh
go test ./...
bash service/async/rpc/internal/content/test-postgres.sh
```

## 前言

由于一些原因，在原先开源的仓库中发生了诸如审核不严格导致的垃圾 PR、以及部分敏感信息泄露等问题。为了更好地控制项目质量和安全，我们决定将仓库迁移到一个新的地址，并重新整理了项目结构和文档说明。

项目资料与访问入口汇总见 Sea-Docs 仓库；前端与社区后端见 [Sea-RideTheWind](https://github.com/Sea-Go/Sea-RideTheWind) / [Sea-RideTheWind-Fronted](https://github.com/Sea-Go/Sea-RideTheWind-Fronted)。

---

## 核心能力

### 1. 搜索（service/search）

- **三路整篇召回**：Dense（余弦）/ Sparse（impact 内积）/ Multi-vector（token 矩阵 MaxSim）三条独立候选分支，RRF(k=60) 融合，互不替代
- **三档执行**：`fast`（dense+sparse 跳 multi）/ `balanced`（三路+RRF）/ `deep`（放宽预算），档位语义见 Sea 搜索方案
- **证据与交付**：命中 → EvidencePack（revision+locator 定位），summary（先搜后总结、引用角标）与 tools（结构化证据交调用方）两种交付
- **查询规划**：恒等规划器（fast）/ 规则规划器（balanced）/ 子查询分解器（deep），规则判档路由只升不降

### 2. 推荐（service/recommend）

- tRPC-Agent-Go 运行时承载的推荐 v2：候选池（长期/短期/周期）、召回、rerank、事件上报
- 用户记忆分层（长期/短期/周期/历史）与行为上下文读取
- 推荐完整闭环（真实曝光归因、双塔训练）在建设中，当前为规则/池化基线

### 3. 异步与用户模型（service/async）

- **indexer**：六接缝管线（EventSource/Encoder/Sink/ReceiptSink/SeenStore/StructureSource），内容寻址工件 + READY 最终提交点，失败零工件零回执可重试
- **worker**：领域事实接纳与异步投影
- **usermodel 工具**：schema 迁移与 SubjectRef 预检命令行

### 4. 可运行入口

| 入口 | 用途 |
| --- | --- |
| `service/search/rpc/cmd/search_api` | 最小 HTTP 服务：`POST /api/v1/search`（tier×delivery），C-3 契约 JSON |
| `service/search/rpc/cmd/search_demo` | 命令行演示：三档×双交付端到端（规划→检索→证据→总结） |
| `service/search/rpc/cmd/retr_eval` | 种子集评测：nDCG/R_cap/MRR，`testdata/index/seeds/` |
| `service/async/rpc/cmd/indexer` | JSONL 事件 → 工件 → READY 的 dev 构建入口 |
| `service/async/rpc/cmd/worker` | 异步事实 worker |
| `service/async/rpc/cmd/usermodel-schema` / `usermodel-subjectref-preflight` | 用户模型 schema 与迁移预检 |

### 5. 可观测性

- 结构化日志 + OTel Trace（含 tRPC-Agent-Go 原生 Root/Graph/Tool Span）
- Prometheus 指标（`/metrics`）、健康检查（`/health`）
- Grafana / Jaeger / Kafka UI 等本地栈见 `deploy/`

---

## 系统架构

```text
              ┌──────────────────────────┐
              │   Web / BFF / 桌宠客户端   │
              └────────────┬─────────────┘
                             │ HTTP (go-zero API)
        ┌────────────────────┼────────────────────┐
        ▼                    ▼                    ▼
 ┌─────────────┐     ┌──────────────┐     ┌─────────────┐
 │ search 服务  │     │ recommend 服务 │     │  async 服务  │
 │ API + zrpc  │     │  API + zrpc   │     │ API + worker │
 └──────┬──────┘     └──────┬───────┘     └──────┬──────┘
        │    tRPC-Agent-Go（Graph/Tool/Model）     │
        ├────────────────────┴────────────────────┤
        ▼                                         ▼
 ┌──────────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐
 │ DataCenter    │  │Postgres │  │ Milvus  │  │  Kafka  │
 │ 模型网关(C31) │  │ (GORM)  │  │(+Neo4j) │  │         │
 └──────────────┘  └─────────┘  └─────────┘  └─────────┘

   观测：OTel → Prometheus / Grafana / Jaeger
```

模型调用统一经 Sea-DataCenter 网关接入（DashScope/Ollama 等由 DC 控制面管理），本服务不直连供应商。

---

## 项目结构

```text
Sea-BreakTheWaves/
├── api/                          # go-zero .api 契约（search / recommend / async / admin）
├── proto/                        # RPC 协议定义
├── service/
│   ├── search/                   # 搜索域
│   │   ├── api/                  # go-zero HTTP API（etc/ 配置、internal/handler|logic|svc）
│   │   └── rpc/                  # zrpc + cmd/{search_api,search_demo,retr_eval}
│   │       └── internal/         # planner / retrieval / evidence / summary / pipeline 等
│   ├── recommend/                # 推荐域（api + rpc，trpcagent 运行时、rerank、skills）
│   ├── async/                    # 异步域（api + rpc，indexer / worker / usermodel / artifact / tree）
│   ├── common/                   # 共享库：wholeindex / retrieval / trpcagent / 遥测
│   └── boundary_test.go          # 守护测试：禁跨域 import
├── framework_wiring_test.go      # 守护测试：C17 框架装配边界（tRPC-Agent-Go）
├── schema_ownership_test.go      # 守护测试：schema 归域内 GORM owner
├── shared_symbol_test.go         # 守护测试：共享符号唯一实现、镜像不回潮
├── naming_test.go                # 守护测试：C45 唯一正名（目录/包名无代次后缀）
├── naming_allowlist.json         # 命名豁免白名单（path/reason/converge_event）
├── testdata/                     # 评测种子集与夹具（index/seeds/）
├── docs/                         # 架构总览、运行时、内容契约、遗留推荐说明
├── deploy/                       # docker-compose、Prometheus、OTel Collector
├── go.mod                        # module github.com/Sea-Go/Sea-BreakTheWaves
└── README.md
```

仓库根的四个守护测试把工程约束固化为可执行断言：框架装配必须真实 import tRPC-Agent-Go、跨域依赖禁止、共享算法符号唯一、目录与包名不带 v2/v3 代次后缀。

---

## 技术栈

- **语言与框架**：Go 1.25+，go-zero（API/RPC），tRPC-Agent-Go v1.10.0（Agent/Graph/Tool/Session）
- **存储与检索**：PostgreSQL（GORM）、Milvus（向量/混合检索）、Neo4j（可选投影）、Redis
- **消息与数据流**：Kafka
- **模型接入**：经 Sea-DataCenter 模型网关（OpenAI-compatible；本仓不内置供应商 key）
- **可观测**：OpenTelemetry、Prometheus、Grafana、Jaeger

---

## 运行环境要求

建议本地环境至少具备：

- Docker / Docker Compose
- Go 1.25+
- PostgreSQL（若不走容器则需自行准备）
- Milvus
- Kafka
- Neo4j（可选，失败时主链路可降级）
- 模型网关可达（Sea-DataCenter，或本地 Ollama）

---

## Go-zero 服务入口

每个域都由 go-zero API 和 zrpc 组成。先启动 RPC，再启动 API：

```sh
# Search
cd service/search/rpc && go run .
cd service/search/api && go run .

# Recommend
cd service/recommend/rpc && go run .
cd service/recommend/api && go run .

# Async RPC / API / Worker
cd service/async/rpc && go run .
cd service/async/api && go run .
cd service/async/rpc/cmd/worker && go run .
```

HTTP 与 zrpc 端口见 [架构总览](docs/ARCHITECTURE_SEARCH_RECOMMEND_ASYNC.md)。

## 快速开始

### 1. 克隆项目

```bash
git clone <your-repo-url>
cd Sea-BreakTheWaves
```

### 2. 准备配置

各服务配置位于对应 `etc/` 目录；入库的示例是 `sea.search.v1.yaml` / `sea.recommend.v1.yaml` / `sea.async.v1.yaml`，本地私有配置 `config.yaml` 已被 .gitignore 排除，按需从示例复制并填写 PostgreSQL、Milvus、Kafka、模型网关地址等。

### 3. 启动基础依赖

```bash
docker compose -f deploy/docker-compose.yaml up -d
```

### 4. 启动服务

按上节顺序启动各域 RPC 和 API；或先用命令行入口体验端到端检索：

```bash
echo "海洋观测" | go run ./service/search/rpc/cmd/search_demo --tier fast --delivery summary
```

### 5. 健康检查

```bash
curl http://localhost:20731/health
curl http://localhost:20721/health
curl http://localhost:20741/health
```

期望响应：

```json
{"status":"ok"}
```

---

## Docker 部署

项目提供了简易部署脚本：

```bash
sh service.sh
```

---

## API 说明

路由以 `api/*.api` 为唯一来源；以下为当前契约摘要。

### Search（prefix `/api/v1`，service/search/api）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/search` | 主搜索：query + tier（fast/balanced/deep）+ delivery（summary/tools） |
| POST | `/api/v1/search/title` | 按文章标题结构化检索（不依赖模型） |
| POST | `/api/v1/search/authors` | 按作者名结构化检索（不依赖模型） |
| GET | `/api/v1/search/tools` | 已注册搜索工具清单 |

### Recommend（prefix `/api/v2`，service/recommend/api）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v2/recommend` | 推荐主入口 |
| POST | `/api/v2/recommend/stream` | 流式推荐 |
| POST | `/api/v2/events` | 行为/曝光事件批量上报 |
| GET | `/api/v2/admin/skills` | 技能清单（管理） |

### Async（prefix `/api/v1/async`，service/async/api）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/async/events` | 领域事件批量摄取 |
| GET | `/api/v1/async/processing/state` | 处理水位 |
| GET | `/api/v1/async/projections/state` | 投影水位 |

### Admin（prefix `/api/v1/admin`，service/recommend/api）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/admin/reco/evaluation/summary` | 推荐评测摘要 |

示例——主搜索：

```http
POST /api/v1/search
Content-Type: application/json

{"query": "海边旅行", "tier": "balanced", "delivery": "summary"}
```

示例——标题检索（元数据匹配，无模型依赖）：

```http
POST /api/v1/search/title
Content-Type: application/json

{"query": "海边旅行"}
```

---

## 配置说明

配置按 go-zero 惯例集中在各服务 `etc/` 目录：

- `sea.*.v1.yaml`：入库示例（连接、端口、模型网关地址）
- `config.yaml`：本地私有配置（已 gitignore，密钥类字段只在此填写）

主要模块：日志、OTel、PostgreSQL（GORM DSN）、Milvus、Kafka、Neo4j（可选）、候选池策略、rerank 参数。

---

## 测试方式

```bash
go test ./...          # 全仓测试（CI 同口径，含 -race）
go vet ./...
```

- 检索链路手验：`go run ./service/search/rpc/cmd/search_demo --tier <档位> --delivery <交付>`
- 评测：`go run ./service/search/rpc/cmd/retr_eval`（种子集 `testdata/index/seeds/`）
- 仓库根守护测试：`go test -run 'Naming|Framework|Schema|SharedSymbol' .`

---

## 可观测体系入口

默认可访问的常用本地地址：

- Prometheus: `http://localhost:39090`
- Grafana: `http://localhost:33000`
- Jaeger: `http://localhost:16686`
- Kafka UI: `http://localhost:38080`
- Neo4j Browser: `http://localhost:37474`

---

## 典型应用场景

- 内容社区推荐与智能内容搜索
- 新闻/资讯分发与个性化 Feed
- 基于三路检索（稠密/稀疏/多向量）的知识问答底座
- 带用户记忆的个性化推荐系统

---

## 项目亮点

- **三路独立召回 + RRF 融合**：dense/sparse/multi-vector 各自可独立验收，不以单向量近似冒充
- **守护测试文化**：框架装配、跨域依赖、共享符号、命名代次四类工程约束全部固化为"删掉就变红"的测试
- **可运行端到端**：从命令行 demo 到 HTTP API，规划→检索→证据→总结全链可本地复现，跨平台确定性验证
- **模型调用收敛**：所有模型请求经统一网关，本仓零供应商凭据

---

## Roadmap

- [ ] 真实 BGE-M3 三路编码接入与 Milvus 物理索引（native-milvus）
- [ ] 三档策略/预算/默认档冻结（D12）
- [ ] 真实曝光归因与训练样本资格（先于任何双塔训练）
- [ ] Ontology 用户模型持久化与用户塔刷新
- [ ] 检索/推荐评测接入置信区间与基线对照

---

## 注意事项

1. Neo4j 初始化失败时主链路会降级，不阻断基础推荐流程。
2. 若模型网关不可用，推荐与检索中的语义能力将受影响；结构化检索（title/authors）不受影响。
3. 本仓不内置任何供应商 API Key；本地 `config.yaml` 由部署者自行填写且不入库。

---

## 未来规划

Sea-BreakTheWaves 未来将持续围绕"让 AI 在权限范围内具备一定自进化能力"这一方向展开探索：在可控、可追踪、可审计的前提下，根据环境反馈、用户行为和任务结果不断优化策略与执行效果。与此同时，项目也将持续探索智能推荐、内容理解、任务协同与知识辅助等方向，逐步形成一套兼具工程可落地性与业务扩展性的智能系统实践方案。

---

## 致谢

Sea-BreakTheWaves 面向生成式推荐场景，尝试把三路检索、用户记忆、推荐策略、模型重排与可观测体系整合成一个清晰、可落地、可迭代的工程框架。

如果你正在构建下一代内容推荐系统，希望这套工程结构能为你提供一个良好的起点。
