# Sea Recommendation

Sea 推荐系统后端当前正在进行一次**破坏式全量重构**：推荐主链路从旧 `RecoAgent`
切换为基于 `trpc-agent-go` 的 v2 推荐平台。新系统不再以旧
`/api/v1/reco/recommend`、旧推荐请求/响应、旧事件日志作为主线，而是以统一领域契约、
`trpc-agent-go StateGraph` 状态流、事件 Hook、成本统计和链路可视化为核心。

当前落地版本已经完成 v2 主链路的第一版闭环：

- 新推荐内核：`internal/recommendationv2`
- 新推荐接口：`POST /api/v2/recommend`
- 新事件接口：`POST /api/v2/events`
- 新流式调试接口：`POST /api/v2/recommend/stream`
- 新观测摘要接口：`GET /api/v2/admin/obs/summary`
- 新链路查询接口：`GET /api/v2/admin/obs/traces`
- 新 Skill 清单接口：`GET /api/v2/admin/skills`
- 新 Skill 目录加载：启动时读取 `skills/*/SKILL.md` 并纳入 v2 `SkillRegistry`
- 新 Prometheus 指标：`/metrics` 输出 `genrec_reco_v2_*`
- 旧推荐接口：`/api/v1/reco/recommend`、`/api/v1/reco/events` 已返回 `410 Gone`

## 1. 重构目标

### 一统

- **统一流程**：推荐主链路统一经过 `RecommendationRuntime`，由 `trpc-agent-go StateGraph` 执行。
- **统一用户对象**：统一使用 `UserIdentity` 表达租户、用户、匿名用户、会话、设备、频道和地区。
- **统一用户画像**：统一使用 `UserProfile` 与 `ProfileSnapshot` 表达静态、动态、行为、时间画像。
- **统一行为事件**：统一使用 `BehaviorEvent` 表达用户行为、推荐链路事件、Agent 事件和观测事件。
- **统一推荐请求/响应**：统一使用 `RecommendRequest` 与 `RecommendResponse`。
- **统一配置**：统一使用 `RecommendConfig` 控制路径、召回、排序、重排、成本和观测。
- **统一二开点**：通过 `ProfileProvider`、`ProfileUpdater`、`Hook`、`EventBus`、Provider、`SkillRegistry`、`ToolPolicy` 扩展，不改核心流程。

### 可视化

- **推荐指标**：请求量、路径分布、返回条数、延迟、兜底状态。
- **成本**：LLM 调用、Tool 调用、Token、向量查询、重排成本、估算金额。
- **链路**：每个推荐响应返回 `trace_id` 和 `steps`，流式接口输出 `run_started`、`step_finished`、`run_finished`。
- **汇总**：`/api/v2/admin/obs/summary` 输出内存级观测摘要，用于第一阶段商业化调试和看板接入。
- **Trace 查询**：`/api/v2/admin/obs/traces` 支持按 `trace_id`、`request_id`、`user_id`、`tenant_id`、`channel`、`path`、`skill` 查询近期推荐链路。
- **Prometheus**：`/metrics` 暴露 v2 请求、延迟、返回条数、成本、Token、调用次数、事件、Hook 失败和 Graph step 指标。
- **Skill 治理**：`/api/v2/admin/skills` 暴露默认和目录加载的商业化 Skill 清单、成本等级、权限和适用路径。

## 2. 当前架构

```text
HTTP API
  |
  |-- POST /api/v2/recommend
  |-- POST /api/v2/events
  |-- POST /api/v2/recommend/stream
  |-- GET  /api/v2/admin/obs/summary
  |-- GET  /api/v2/admin/obs/traces
  |-- GET  /api/v2/admin/skills
  |-- GET  /metrics
  |
RecommendationService
  |
RecommendationRuntime
  |
trpc-agent-go StateGraph / Executor
  |
  normalize_request
    -> load_user_profile
    -> route
    -> fast_recall / slow_recall / hybrid_recall
    -> fast_rank / slow_rank / hybrid_rank
    -> agent_rerank
    -> quality
    -> explain
    -> emit_events
    -> build_response
```

