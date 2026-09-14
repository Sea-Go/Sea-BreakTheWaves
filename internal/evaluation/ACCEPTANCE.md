# WS09-E / H12 离线评测最小基线验收

状态：**LOCAL_VERIFIED（固定合成 fixture）**，2026-09-14。尚无真实 H09 主体/展示事实、WS07-E ADS、获准产品 qrels 或实验分流接入；本页数字只用于检查公式，不能写作线上收益或业务发布意见。

## 工作区域和职责

- `[W0:ROOT]`：本 BTW 独立 worktree；Git 基点 `68c4c6f`。
- `[W1:WRITE]`：`internal/evaluation/` 的计算、测试、`testdata/` 与本文。
- `[R1:READ_ONLY]`：Sea-Docs 的 WS09-E、H12、算法评测和离线数据交接方案；BTW search/recommend/usermodel 契约。
- `[D1:DEPENDENCY]`：`go.mod` 锁定 tRPC-Agent-Go `v1.8.1`；未修改依赖。核心模块当前没有可直接导入的 `evaluation` 包；本切片仅计算冻结事实，不运行 Agent/LLM/Judge。将来 Runner 评测可以把其输出交给此确定性计算边界。
- `[G1:GENERATED]`：无；`[X1:EXTERNAL]`：无数据库、模型或在线调用；`[N1:OUT_OF_SCOPE]`：其他 BTW 能力域、原业务工作树和真实数仓；`[T1:TEMP]`：无保留产物。
- 主职责 `[C3:DOMAIN]` 评测资格和指标；跨区 `[C7:CONTRACT]` H01/H12 输入修订、`[C8:VERIFY]` fixture/验收。无发布状态迁移。

## 冻结输入与口径

`EvaluateSearch` 要求 benchmark/run/corpus、K、评测截止、时间切分、qrels 版本/来源/hash/可用时点及逐案结果。qrels 与运行语料快照必须一致；来源须由提供者标成 `approved_frozen` 或明确 `synthetic_fixture`。该标记是输入声明，**本包不授予标注审批权，也不校验外部工件的真实签名**。调用方须先核对来源工件和修订。`input_hash` 覆盖冻结的全部输入、切分及候选顺序；case 与 judgment 的枚举顺序先规范化。修订变化产生新 hash，旧报告不可原地换标注。

搜索仅对有完整 qrels、已判断前 K、含正相关答案的 case 计算 S09 Recall@K、S11 MRR@K、S12 nDCG@K。nDCG 使用非负线性 gain；未返回位置贡献 0。`completed` 空结果且有正 qrels 是真实 0；`failed`、无获准 qrels、未来 qrels、未判断前 K 是 `not_evaluable`/缺失分母；完整无答案 case 为 `not_applicable`，留给 S22。`partial` 保留实际结果的诊断值并单列 case 数，不能与完整结果无区别。候选与已核验引用分别计数，已核验引用必须有提供方 receipt ID；这个字段不自动把候选判成相关。

`EvaluateRecommendation` 只从冻结归因数据集的真实 `visible + impression_id + served` 生成曝光集合。已下发但不可见只进入 served 数，不进入曝光、负例或内容覆盖。成熟正标签限定 click/read/favorite；成熟负标签限定显式负反馈或已成熟窗口内未获准正反馈，且标签可用与窗口成熟时点均不晚于截止。待定/排除/未来标签不填 0。R06 AUC 以成熟正负配对、同分半票计算；单类别或无展示为 `not_evaluable`。R05 nDCG@K 只对 Top K 全部真实可见且列表标签完整的请求计算；R12 是窗口真实展示唯一对象数 / 固定合格内容集合数。覆盖与排序的分母分开。

所有 case 使用完整 `SubjectRef(authority_id, tenant_id, subject_id)` 冻结主体，不能用裸 UID 跨身份源/租户合并；session 只在该完整主体内分组。`query_family_scope` 必须显式为 `subject` 或 `global`；前者按主体隔离，后者可跨主体防止近似问题泄漏。`SplitPlan` 冻结 train/validation/test 时间边界及 embargo；请求时可用的特征不得晚于请求。相同完整主体、所属会话或声明作用域下的查询族不能跨切分。`EvalReport` 保留 `input_hash`、指标定义修订、K、来源/切分版本及 hash、截止时点、数值/状态/分子分母、缺失/空/部分/未判断/真实展示/成熟计数与限制。`recommendation` 固定 `incomplete`；本切片没有评分门禁或业务激活权。

## 本地结果

执行：

```bash
go test -race ./internal/evaluation
go vet ./internal/evaluation
git diff --check
```

三项通过。固定搜索 fixture 两个 case：首案 b(相关 1)、a(相关 2) 的前两名，次案有正标注但完整空结果；宏平均 Recall@2 = 0.5、MRR@2 = 0.5、nDCG@2 = 0.429859...，可核对 1 个空结果、1 个引用收据和 1 个仅候选。固定推荐 fixture 有 5 个 served、4 个 visible、4 个成熟标签，AUC = 0.75、请求级 nDCG@2 = 0.815465...、可见对象覆盖 = 4/5；这些值仅证明手算和代码一致。

反例已测试：未获准/未来 qrels、未知 Top K 对象、不同语料快照、搜索失败与部分结果、重复候选、无收据的“已验证”引用、全无答案、跨版 hash 变化；无真实曝光、待定标签、仅一类成熟标签、无来源负例、重复 impression、未下发却声称展示、未来标签、同分 AUC；未来特征、越界时间切分、同完整主体跨 split、不同 tenant/authority 的同 UID 与同名会话互不串、显式全局 query family 跨 split 泄漏。

## 接口交接与未验收项

WS07-E 提供固定 ADS/DWS 数据集工件与权威 hash、水位、有效内容集合、真实展示和成熟归因修订；不能由此 Go 包平行重算仓库 SQL、补造 `client_impression`，也不能把 `candidate_served` 作为负例。搜索/内容 owner 需提供同一 CorpusSnapshot 的获准 qrels、case 覆盖、独立候选与 RTW 引用收据。WS09-D 可消费 `input_hash + split_revision + metric_definition_revision + K` 作为训练候选对照锚点，但需自行持有模型/数据集导出与发布证据。

尚未实现 WS09-E 的 12 种搜索组合和三路消融、49 指标族、真实 qrels/ADS 联调、分流/SRM/成熟 cohort 统计、持久报告/ImprovementCase/ExperimentSpec、正式结论门禁和上线复验。`approved_frozen` 目前仅是调用方声明；没有真实提供方接纳前状态保持 **LOCAL_VERIFIED**，H12 不标整体 `ACCEPTED`。
