# planner（B2 查询规划与判档 · 纯域层）

`service/search/rpc/internal/planner` 实现工程方案 §3.1 的 B2：**查询规划**
（把一个用户查询变成结构化检索计划）与**判档**（把查询复杂度映射为
fast/balanced/deep 检索档位）。本包是纯域层——不调模型、无 IO、无状态，
所有规划器与判档器零值可用、并发安全、同输入同输出。

## 与 §3.1 / §3.2 的对应

§3.1 查询策略分配表逐行落地（LLM 预算列即 `Plan.LLMBudget` 契约）：

| 档 | LLM 预算 | 本包实现 | 计划内容 |
| --- | --- | --- | --- |
| fast | **0 次** | `Identity`（identity.go） | 恒等计划：`rewritten_query` 原样透传，无变体/子查询——B1 fast 直通的实现（直发 dense+sparse 两路，跳 multi [D-4]） |
| balanced | **1 次** | `Rule`（rule.go） | `rewritten_query`=TrimSpace 后原查询（§3.1 ④"原查询永远保留为一路防漂移"）+ 3 个确定性变体（去停用词/前 N 关键词/同义词表查换） |
| deep | **K 轮×1 次** | `Decomposer`（decompose.go） | 伞查询 + 有界子查询列表（≤5），每子查询经 Delegate 递归产"1 次规划"口径的子 Plan，预算 K=len(subqueries) |

§3.2 档位 × 能力矩阵中本包对应"LLM 查询规划"行（fast ✘ 0 次 /
balanced ✔ 1 次 / deep ✔ K 轮）；矩阵其余行（三路取舍、树折叠、精排）
由检索层 `retrieval`（M3）与精排层（B4）承接，本包只产计划不执行。

## 职责（B2）

| 能力 | 入口 | 说明 |
| --- | --- | --- |
| 计划契约 | `Plan` / `Planner` / `Plan.Validate()`（plan.go） | §3.1 planner 输出的 Go 形态（JSON snake_case，`rewritten_query/variants/stepback?/subqueries/tier/llm_budget`）+ 出口/入口校验（fast=0、balanced=1、deep=K 一致性） |
| fast 恒等规划 | `Identity.Plan` | 原样透传、预算 0；只服务 fast 档 |
| balanced 规则规划 | `Rule.Plan` | TrimSpace + 3 确定性变体（写死小表，见 rule.go 文件头 dev 口径）、预算 1；只服务 balanced 档 |
| deep 子查询分解 | `Decomposer.Plan` / `Decomposer.SubPlans` | 按分号/问号/连接词切分、有界 ≤5、递归调 Delegate 产子 Plan、预算 K；只服务 deep 档 |
| 判档 | `Router.Route`（router.go） | 规则复杂度判断（见下）；`MaxTier` 实现档间只升不降 |

判档规则（dev 口径，长度按 rune 计）：**deep** = len>100 或含
比较/分析/为什么；**balanced** = len>30 或含多个实体（≥2 个 ASCII
词元或书名号，多实体的 dev 近似）；否则 **fast**。

## dev 口径声明（哪些是替身）

1. **三档规划器都不调 LLM**：balanced 的"1 次"与 deep 的"K 轮×1 次"
   目前由确定性规则产出，`LLMBudget` 记的是契约预算而非实际调用数。
2. **小表写死**：停用词表、同义词表、连接词表均为人工小表（注释即
   规范）；同义词查换是单趟替换（产物不重扫，避免"检索→搜索"被
   "搜索→检索"换回），对合成词可能误换。
3. **切分无分词**：连接词按子串匹配（"和平"会被"和"切开）；子查询
   超 5 段取前 5、丢弃尾部。
4. **stepback 不产出**：§3.1 balanced ③ 的 step-back 变体属 LLM 规划
   器职责，dev 规划器置 nil（契约字段与序列化已就位并被测试钉死）。
5. **判档是规则替身**：Adaptive-RAG 蓝本（arXiv:2403.14403）的小 LM
   分类器尚未接入，`Route` 是长度/触发词/实体计数的规则版。

## 边界（不能做什么）

- **不调模型、不做编码**：查询向量化（dense/sparse/multi 三件套）属
  DC representation 职责，本包输出的是文本计划。
- **不做检索执行**：三路召回、RRF、树折叠、邻域扩展由 `retrieval`（M3）
  承接；**不做精排**：CE 第 4 路 / RankGPT listwise 属 B4。
- **不做档位升级决策**：fast 召回不足返回缺口、不自动升级（C20/D12）；
  本包只提供 `MaxTier` 供调用方合并"用户显式档 × 判档建议档"。
- **与 `trpcagent/planner` 无关**：那是 trpc-agent 侧 Search
  fast/detailed 的规划占位包；本包是 §3.1 的 B2 检索查询规划。

## 真实化路径

| dev 替身 | 真实形态 |
| --- | --- |
| `Rule`（balanced 规则变体） | LLM 规划器：query2doc 扩展 + 多视角改写 + step-back（1 次调用），接口同为 `Planner` |
| `Decomposer`（连接词切分） | LLM 分解（self-ask 式有界子查询队列，逐轮检索-重规划），超限尾部由 LLM 合并/摘要 |
| `Router.Route`（规则判档） | Adaptive-RAG 式小 LM 分类器（自动标签，与三档同构）；接口不变 |
| 写死同义词/停用词小表 | LLM 规划器自带或外挂同义词库 |

## 验收

```bash
go vet ./service/search/rpc/internal/planner/...
GOCACHE=/tmp/gocache-m4 go test -race -count=1 ./service/search/rpc/internal/planner/...
```

接线（B1 网关按 `MaxTier(请求档, Route建议档)` 选规划器、B3 执行器
消费 `Plan`/`SubPlans` 逐轮检索）在后续装配里程碑完成，本包不依赖它们。