当前 `RecommendationRuntime` 已使用 `trpc.group/trpc-go/trpc-agent-go/graph.StateGraph`
编译推荐流程，并通过 `graph.NewExecutor` 执行。每个节点在执行前会经过
`SkillRegistry` 与 `ToolPolicy` 校验，执行后沉淀 `TraceStep`、成本、事件和 Prometheus 指标。
服务启动时会通过 `WithSkillDirectory("skills")` 读取 `skills/*/SKILL.md`，将声明式 Skill
加载到 v2 `SkillRegistry`；旧工具目录如果没有 `SKILL.md` 会被跳过，不影响 v2 启动。
每次推荐完成后，runtime 会把响应链路固化为内存 `TraceRecord`，用于第一阶段链路查询和调试。

仍需继续推进的是把部分默认 provider 内部的模拟逻辑替换为真实召回、排序、重排、
质量和解释实现；但主流程入口、状态流、策略治理和观测出口已经统一到 v2 内核。

## 3. 目录结构

```text
recommendation/
├── main.go                         # 服务启动入口，当前推荐主链路接入 v2 runtime
├── router/
│   ├── router.go                    # HTTP 路由，包含 v2 推荐接口和旧推荐接口废弃响应
│   ├── router_test.go               # v2 路由与旧接口 410 测试
│   └── response.go                  # 统一 HTTP 响应包装
├── internal/recommendationv2/
│   ├── types.go                     # v2 统一用户/画像/请求/响应/事件/成本/链路类型
│   ├── config.go                    # 默认配置与请求级配置合并
│   ├── events.go                    # EventBus、HookRegistry、Hook 扩展点
│   ├── skills.go                    # v2 SkillRegistry、SkillDefinition、ToolPolicy、Skill 目录加载
│   ├── runtime.go                   # RecommendationRuntime 与 trpc-agent-go StateGraph 管线
│   ├── service.go                   # HTTP 层调用的 RecommendationService
│   ├── obs.go                       # 内存观测汇总、Trace 查询和 Prometheus v2 指标上报
│   └── runtime_test.go              # runtime 行为测试
├── agent/                           # 非推荐主链路 Agent 能力；旧 RecoAgent 已删除
├── internal/agent/                  # 既有 12 Agent 骨架，后续逐步并入 v2 runtime
├── internal/domain/                 # 既有领域接口和模型；v2 已提供 WithDomain* 适配器
├── internal/recall/                 # 既有召回能力，后续作为 v2 recall Tool/Skill
├── internal/rank/                   # 既有排序能力，后续作为 v2 rank Tool/Skill
├── internal/rerank/                 # 既有重排能力，后续作为 v2 rerank Tool/Skill
├── internal/quality/                # 既有质量评估能力，后续作为 v2 quality Tool/Skill
├── internal/graph/                  # 既有 Neo4j/图谱能力，后续作为 v2 graph Tool/Skill
├── internal/obs/                    # 既有 OTel/Prometheus 观测能力
├── skills/                          # 既有声明式 Skill 目录
├── storage/                         # Postgres/Milvus/Neo4j 等仓储
├── config/                          # 配置加载
└── go.mod                           # 已引入 trpc-agent-go v1.10.0
```

## 4. v2 核心类型

### UserIdentity

统一用户身份对象：

```json
{
  "tenant_id": "tenant-a",
  "user_id": "u_10001",
  "anonymous_id": "anon_abc",
  "session_id": "s_123",
  "device_id": "ios_1",
  "channel": "home_feed",
  "locale": "zh-CN",
  "tags": ["vip", "travel"],
  "extension": {}
}
```

字段说明：

- `tenant_id`：租户 ID，用于商业化隔离。
- `user_id`：登录用户 ID。
- `anonymous_id`：匿名用户 ID，用于未登录推荐。
- `session_id`：会话 ID，用于会话画像和链路关联。
- `device_id`：设备 ID。
- `channel`：推荐频道。
- `locale`：地区/语言。
- `tags`：用户标签。
- `extension`：业务扩展字段。

### UserProfile / ProfileSnapshot

`UserProfile` 分为四层：

- `static`：静态画像，如注册属性、会员等级、行业标签。
- `dynamic`：动态画像，如当前频道、近期兴趣。
- `behavior`：行为画像，如点击、收藏、完读、负反馈聚合。
- `temporal`：时间画像，如长期兴趣、短期兴趣、会话兴趣、周期兴趣。

