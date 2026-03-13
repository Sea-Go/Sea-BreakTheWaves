<div align="center">
  <img src="./logo.png" alt="Sea-BreakTheWaves Logo" width="160" />


# Sea-BreakTheWaves

**基于 Go 构建的生成式推荐系统引擎**  
面向内容理解、候选召回、智能决策、用户记忆与可观测链路的一体化推荐服务工程实践。
</div>

---

## 前言
由于一些原因，在原先开源的仓库中发生了诸如审核不严格导致的垃圾 PR、以及部分敏感信息泄露等问题。为了更好地控制项目质量和安全，我们决定将仓库迁移到一个新的地址，并重新整理了项目结构和文档说明。

之后这个项目会在一段时间内进入持续维护的阶段，并大概在今年的暑假之前开启新的征程，届时欢迎大家继续关注和支持，也希望您通过进入下面的群聊加入我们。

项目资料与访问入口汇总

- 📄 [项目文档](https://my.feishu.cn/wiki/FWSkwcTKwiuCcGkzqwPcfx0Inuc?from=from_copylink )   密码：174w667#
- 👥QQ 交流群：750807478
- 🧩 [前端仓库](https://github.com/Sea-Go/Sea-RideTheWind-Fronted) / [后端仓库](https://github.com/Sea-Go/Sea-RideTheWind)
- 🚀 [在线体验](sea-ridethewindbreakthewaves.xyz)

## 项目简介

Sea-BreakTheWaves 是 Sea 系列中的推荐与智能决策核心模块，聚焦于把“生成式推荐能力”真正落地为一个可维护、可扩展、可观测的工程化系统。

它并不只关注“给出推荐结果”这一件事，而是把推荐系统拆解为一条完整链路：

- 文档/内容接入与切分
- 向量化与索引构建
- 粗召回与候选池维护
- 精排与结果生成
- 用户历史与长期/短期记忆管理
- 日志、指标、Trace 全链路观测

从项目代码结构看，Sea-BreakTheWaves 以 **RecoAgent** 和 **ContentSearchAgent** 为核心，对推荐、检索、记忆、入库、重排等能力进行模块化封装，并通过统一的 Skill Registry 将多个能力组件组织成一个可编排、可演进的推荐服务系统。

---

## 设计目标

Sea-BreakTheWaves 主要解决以下几类问题：

1. **推荐链路工程化**  
   将“召回、排序、推荐解释、曝光副作用、用户记忆维护”组织为稳定清晰的服务流程，而不是零散脚本或单点算法逻辑。

2. **生成式推荐落地**  
   在传统召回/排序链路之上，引入模型意图分析、语义理解和工具调用机制，使推荐结果具备更强的上下文理解能力。

3. **内容搜索与推荐分层**  
   同时提供推荐接口与内容搜索接口，分别服务于“主动分发”和“按需检索”两类场景。

4. **用户状态持续沉淀**  
   通过短期记忆、长期记忆、周期记忆与用户历史行为管理，增强推荐系统对用户偏好与上下文的感知能力。

5. **可观测与可运维**  
   接入 Prometheus、Jaeger、Grafana 等组件，帮助研发快速定位问题、评估链路质量并持续优化。

---

## 核心能力

### 1. 文档入库

系统提供文档入库能力，支持将内容进行切分、向量化并写入检索系统与关系型存储：

- 文本切分（chunk）
- Embedding 生成
- 写入 Milvus 向量库
- 写入 Postgres 元数据存储
- 可选写入 Neo4j，用于 GraphRAG / 图谱检索增强

对应接口：

```http
POST /api/v1/docs/ingest
```

---

### 2. 推荐引擎

推荐主流程由 `RecoAgent` 负责统筹，典型链路包括：

- 用户请求解析
- 意图识别（intent.parse）
- 推荐策略路由（policy.route）
- 用户记忆与行为上下文读取
- 候选池补充与召回
- LLM / rerank 精排
- 结果校验与输出
- 曝光/副作用处理

对应接口：

```http
POST /api/v1/reco/recommend
```

推荐链路适合用于：

- 首页 Feed 推荐
- 热点内容推荐
- 个性化内容分发
- 周期性主题推荐
- 基于用户兴趣与历史行为的智能推荐

---

### 3. 内容搜索

除了推荐能力，系统还实现了独立的内容搜索 Agent，用于完成“问题 → 检索意图 → 向量召回 → rerank → 返回内容结果”的流程。

对应接口：

```http
POST /api/v1/search
```

典型特点：

- 不依赖用户记忆
- 支持 query 意图分析
- 支持 embedding 召回
- 支持 rerank 精排
- 返回文章、片段、标题、标签等结构化结果

这使 Sea-BreakTheWaves 不只是推荐系统，也是一套可直接承载智能内容检索的服务底座。

---

### 4. 技能化架构

系统使用 `skillsys` 管理可调用能力，将业务能力按 skill 拆分并统一注册。

当前已注册的核心 skills 包括：

- `doc_ingest`：文档入库
- `milvus_search`：向量搜索
- `pool_manage`：候选池维护
- `user_history`：用户历史行为管理
- `memory_manage`：用户记忆读写与维护
- `rerank`：重排序能力

这种结构有几个好处：

- 能力边界清晰
- 易于扩展和替换
- 方便 Agent 编排调用
- 适合持续演进为更复杂的多工具推荐系统

---

### 5. 用户记忆与行为管理

项目将用户状态建模为多层次结构：

- **长期记忆（long term）**：稳定偏好、长期兴趣
- **短期记忆（short term）**：近期上下文与即时兴趣
- **周期记忆（periodic）**：按时间桶维护的周期性偏好
- **用户历史（history）**：曝光、行为与交互轨迹

系统还会将记忆内容拆分为 Memory Chunks，并支持基于向量检索的相关片段召回，以避免把整段记忆直接塞进 Prompt，提升效率与质量。

---

### 6. 可观测性

Sea-BreakTheWaves 在工程层面内置了完整的可观测能力：

- **日志**：Zap + Lumberjack
- **指标**：Prometheus
- **链路追踪**：OpenTelemetry + Jaeger
- **可视化**：Grafana

默认暴露：

```http
GET /metrics
GET /health
```

可用于：

- 推荐链路耗时分析
- Agent 调用质量观察
- 异常排查
- 服务健康状态检查
- 线上性能监控与容量评估

---

## 系统架构

```text
                ┌──────────────────────────────┐
                │         Client / App         │
                └──────────────┬───────────────┘
                               │
                               ▼
                    ┌────────────────────┐
                    │   Gin HTTP Server   │
                    └─────────┬──────────┘
                              │
          ┌───────────────────┼───────────────────┐
          ▼                   ▼                   ▼
 ┌────────────────┐  ┌────────────────┐  ┌────────────────┐
 │   RecoAgent    │  │ ContentSearch  │  │ Skill Registry │
 │ recommendation │  │     Agent      │  │   + Tools      │
 └───────┬────────┘  └────────┬───────┘  └───────┬────────┘
         │                    │                  │
         ├────────────┬───────┴───────┬──────────┤
         ▼            ▼               ▼          ▼
   ┌──────────┐ ┌──────────┐   ┌──────────┐ ┌──────────┐
   │ Postgres │ │  Milvus  │   │  Neo4j   │ │  Kafka   │
   └──────────┘ └──────────┘   └──────────┘ └──────────┘
         │                                         │
         ▼                                         ▼
   ┌──────────┐                             ┌──────────┐
   │  Memory  │                             │Article In│
   │ History  │                             │  Gest    │
   └──────────┘                             └──────────┘

         Observability: Prometheus / Jaeger / Grafana / Logs
```

---

## 项目结构

```text
Sea-BreakTheWaves/
├── agent/               # 推荐 Agent / 内容搜索 Agent
├── chunk/               # 文档切分与 chunk 相关逻辑
├── config/              # 配置加载
├── embedding/           # 向量 schema 与 embedding 服务
├── infra/               # Postgres / Milvus / Neo4j / OTel 等基础设施初始化
├── kafka/               # Kafka 消费能力
├── metrics/             # Prometheus 指标定义
├── middleware/          # Trace / 错误处理中间件
├── router/              # HTTP 路由
├── skills/              # 各类推荐技能实现
├── skillsys/            # Skill 注册与调用框架
├── storage/             # Postgres 存储层
├── type/                # 公共类型定义
├── zlog/                # 日志与 trace 封装
├── ChatTest/            # 命令行对话式测试
├── docker-compose.yaml  # 本地依赖环境编排
├── config.yaml.example  # 配置示例
├── dockerfile           # 镜像构建
└── main.go              # 服务启动入口
```

---

## 技术栈

### 后端框架

- Go 1.25+
- Gin

### 存储与检索

- PostgreSQL
- Milvus
- Neo4j
- Redis

### 消息与数据流

- Kafka

### 智能能力

- OpenAI Go SDK
- DashScope / Qwen Embedding
- Qwen Rerank

### 可观测组件

- Prometheus
- Grafana
- Jaeger
- OpenTelemetry
- Elasticsearch / Kibana

---

## 运行环境要求

建议本地环境至少具备：

- Docker / Docker Compose
- Go 1.25+
- PostgreSQL（若不走容器则需自行准备）
- Milvus
- Kafka
- Neo4j（可选，失败时主链路可降级）
- DashScope / 模型服务可用 API Key

---

## 快速开始

### 1. 克隆项目

```bash
git clone <your-repo-url>
cd Sea-BreakTheWaves
```

### 2. 准备配置

复制配置模板：

```bash
cp config.yaml.example config.yaml
```

然后至少修改以下关键配置：

- `postgres.dsn`
- `milvus.address`
- `ali.apikey`
- `Kafka.address`
- `neo4j.address`
- `services.httpPort`

---

### 3. 启动基础依赖

```bash
docker compose up -d
```

项目中的 `docker-compose.yaml` 已包含以下组件：

- etcd
- postgres
- redis
- neo4j
- kafka
- minio
- milvus
- elasticsearch
- kibana
- prometheus
- grafana
- jaeger
- node-exporter
- cadvisor

---

### 4. 启动服务

```bash
go run main.go
```

启动成功后，默认监听地址为：

```text
0.0.0.0:20721
```

---

### 5. 健康检查

```bash
curl http://localhost:20721/health
```

期望响应：

```json
{
  "status": "ok"
}
```

---

## Docker 部署

项目提供了简易部署脚本：

```bash
sh service.sh
```

该脚本会完成：

- 构建镜像
- 删除旧容器
- 以 `breakthewaves` 名称启动服务
- 暴露 `20721` 端口

---

## API 说明

### 1. 健康检查

```http
GET /health
```

---

### 2. 指标采集

```http
GET /metrics
```

---

### 3. 文档入库

```http
POST /api/v1/docs/ingest
Content-Type: application/json
```

示例：

```json
{
  "title": "海边旅行攻略",
  "content": "这是一篇关于海边旅行的内容……"
}
```

> 具体字段以 skill 的实际入参定义为准；如果你后续要，我可以继续帮你把每个接口的精确 JSON 入参补齐成 OpenAPI 风格。

---

### 4. 推荐接口

```http
POST /api/v1/reco/recommend
Content-Type: application/json
```

示例：

```json
{
  "user_id": "u_10001",
  "session_id": "s_abc123",
  "query": "我最近想看一些和海边、风景、治愈感有关的内容",
  "surface": "home_feed",
  "period_bucket": "d1",
  "explain": true
}
```

---

### 5. 内容搜索接口

```http
POST /api/v1/search
Content-Type: application/json
```

示例：

```json
{
  "query": "适合夏天海边旅行的内容",
  "topk": 10,
  "recall_k": 30,
  "explain": true
}
```

---

### 6. 查看已注册工具

```http
GET /api/v1/tools
```

---

## 配置说明

项目采用统一 YAML 配置，不依赖 `.env` 或 `getenv`。配置集中在 `config.yaml` 中。

主要配置模块包括：

- `log`：日志路径、日志级别、服务名
- `otel`：OpenTelemetry 配置
- `postgres`：数据库连接
- `milvus`：向量库配置
- `ali`：大模型、Embedding、Rerank 配置
- `Kafka`：消息队列配置
- `neo4j`：图数据库配置
- `services`：HTTP 服务端口配置
- `pools`：候选池策略配置
- `agent`：模型、温度、最大工具调用次数
- `split`：文档切分参数
- `ranking`：召回/排序参数

---

## 候选池策略

系统内置长期、短期、周期三类候选池策略：

- `long_term`
- `short_term`
- `periodic`
- `recommend`

相关参数包括：

- `min_size`
- `refill_size`
- `take_size`
- `remove_after_recommend`

这意味着推荐系统可以在“池化候选 + 动态补充 + 出池控制”的模式下运行，更适合实际推荐场景中的吞吐与稳定性要求。

---

## 可观测体系入口

默认可访问的常用本地地址：

- Prometheus: `http://localhost:39090`
- Grafana: `http://localhost:33000`
- Jaeger: `http://localhost:16686`
- Kibana: `http://localhost:35601`
- Kafka UI: `http://localhost:38080`
- Neo4j Browser: `http://localhost:37474`
- NeoDash: `http://localhost:35005`
- Redis Insight: `http://localhost:35540`

---

## 测试方式

项目提供了命令行对话式测试：

```bash
go run ./ChatTest/chat_cli.go
```

你可以在测试过程中观察：

- 工具调用顺序
- 工具调用入参/出参
- 最终回答结果
- 配合 Jaeger 查看 trace_id 对应链路

这对于调试推荐 Agent、理解工具编排过程非常有帮助。

---

## 典型应用场景

Sea-BreakTheWaves 适合以下方向：

- 内容社区推荐
- 新闻/资讯分发
- 智能内容搜索
- 用户兴趣驱动的 Feed 流系统
- 带记忆能力的个性化推荐系统
- 基于向量检索与 Rerank 的推荐中台

---

## 项目亮点

- **推荐与搜索双能力并存**：不仅能做推荐，也能做内容检索
- **技能化结构清晰**：能力拆分明确，便于维护与扩展
- **记忆系统完整**：长期、短期、周期记忆协同工作
- **可观测能力成熟**：从日志到 Trace 再到指标全覆盖
- **基础设施齐全**：Milvus、Kafka、Neo4j、Postgres 一体化支持
- **适合继续演进**：便于扩展为更复杂的 Agentic Recommendation System

---

## Roadmap

- [ ] 补充完整 OpenAPI / Swagger 文档
- [ ] 增加更细粒度的推荐解释能力
- [ ] 支持多模型路由与 A/B 实验
- [ ] 增强 GraphRAG 与图谱检索能力
- [ ] 完善推荐结果评估体系
- [ ] 增加更多场景化推荐策略模板
- [ ] 支持在线特征与实时流式召回

---

## Logo 使用说明

当前 README 已预留 logo 引用：

```html
<img src="./assets/logo.png" alt="Sea-BreakTheWaves Logo" width="160" />
```

你只需要在项目根目录下新增：

```text
assets/logo.png
```

即可直接显示。

如果你的 logo 文件名不是 `logo.png`，把 README 顶部的路径替换掉就行，例如：

```html
<img src="./static/logo.jpg" alt="Sea-BreakTheWaves Logo" width="160" />
```

---

## 注意事项

1. 当前压缩包中未发现现成的 logo 图片资源，因此 README 中使用了预留路径。
2. `config.yaml.example` 中的部分默认配置与 `docker-compose.yaml` 中的默认账号/库名可能需要你按实际环境对齐后再运行。
3. Neo4j 初始化失败时主链路会降级，不阻断基础推荐流程。
4. 若模型服务不可用，推荐与检索中的语义能力将受影响。

---

## 未来规划
Sea-BreakTheWaves 未来将持续围绕“让 AI 在权限范围内具备一定自进化能力”这一方向展开探索。我们希望系统不仅能够完成基础的推荐与决策任务，还能够在可控、可追踪、可审计的前提下，根据环境反馈、用户行为和任务结果不断优化自身策略与执行效果，逐步提升模型在真实业务场景中的适应能力。

在产品落地方向上，项目后续计划尝试接入更多实际使用场景，例如飞书机器人、QQ 机器人等，让 AI 能够以更自然的方式服务于团队协作、信息分发、内容推荐和日常辅助决策。通过与消息平台、协作工具的结合，Sea-BreakTheWaves 将进一步从“推荐系统能力模块”走向“可交互的智能应用底座”。

与此同时，项目也将持续探索更多 AI 应用场景，包括但不限于智能推荐、内容理解、任务协同、知识辅助、个性化服务等方向。我们希望通过不断扩展能力边界，验证 AI 在更多真实场景中的可用性与价值，逐步形成一套兼具工程可落地性与业务扩展性的智能系统实践方案。

---

## 致谢

Sea-BreakTheWaves 面向生成式推荐场景，尝试把向量检索、用户记忆、推荐策略、模型重排与可观测体系整合成一个清晰、可落地、可迭代的工程框架。

如果你正在构建下一代内容推荐系统，希望这套工程结构能为你提供一个良好的起点。