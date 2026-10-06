# pipeline —— B 域端到端检索管线（装配层，dev 形态）

`service/search/rpc/internal/pipeline` 把 B 域五个已交付的域包串成
**端到端可运行的检索管线**（工程方案图 3 的 B2→B3→B5→B6 主链）：
一次 `Execute` 完成 query→plan→三档路由→search→summary/tools。本包
是**装配层**：域逻辑全部来自被串接的包，管线只做编排（档位路由、
编码接缝、交付分叉），不引入新的域规则。

## 端到端管线图

```
                       PipelineRequest{Query, Tier, Delivery}
                                     │
                   ┌─────────────────▼──────────────────┐
                   │ ① 入口校验（空查询/档位/交付形态）  │
                   └─────────────────┬──────────────────┘
                                     │
              ┌──────────────────────▼──────────────────────┐
              │ ② 判档合并（B2 planner.Router）             │   D-3 规则替身
              │    suggest = Router.Route(query)            │
              │    tier    = MaxTier(user, suggest)  只升不降│
              └──────────────────────┬──────────────────────┘
                                     │
              ┌──────────────────────▼──────────────────────┐
              │ ③ 规划（B2，按档位路由规划器）               │
              │    fast     → planner.Identity   （0 次）   │
              │    balanced → Planner（注入，Rule）（1 次） │
              │    deep     → Decomposer{Delegate: Planner} │
              │               （K 轮×1 次，K=len(subqueries)）│
              │    出口跑 Plan.Validate                      │
              └──────────────────────┬──────────────────────┘
                                     │ Plan.RewrittenQuery（防漂移那一路）
              ┌──────────────────────▼──────────────────────┐
              │ ④ 查询编码（Encoder 接缝；dev=FakeEncoder）  │   DC rep. 替身
              │    query_id = q-hex(sha256(query))[:12]     │
              └──────────────────────┬──────────────────────┘
                                     │ retrieval.Request{三件套}
              ┌──────────────────────▼──────────────────────┐
              │ ⑤ 检索（B3 Searcher.Search）                │
              │    fast 两路 / balanced·deep 三路 + RRF     │
              │    deep 双倍路内候选（放宽模拟）             │
              └──────────────────────┬──────────────────────┘
                                     │ evidence.DocHit
              ┌──────────────────────▼──────────────────────┐
              │ ⑥ 证据组装（B5 evidence.BuildPack）          │
              │    → EvidencePack（排序 + 整包校验）         │
              └───────────┬─────────────────────┬───────────┘
                          │ tools               │ summary
                          ▼                     ▼
                 PipelineResult{Pack}   ┌──────────────────────────┐
                 （B5 直返 B1，          │ ⑦ B6 Summarizer.Summarize │
                    绝不调 Summarizer）  │    + FormatAnswer         │
                                         │    → 答案+[n] 角标+引用   │
                                         └──────────┬───────────────┘
                                                    ▼
                                    PipelineResult{Answer,
                                      FormattedAnswer, Citations, Pack}
```

## API

| 入口 | 说明 |
| --- | --- |
| `Pipeline` | 装配结构：注入 `Planner`/`Searcher`/`Summarizer`（+`Encoder`/`Router` 接缝）；构造后只读、并发安全 |
| `Execute(ctx, PipelineRequest)` | 一步到位执行上图 ①—⑦；任一阶段失败即整体失败，绝不产出部分结果 |
| `PipelineRequest{Query, Tier, Delivery}` | 查询文本 + 用户显式档位（下限，判档只升不降）+ 交付形态（summary/tools） |
| `PipelineResult` | summary 交付填 `{Answer, FormattedAnswer, Citations, Pack}`；tools 交付只填 `Pack` |
| `NewDefaultPipeline(store)` | 默认装配：`planner.Rule` + `retrieval.NewSearcher` + `summary.NewStub` + `FakeEncoder` |
| `QueryEncoder` / `FakeEncoder` | 查询编码接缝（encoder.go）：查询文本 → 三路表示；dev 为 fakerepr 假编码的查询侧 |

错误契约（`errors.Is` 可判别）：空查询 → `planner.ErrEmptyQuery`；
未知档位 → `planner.ErrInvalidTier`；未知交付 → `pipeline.ErrInvalidDelivery`；
ctx 取消 → `context.Canceled`；下游错误原样包裹（保留各域哨兵）。

## 组件注入表

| 字段 | 类型 | 服务阶段 | dev 默认（NewDefaultPipeline） | 真实化 |
| --- | --- | --- | --- | --- |
| `Planner` | `planner.Planner` | ③ balanced 档规划 + deep 档 Delegate（子计划按 balanced 口径递归） | `planner.Rule{}` | LLM 规划器（query2doc/step-back/self-ask） |
| `Searcher` | `*retrieval.Searcher` | ⑤⑥ 三路召回+RRF+证据组装 | `retrieval.NewSearcher(store)`（TopN=50） | Milvus 等真实引擎（`service/common/retrieval`） |
| `Summarizer` | `summary.Summarizer` | ⑦ summary 交付（tools 不调，允许 nil） | `summary.NewStub()` | 经 D1 dc-gateway 的模型摘要 |
| `Encoder` | `pipeline.QueryEncoder` | ④ 查询文本→三路表示（nil 回退 FakeEncoder） | `FakeEncoder{}` | DC representation 查询编码器（同 encoder_id） |
| `Router` | `*planner.Router` | ② 判档建议（nil 回退零值） | `planner.NewRouter()` | Adaptive-RAG 式小 LM 分类器 |