`ProfileSnapshot` 是一次推荐使用的画像快照，包含：

- `user`
- `profile`
- `snapshot_id`
- `created_at`

这保证推荐结果可以回溯到当时使用的画像版本。

### RecommendRequest

推荐请求的唯一入口结构：

```json
{
  "tenant_id": "tenant-a",
  "request_id": "rec_001",
  "scenario": "home_feed",
  "channel": "home_feed",
  "user": {
    "user_id": "u1",
    "channel": "home_feed"
  },
  "query": "",
  "context": {
    "profile_override": {
      "temporary_interest": "AI travel"
    }
  },
  "top_k": 10,
  "path_mode": "hybrid",
  "debug": true,
  "config": {
    "path_mode": "hybrid",
    "recall": {
      "top_k": 50,
      "sources": ["rule", "content", "cf", "graph", "channel"]
    },
    "rerank": {
      "model": "self",
      "top_n": 20,
      "budget": 0.01
    },
    "obs": {
      "return_steps": true,
      "return_explain": true
    }
  },
  "extension": {}
}
```

### RecommendResponse

推荐响应统一包含：

- `request_id`
- `trace_id`
- `path_taken`
- `items`
- `explanations`
- `cost`
- `metrics`
- `steps`
- `fallback`
- `extension`

示例：

```json
{
  "request_id": "rec_001",
  "trace_id": "trace_xxx",
  "path_taken": "hybrid",
  "items": [
    {
      "id": "graph_01",
      "article_id": "graph_01",
      "score": 1.09,
      "source": "graph",
      "rank": 1,
      "reason": "reranked by self",
      "features": {
        "recall_source": "graph",
        "quality_score": 0.85,
        "rerank_model": "self"
      }
    }
  ],
  "cost": {
    "tokens_in": 120,
    "tokens_out": 80,
    "llm_calls": 1,
    "tool_calls": 3,
    "vector_queries": 1,
    "rerank_cost": 0.01,
    "estimated_amount": 0.01
  },
  "metrics": {
    "latency_millis": 2,
    "path": "hybrid",
    "item_count": 10,
    "fallback": false
  }
}
```

### BehaviorEvent

统一行为事件模型：

```json
{
  "event_id": "evt_001",
  "event_type": "click",
  "request_id": "rec_001",
  "trace_id": "trace_xxx",
  "tenant_id": "tenant-a",
  "user": {
    "user_id": "u1"
  },
  "article_id": "graph_01",
  "rank": 1,
  "channel": "home_feed",
  "path_taken": "hybrid",
  "timestamp": "2026-07-02T12:00:00Z",
  "metadata": {}
}
```

事件类型分为四类：

- 用户行为：`impression`、`click`、`like`、`dislike`、`favorite`、`read_complete`、`search`
- 推荐链路：`request_started`、`path_routed`、`recall_done`、`rank_done`、`rerank_done`、`quality_done`、`request_finished`
- Agent 事件：`agent_started`、`llm_call`、`tool_call`、`skill_invoked`、`agent_finished`
- 观测事件：`cost_updated`、`trace_emitted`、`fallback_triggered`、`error`

## 5. 推荐路径

### fast

定位：低延迟、低成本、兜底路径。

当前行为：

- 执行 `recall`
- 执行 `rank`
- 跳过 `rerank` 的 LLM 成本模拟
- 返回 TopK

适用场景：

- 预算不足
- 简单推荐
- 兜底
- 高 QPS 频道

### slow

定位：复杂意图、高解释需求、高价值流量。

当前路由规则：

- `query` 非空时，`auto` 会路由到 `slow`
- `debug=true` 时，`auto` 会路由到 `slow`

当前行为：

- 执行召回、排序、重排、质量、解释
- 记录模拟 LLM 成本
- 返回链路步骤

后续计划：

- 替换 `rerank`、`quality`、`explain` 节点为真实 `LLMAgent`
- 引入 IntentAgent、RecallPlannerAgent、GraphReasonerAgent

### hybrid

定位：默认商业化推荐路径，平衡质量、解释和成本。

当前行为：

- fast 风格召回 TopN
- slow 风格重排 TopN
- 质量打分
- 解释生成
- 成本统计

默认配置中 `path_mode` 为 `auto`；无查询、预算充足的普通推荐会路由到 `hybrid`。

## 6. API

### POST /api/v2/recommend

推荐主接口。

请求：

```bash
curl -X POST http://localhost:8080/api/v2/recommend \
  -H 'Content-Type: application/json' \
  -d '{
    "tenant_id": "tenant-a",
    "user": {"user_id": "u1"},
    "top_k": 3,
    "path_mode": "hybrid",
    "debug": true
  }'
```

响应：

```json
{
  "code": 200,
  "msg": "ok",
  "data": {
    "request_id": "rec_xxx",
    "trace_id": "trace_xxx",
    "path_taken": "hybrid",
    "items": [],
    "cost": {},
    "metrics": {},
    "steps": []
  }
}
```

### POST /api/v2/events

行为事件批量上报。

```bash
curl -X POST http://localhost:8080/api/v2/events \
  -H 'Content-Type: application/json' \
  -d '{
    "events": [
      {
        "event_type": "click",
        "user": {"user_id": "u1"},
        "article_id": "graph_01",
        "rank": 1
      }
    ]
  }'
```

响应：

```json
{
  "code": 200,
  "msg": "success",
  "data": {
    "accepted": 1
  }
}
```

#### 原始活动数据自动分析

当 `activity_worker.enabled: true` 时，事件中的 `raw_event` 会在该请求内完整转发给远端 Qwen Worker；普通行为事件仍按原路径处理。Worker 返回标准化事件列表和分值，本地触发器按用户（优先 `user_id`，其次匿名、会话或设备标识）累计分值，并将是否应调用 Agent 返回给客户端。活动事件的外层 `user` 必须携带其中一种标识；缺失时返回 `skipped`，不会请求模型或累分。

```bash
curl -X POST http://localhost:8080/api/v2/events \
  -H 'Content-Type: application/json' \
  -d '{
    "events": [
      {
        "event_id": "activity-20260802-0001",
        "event_type": "activity.raw",
        "tenant_id": "tenant-a",
        "user": {"user_id": "u1"},
        "raw_event": {
          "source": "desktop-client",
          "records": [
            {"type": "window.focus", "app": "editor", "at_ms": 1785676800000}
          ],
          "original_metadata": {"keep_every_field": true}
        },
        "activity_context": {"goal": "finish current task"}
      }
    ]
  }'
```

返回的 `activity_analysis` 与输入事件一一对应：

```json
{
  "accepted": 1,
  "activity_analysis": [
    {
      "status": "analyzed",
      "request_id": "activity-20260802-0001",
      "events": [{"activity": "coding", "confidence": 0.86}],
      "score": 0.72,
      "accumulated_score": 1.08,
      "should_call_agent": true,
      "trigger_id": "activity_..."
    }
  ]
}
```

客户端只在 `should_call_agent=true` 时调用 Agent，并以 `trigger_id` 作为幂等键。请由客户端稳定生成并重试相同的 `event_id`：在进程内重复缓存窗口中，重复事件不会再次请求模型或重复累分。分析超时、鉴权失败或 Worker 不可用时，原行为事件仍会被接收，`activity_analysis[].status` 为 `failed`；可随后用相同 `event_id` 重试。累计分数和重复结果缓存当前为进程内状态，服务重启后会清空。

在本地、被忽略的 `config.yaml` 中配置 Worker 凭据；模板见 `config.yaml.example`。不要将 Bearer Token 提交到仓库。

### POST /api/v2/recommend/stream

AG-UI/SSE 风格流式调试接口。

当前事件：

- `run_started`
- `step_started`
- `step_finished`
- `run_finished`

```bash
curl -N -X POST http://localhost:8080/api/v2/recommend/stream \
  -H 'Content-Type: application/json' \
  -d '{
    "tenant_id": "tenant-a",
    "user": {"user_id": "u1"},
    "top_k": 3,
    "path_mode": "hybrid",
    "debug": true
  }'
```

### GET /api/v2/admin/obs/summary

内存级观测摘要。

```bash
curl http://localhost:8080/api/v2/admin/obs/summary
```

响应数据包含：