fast 档的恒等规划器与 deep 档的分解器由管线按有效档位**内部装配**
（`plannerFor`）：注入的 `Planner` 必须能服务 balanced 档——这正是
B2 README 预告的"B1 网关按 MaxTier(请求档, Route建议档) 选规划器"
接线落点。

## 与 B 域进程的映射

| B 域里程碑 | 包 | 管线中的落点 |
| --- | --- | --- |
| B1 网关（档位合并/交付分叉） | ——（本包承接其 B 域侧接线） | ①② 的 MaxTier 合并与 ⑦ 的 tools/summary 分叉 |
| B2 查询规划与判档 | `internal/planner` | ② Router.Route + ③ Identity/Rule/Decomposer |
| B3 检索执行 | `internal/retrieval` | ⑤ Searcher.Search（三路+RRF+档位矩阵） |
| B5 证据组装 | `internal/evidence` | ⑥ BuildPack（Search 内部调用的下游） |
| B6 摘要交付 | `internal/summary` | ⑦ Summarize + FormatAnswer/RenderCitations |
| B4 精排（CE 第 4 路/RankGPT） | **不在本管线** | 见下"边界" |
| dev 种子集装载 | `internal/devseed` + `internal/fakerepr` | cmd/search_demo 与本包测试的 Store 来源 |

演示入口见 [cmd/search_demo](../../cmd/search_demo/)；评测入口见
[cmd/retr_eval](../../cmd/retr_eval/)。

## dev 口径声明（哪些是替身）

1. **查询编码是替身**：`FakeEncoder` 用 fakerepr 假编码（种子 = 查询
   文本），与文档侧（种子 = doc_key‖structure_ref‖revision_id）同一
   哈希口径但**不同种子空间**——sparse term 基本不相交，命中主要由
   dense/multi 的哈希相似度驱动。数字不代表真实检索质量，只保证链路
   确定性可测。
2. **deep 不逐轮**：`Decomposer` 产出的子查询列表经 `Plan.Validate`
   把关后，dev 执行取 `Plan.RewrittenQuery`（伞查询，防漂移那一路）
   单发检索 + Searcher 的 deep 放宽（双倍路内候选）；K 轮逐轮检索-
   重规划是真实化路径。
3. **判档是规则替身**：`Router.Route` 为长度/触发词/实体计数规则，
   接入小 LM 分类器时接口不变。
4. **query_id 由查询文本哈希派生**（`q-hex(sha256)[:12]`）：不含档位/
   交付——同一查询的不同档位共享追踪键；跨请求去重/追踪属网关职责。

## 边界（明确不做）

- **不含 B4 fusion**：B3 的 `Searcher.Search` 已含 RRF 融合，CE 第 4 路
  / RankGPT listwise 精排属 B4，不在本管线（`LaneScores.Rerank` 字段
  已为其预留）。
- **不做 fast 召回缺口升级**：升档只经判档建议的 MaxTier 合并（进入
  检索前一次性决定）；fast 召回不足不自动升级（C20/D12）。
- **不做流式**：产出完整 `PipelineResult`；C-6 chat 的流式切分在
  A1/B1 交付层处理。
- **不做持久化/网络/工件生产**：Store 由调用方装载注入（dev 用
  devseed；生产对接 artifact.Switcher 生效清单）。
- **不重复域校验**：各域包的出口闸（Plan.Validate /
  EvidencePack.Validate / SummaryResult.Validate）是唯一把关，管线
  只补请求侧入口校验与注入齐备性检查。

## 真实化路径

| dev 替身 | 真实形态 |
| --- | --- |
| `FakeEncoder`（哈希假编码） | DC representation 查询编码器（与文档侧同 encoder_id、同 term 空间） |
| 伞查询单发（deep） | 逐子查询检索-重规划（self-ask 轮次），多轮 pack 融合后进 B6 |
| `planner.Rule`/`Decomposer` | LLM 规划器（1 次）与 LLM 分解（K 轮×1 次），接口不变 |
| `Router.Route` 规则判档 | 小 LM 分类器（Adaptive-RAG 蓝本），接口不变 |
| `summary.StubSummarizer` | 经 D1 dc-gateway 的模型摘要（`Summarizer` 接口不变，出口闸照旧） |
| `retrieval.Store` 全量扫描 | Milvus 等 ANN/倒排引擎（`service/common/retrieval`） |

## 验收

```sh
go vet ./service/search/rpc/internal/pipeline/... ./service/search/rpc/cmd/search_demo/...
GOCACHE=/tmp/gocache-e2e go test -race -count=1 \
  ./service/search/rpc/internal/pipeline/... ./service/search/rpc/cmd/search_demo/...

# 演示入口（样例输出见 cmd/search_demo/README.md）
GOCACHE=/tmp/gocache-e2e go run ./service/search/rpc/cmd/search_demo --tier fast --delivery summary <<< "海洋观测的核心要点"
```

测试钉死：三档×两种交付全通过（fast 无 multi 路分数、balanced/deep
有）；同输入多次 `Execute` 深相等（确定性）；空查询/未知档位/未知
交付拒绝；tools 交付不调 Summarizer（录制探针）且允许 Summarizer
缺席；判档升档（deep 触发词把用户 fast 档升到 deep，候选放宽可观察）。