- `requests`
- `events_accepted`
- `events_rejected`
- `activity_analyzed`
- `activity_analysis_failures`
- `activity_agent_triggers`
- `by_path`
- `last_trace_id`
- `last_request_id`
- `total_cost`
- `average_latency_ms`
- `labels`

### GET /api/v2/admin/obs/traces

查询近期推荐链路。第一阶段使用 runtime 内存环形窗口保存最近 256 条 `TraceRecord`，
用于商业化调试台、AG-UI 调试流和运维排查；后续可替换为 Jaeger、Langfuse 或专用 Trace 存储。

```bash
curl 'http://localhost:8080/api/v2/admin/obs/traces?user_id=u1&skill=recall.hybrid&limit=20'
```

支持查询参数：

- `trace_id`：精确查询某次推荐链路
- `request_id`：按请求 ID 查询
- `user_id`：按登录用户查询
- `tenant_id`：按租户查询
- `channel`：按频道查询
- `path`：按 `fast | slow | hybrid` 查询
- `skill`：按链路中出现的 Skill 查询，例如 `recall.hybrid`、`rerank.self`
- `limit`：返回条数，默认和上限均受内存窗口控制

响应数据包含：

- `trace_id`、`request_id`
- `tenant_id`、`user_id`、`anonymous_id`
- `channel`、`scenario`、`path`
- `started_at`、`finished_at`、`latency_ms`
- `steps`：Graph 节点执行明细
- `cost`：Token、Tool、LLM、向量、图谱和金额估算
- `skills`：本次链路命中的商业化 Skill
- `fallback`、`items`

### GET /api/v2/admin/skills

查询 v2 推荐内核当前注册的商业化 Skill 清单。

```bash
curl http://localhost:8080/api/v2/admin/skills
```

响应数据包含：

- `name`：Skill 名称，例如 `recall.hybrid`
- `category`：分类，例如 `recall`、`rank`、`rerank`、`quality`、`event`
- `cost_level`：成本等级，`low | medium | high`
- `permissions`：权限声明，例如 `read`、`write`、`llm`
- `paths`：适用路径，`fast | slow | hybrid`
- `online`：是否允许在线调用
- `write`：是否为写入型 Skill
- `extension.source`：当 Skill 来自 `skills/*/SKILL.md` 时记录 manifest 路径
- `extension.tools`：当 manifest 声明 tools 时记录其依赖工具

Skill 来源：

- 内置默认 Skill：保证 `RecommendationGraph` 必需节点始终可运行。
- 声明式目录 Skill：启动时由 `WithSkillDirectory("skills")` 加载 `skills/*/SKILL.md`。
- 旧工具目录：没有 `SKILL.md` 的历史工具目录会被跳过，不进入 v2 治理面。

默认内置和目录加载后必须覆盖的核心 Skill：

| Skill | 分类 | 成本 | 路径 |
| --- | --- | --- | --- |
| `profile.load` | profile | low | fast / slow / hybrid |
| `route.path` | graph | low | fast / slow / hybrid |
| `recall.hybrid` | recall | low | fast / slow / hybrid |
| `rank.traditional` | rank | low | fast / slow / hybrid |
| `agent.intent` | agent | medium | slow |
| `rerank.self` | rerank | medium | slow / hybrid |
| `quality.score` | quality | low | fast / slow / hybrid |
| `explain.recommend` | explain | medium | fast / slow / hybrid |
| `event.report` | event | low | fast / slow / hybrid |
| `rerank.self` | rerank | medium | slow / hybrid |
| `quality.score` | quality | low | fast / slow / hybrid |
| `explain.recommend` | explain | medium | fast / slow / hybrid |
| `event.report` | event | low | fast / slow / hybrid |

### GET /metrics

Prometheus 指标出口。v2 新增指标前缀为 `genrec_reco_v2_*`：

| 指标 | 含义 |
| --- | --- |
| `genrec_reco_v2_requests_total` | 推荐请求数，按 tenant/channel/scenario/path/status 分组 |
| `genrec_reco_v2_latency_seconds` | 端到端延迟直方图 |
| `genrec_reco_v2_returned_items` | 返回条数直方图 |
| `genrec_reco_v2_cost_amount_total` | 成本金额累计，含 estimated/rerank/cache_saved |
| `genrec_reco_v2_tokens_total` | token 累计，含 input/output/cached |
| `genrec_reco_v2_calls_total` | LLM/Tool/Graph/Vector 调用累计 |
| `genrec_reco_v2_events_total` | 行为和链路事件累计 |
| `genrec_reco_v2_hook_failures_total` | Hook 失败累计 |
| `genrec_reco_v2_trace_steps_total` | Graph 节点执行结果累计 |

### 旧推荐接口

以下接口已破坏式废弃：

| 方法 | 路径 | 状态 |
| --- | --- | --- |
| POST | `/api/v1/reco/recommend` | `410 Gone` |
| POST | `/api/v1/reco/events` | `410 Gone` |

响应说明会提示迁移到：

- `/api/v2/recommend`
- `/api/v2/events`

## 7. 配置模型

`RecommendConfig` 当前包含：

```text
RecommendConfig
├── path_mode
├── recall
│   ├── sources
│   ├── top_k
│   ├── weights
│   ├── timeout_millis
│   └── fallback_source
├── rank
│   ├── feature_weights
│   ├── model_version
│   └── ab_bucket
├── rerank
│   ├── model
│   ├── top_n
│   ├── budget
│   └── timeout_millis
├── cost
│   ├── max_tokens
│   ├── max_amount
│   ├── model_tier
│   └── cache_strategy
└── obs
    ├── trace_sample_rate
    ├── return_steps
    └── return_explain
```

默认配置：

- `path_mode`: `auto`
- `recall.sources`: `rule`、`content`、`cf`、`graph`、`channel`
- `recall.top_k`: `50`
- `rerank.model`: `self`
- `rerank.top_n`: `20`
- `cost.max_tokens`: `1200`
- `cost.max_amount`: `0.03`
- `obs.return_steps`: `true`
- `obs.return_explain`: `true`

配置合并规则：

```text
request.config > scenario config > tenant/channel config > global config > code default
```

当前实现：

- `DefaultConfig()` 提供 code default。
- `WithConfig(...)` 注入 global config。
- `WithConfigProvider(...)` 注入 tenant/channel 与 scenario 配置。
- `request.config` 覆盖上述配置。
- `request.path_mode` 最终覆盖所有配置路径。

## 8. 二开机制

### ProfileProvider

推荐前加载用户画像。

```go
type ProfileProvider interface {
    LoadProfile(ctx context.Context, user UserIdentity) (ProfileSnapshot, error)
}
```

默认实现是 `InMemoryProfileStore`，只生成内存画像快照。

业务方可以替换为：

- Postgres 画像
- Redis 画像
- 图谱画像
- 实时特征平台

### ProfileUpdater

行为事件后更新用户画像。

```go
type ProfileUpdater interface {
    UpdateProfile(ctx context.Context, event BehaviorEvent) error
}
```

可用于：

- 点击后强化兴趣
- dislike 后降低标签权重
- 完读后更新长期兴趣
- 搜索后更新短期意图

### Hook

事件处理扩展点。

```go
type Hook interface {
    Name() string
    OnEvent(ctx context.Context, event BehaviorEvent) (HookResult, error)
}
```

Hook 可以用于：

- 事件校验
- 审计
- 限流
- 指标上报
- 风控
- 训练样本沉淀
- 质量反馈闭环

Hook 返回 `HookReject` 时可拒绝事件。

Hook 支持两种模式：

- `RegisterBlocking`：同步 Hook，可拒绝请求或事件。
- `RegisterAsync`：异步 Hook，不阻断主链路，失败进入 `hook_failures` 和 Prometheus 指标。

### EventBus

`EventBus` 按注册顺序调用 Hook：

- Hook 报错会进入 rejected/failures 统计
- Hook 显式 reject 会拒绝事件
- 推荐链路内部事件通过 `publish` 发出

### Provider 扩展

推荐主链路节点本身不直接绑定业务实现，而是通过 provider 接口接入：

```go
type RecallProvider interface {
    Recall(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error)
}

type RankProvider interface {
    Rank(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error)
}

type RerankProvider interface {
    Rerank(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error)
}

type QualityProvider interface {
    Score(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, *FallbackReport, error)
}

type ExplainProvider interface {
    Explain(ctx context.Context, rctx RecommendationContext) ([]RecommendItem, []Explanation, error)
}
```

业务方可以通过：

- `WithRecallProvider`
- `WithRankProvider`
- `WithRerankProvider`
- `WithQualityProvider`
- `WithExplainProvider`

替换默认实现，不需要修改 `RecommendationGraph`。

### SkillRegistry

`SkillRegistry` 是 v2 推荐内核自己的 Skill 清单治理层，不依赖旧 `skillsys` 主注册路径。
每个 `SkillDefinition` 必须声明：

- 输入 / 输出 schema
- 成本等级
- 权限
- 适用路径
- 是否在线调用
- 是否写入型能力

### ToolPolicy

`ToolPolicy` 在每个 Graph 节点执行前强制校验：

- Skill 是否被禁用
- Skill 是否允许当前租户
- Skill 是否允许当前频道
- Skill 是否允许当前路径
- 高成本 Skill 是否被错误放到 fast 路径
- 当前预算是否允许高成本 Skill
- 写入型 Skill 是否声明 `write` 权限

默认策略：

- 高成本 Skill 禁止 fast 路径调用。
- 高成本 Skill 在低预算请求中拒绝。
- 写入型 Skill 必须显式声明 `write` 权限。
- `ToolPolicy` 拒绝会返回错误 fallback，并进入 trace/error 事件。

## 9. 可观测性

### 单请求链路

`RecommendResponse.steps` 会记录每个节点：

- `name`
- `type`
- `status`
- `started_at`
- `finished_at`
- `duration_millis`
- `cost`
- `error`

当前节点列表：

```text
normalize_request
load_user_profile
route
fast_recall / slow_recall / hybrid_recall
fast_rank / slow_rank / hybrid_rank
agent_rerank
quality
explain
emit_events
build_response
```

### 成本

`CostReport` 当前包含：

- `tokens_in`
- `tokens_out`
- `cached_tokens`
- `llm_calls`
- `tool_calls`
- `graph_queries`
- `vector_queries`
- `rerank_cost`
- `estimated_amount`
- `cache_saved_amount`

当前实现中：

- `recall` 增加 `tool_calls` 和 `vector_queries`
- `rank` 增加 `tool_calls`
- `slow/hybrid rerank` 增加 `llm_calls`、token 和 `rerank_cost`

### 汇总

`ObservationSummary` 当前包含：

- 请求总数
- 事件 accepted/rejected
- Hook failures
- path 分布
- 最新 trace/request
- 总成本
- 平均延迟
- 标签

Prometheus 当前已经接入 v2 runtime，覆盖请求、延迟、返回条数、成本、token、调用次数、
事件、Hook 失败和节点链路。OTel/Jaeger/Langfuse 深度链路仍是后续增强项。

## 10. 运行方式

### 安装依赖

```bash
cd /Users/edy/Sea/Sea-BreakTheWaves/recommendation
go mod tidy
```

### 运行测试

聚焦测试：

```bash
go test ./internal/recommendationv2 ./router
```

全量测试：

```bash
go test ./...
```

当前已验证：

```text
go test ./... 通过
```

### 启动服务

```bash
go run .
```

服务地址由 `config.yaml` 中的 `services.http_addr` 和 `services.http_port` 决定。

## 11. 当前完成状态

已完成：

- 新增 `internal/recommendationv2`
- 新增统一 v2 类型
- 新增 v2 默认配置和配置合并
- 新增 EventBus / HookRegistry
- 新增 ProfileProvider / ProfileUpdater 扩展点
- 新增真实 `trpc-agent-go StateGraph` 编排和 `graph.NewExecutor` 执行
- 新增 provider 扩展：召回、排序、重排、质量、解释
- 新增 SkillRegistry / SkillDefinition / ToolPolicy
- 新增节点执行前 Skill/Tool 策略校验
- 新增成本、链路步骤、观测摘要和 Prometheus v2 指标
- 新增 OTel root/node span 和内存 Trace 查询
- 新增 `WithDomainRecaller` / `WithDomainRanker` / `WithDomainReranker` / `WithDomainQualityJudger`
- 新增生产装配 `WithProductionProviders`，`main.go` 已将现有 `ArticleRepo` / `PoolRepo` 接入 v2 runtime
- 新增生产级基础链路：规则/频道混合召回、WeightedRanker、baseline rerank、静态质量过滤
- 新增 v2 HTTP API
- 新增 `/api/v2/admin/skills`
- 新增 `skills/*/SKILL.md` 目录加载
- 废弃旧推荐主接口和旧推荐事件接口
- `main.go` 不再实例化旧 `RecoAgent`
- `cmd/recrecorder` 已迁移到 `/api/v2/recommend`
- 删除旧 `agent.RecoAgent` 推荐主链路和旧灰度回退路由
- 引入 `trpc.group/trpc-go/trpc-agent-go v1.10.0`
- 新增 runtime/router 测试
- 全模块 `go test ./...` 通过

仍需推进：

- 将 baseline rerank、静态质量过滤和 explain 替换为真实 LLMAgent / 模型服务实现
- 将现有 `internal/rerank`、`internal/quality`、`internal/graph` 深度接入 v2 Tool/Skill
- 将 EventBus 接入 Kafka/异步 worker
- 将内存 Trace 窗口替换或补充为可查询持久化链路存储
- 将 Jaeger / Langfuse 深度链路接入 v2 runtime
- 建立 tenant/scenario/channel 级配置中心
- 建立 Grafana/Jaeger/Langfuse 商业化看板

## 12. 破坏式迁移说明

本次重构是破坏式迁移，不保证旧推荐接口兼容。

旧系统状态：

- `agent.RecoAgent` 推荐主链路代码已删除，不再作为参考实现或回滚路径。
- 旧 `/api/v1/reco/recommend` 不再可用，返回 `410 Gone`。
- 旧 `/api/v1/reco/events` 不再可用，返回 `410 Gone`。
- 非推荐能力暂时保留，例如文档入库、搜索、标题搜索、作者搜索、onboarding、metrics、health。

新系统要求：

- 新推荐能力必须经过 `RecommendationRuntime`
- 新推荐请求必须使用 `RecommendRequest`
- 新推荐响应必须使用 `RecommendResponse`
- 新行为事件必须使用 `BehaviorEvent`
- 新观测必须携带 `trace_id`
- 新扩展必须通过 ProfileProvider、ProfileUpdater、Hook、EventBus、Tool/Skill 接入
- 新在线能力必须声明 SkillDefinition 并通过 ToolPolicy 校验

## 13. 后续建议顺序

1. 将 baseline rerank provider 替换为现有 `internal/rerank` 自研/外部 rerank 服务。
2. 将静态质量过滤替换为现有 `internal/quality` Judge/Best-of-N。
3. 将 `explain` provider 接入真实 LLMAgent。
4. 将图谱召回、实体链接、Cypher 查询接入 v2 Tool/Skill。
5. 将 `EventBus` 接入 Kafka/异步 Hook worker。
6. 将 OTel / Jaeger / Langfuse 深度链路接入 v2 runtime，并替换内存 trace 窗口。
7. 建立租户/频道/场景级配置中心和商业化看板。

## 14. 快速验收清单

- `POST /api/v2/recommend` 返回 `request_id`、`trace_id`、`path_taken`、`items`、`cost`、`metrics`
- `debug=true` 或 `obs.return_steps=true` 时返回 `steps`
- `POST /api/v2/events` 返回 accepted/rejected
- `GET /api/v2/admin/obs/summary` 能看到请求数、路径分布和成本累计
- `GET /api/v2/admin/obs/traces` 能按 `trace_id`、`user_id`、`skill` 查询推荐链路
- `GET /api/v2/admin/skills` 能看到默认商业化 Skill 清单
- 服务启动时 `skills/*/SKILL.md` 会进入 v2 SkillRegistry，旧无 manifest 工具目录会被跳过
- 服务启动时 `WithProductionProviders(articleRepo, poolRepo)` 会把现有仓储接入 v2 推荐主链路
- `/metrics` 能看到 `genrec_reco_v2_*` 指标
- `POST /api/v2/recommend/stream` 输出 SSE 事件
- `POST /api/v1/reco/recommend` 返回 `410 Gone`
- `POST /api/v1/reco/events` 返回 `410 Gone`
- `go test ./...` 通过
