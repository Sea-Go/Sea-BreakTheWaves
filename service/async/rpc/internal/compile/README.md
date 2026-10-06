# compile（C4 编制编排 + C5 步骤执行 · 纯域层）

`service/async/rpc/internal/compile` 实现工程方案 §4.8 决策 8 的
STORM 式编制流水线**纯域层**：C4（Wiki 编制编排——四阶段状态机）与
C5（步骤执行——幂等的单阶段执行器）。本包不调模型、无 IO、无状态，
编排器与 dev 执行器零值可用、并发安全、同输入同输出；阶段产物是
JSON snake_case 纯数据，落盘与断点恢复由 worker 装配层承接。

依据：《LLM-Wiki 平台落地工程方案》§4.8 决策 8、里程碑 M5、
C-13 阶段任务契约；《LLM-Wiki 相关系统考察依据》§A（STORM 代码级
核验：四模块、do_* 开关、引用双表形态、WikiChat claims 过滤）。
均位于 Sea-Docs（文档/技术与文档/参考，仓库外姊妹库）。

## 职责（C4 编排 + C5 步骤执行）

| 能力 | 入口 | 说明 |
| --- | --- | --- |
| 阶段契约 | `Stage` / `StageInput` / `StageOutput`（stage.go） | 四阶段枚举与各阶段输入/输出数据契约（JSON 可落盘） |
| 编排状态机 | `Orchestrator.Advance` / `Run`（orchestrator.go） | C4：`CompileJob` 按 pending→curating→…→done/failed 推进；幂等、失败不回退、do_* 跳过 |
| 步骤执行 | `StageFunc` 接口（executor.go） | C5：单阶段执行器抽象（对应 C-13 阶段任务 `{step_id, stage, inputs_ref}` 的 step 执行） |
| dev 执行器 | `DevCurator` / `DevOutliner` / `DevArticleWriter` / `DevPolisher` | 四阶段确定性替身（`DevExecutor()` 成套取用） |
| 引用双表+Claim | `Citation` / `ExtractClaims` / `UnevidencedClaims`（claim.go） | 行内 [n] + 编号→(doc revision, locator) 映射表；无证据 claim 供前端标"待人工补证" |

## STORM 四阶段对应

STORM 四模块（Knowledge Curation→Outline Generation→Article
Generation→Article Polishing）逐段落地，每阶段输入/输出按本平台
"管理员导入语料（doc_key 全链路唯一）"改造：

| Stage | STORM 模块 | 输入 | 输出 | dev 执行器（替身口径） |
| --- | --- | --- | --- | --- |
| `curate` | Knowledge Curation | 资料源列表（`SourceRef`=doc_key+摘要+revision） | 要点清单+多视角提问 | `DevCurator`：摘要高频词 top-8 作要点（CJK 二元组+ASCII 词元计数，次数降序/字典序升序）+ 3 个固定视角问题 |
| `outline` | Outline Generation | 要点清单 | 章节树 | `DevOutliner`：要点均分 ≤5 章，标题"第 N 章：要点摘要"，`PointIndexes` 记各章覆盖 |
| `article` | Article Generation | 章节+要点+资料源 | 章节正文（行内 [n] 引用）+引用映射表 | `DevArticleWriter`：每章正文=对应要点逐行复述+行内 [n]（n 从 1 递增、按源数回卷，doc_key 取自 sources）；引用表=编号 1..S |
| `polish` | Article Polishing | 全文（`ArticleDraft`） | 润色后全文+去重说明 | `DevPolisher`：原样返回+末尾追加"由 AI 编制，待人工修订"声明，不去重（`DedupNotes` 空） |

阶段间接线由编排器装配（`stageInput`）：curate←`job.Sources`；
outline←curate 要点；article←sources+要点+章节树；polish←article 全文。

## do_* 语义（断点续跑）

对应 STORM 的 do_research/do_generate_outline/do_generate_article/
do_polish_article 四开关，本包拆成三个正交机制：

1. **幂等跳过=产物已存在**：`Advance` 对已完成阶段（`StageState` 有
   产物）直接短路返回、不调执行器——STORM"阶段产物已落盘则跳过该段"
   的 Sea 形态；`Run` 重入自动从断点续跑（已完成阶段不重复执行）。
2. **失败不回退**：阶段失败只置 `StatusFailed`+`StageState.Err` 记账，
   已完成阶段原样保留；对同阶段再次 `Advance` 即重试（Err 清空、
   恢复推进）。`Advance` 值语义：不接管返回值则调用方持有的任务不变。
3. **do_polish 可关**：`Advance(ctx, job, StagePolish, nil)` 以 nil
   执行器显式跳过润色（=do_polish_article=false），Article 终稿直接
   done；`Run` 侧为 `RunOptions{SkipPolish: true}`。curate/outline/
   article 是内容链前置，传 nil 报 `ErrSkipNotAllowed`。

## 引用双表与 Claim 追踪

- **双表形态**（把 STORM"编号→URL"升级为"编号→(doc revision,
  locator)"，补齐其无段级定位的缺口）：第一表=正文行内 [n] 角标
  （`SectionDraft.Body` 内）；第二表=`ArticleDraft.Citations`
  （`Citation{Index, DocKey, RevisionID, *Locator}`），Article 阶段
  生成、Polish 原样携带、发布时随 revision 冻结。`Locator`
  （section_path+para_index+quote）为段级定位契约，dev 执行器不产出、
  真实化后由检索/结构层 locator 语义填充。
- **Claim 过滤**（借 WikiChat generate→claims→证据过滤，对冲
  over-association 风险）：`ExtractClaims(text, citations)` 按句提取
  （句末标点+换行分句），句内首个 [n] 命中引用表则 `HasEvidence=true`；
  无角标（`SourceIndex=0`）与悬空编号（`SourceIndex=n>0`）均无证据。
  `UnevidencedClaims` 产出"待人工补证"清单供前端标记。

## 边界（不能做什么）

- **不调 LLM**：四个执行器是确定性替身（真实化路径见下）；多 LM
  路由（对话/提问便宜模型、大纲/成文强模型）属 DC 模型网关职责。
- **不做产物落盘**：`CompileJob`/`StageOutput` 仅保证 JSON 可序列化
  往返（测试钉死）；落盘、断点恢复的持久化与 C-13 任务派发由 C4
  worker 装配层承接。
- **不发布**：编制产物止于草稿（终稿声明"待人工修订"）；版本化
  发布/回滚/C08 门禁在 A5（RTW release rpc）。
- **不做 Co-STORM 管理员话语注入**：调研话语的可观察与导向注入属
  编排装配层（后续里程碑），本包的 `StageInput` 不含话语通道。
- **不依赖既有编制链**：`internal/app` 的 wiki_compile worker（既有
  RTW/DC 票据契约）与本包无编译期依赖；按本包状态机重构接线属后续
  里程碑。

## 真实化路径

| dev 替身 | 真实形态（接口不变，均实现 `StageFunc`） |
| --- | --- |
| `DevCurator`（高频词+固定视角） | LLM 调研：相似主题 perspective 发现+"撰写者×专家"多轮对话提问（便宜快模型），产物落 `conversation_log` |
| `DevOutliner`（均分 ≤5 章） | LLM 大纲（强模型），层级大纲用 `Section.Children` 契约 |
| `DevArticleWriter`（要点复述+[n]） | 成文 LM（"generate verifiable text with citations"）：按节检索 top-k 参考编号拼 prompt，行内 [1][3] 式引用，locator 由证据检索填充 |
| `DevPolisher`（原样+声明） | LLM 润色：补总结节+去重，`DedupNotes` 记录合并/删除的重复表述 |
| `ExtractClaims`（句子级+首角标） | WikiChat 式语义级 claim 切分+逐 claim 证据比对（标题行不再计入待补证） |

## 验收

```bash
go vet ./service/async/rpc/internal/compile/...
GOCACHE=/tmp/gocache-c45 go test -race -count=1 ./service/async/rpc/internal/compile/...
```

测试覆盖：编排全流程（4 阶段顺序+done+阶段间接线）、幂等（已完成
阶段跳过且产物指针不变）、失败重试（failed 记账+原任务不变+重试恢复）、
do_polish 跳过（nil 执行器与 `RunOptions.SkipPolish` 两口径）、前置
校验、`Run` 断点续跑（curate 全程仅执行一次）、`CompileJob` JSON 落盘
往返、Claim 分类（有/无引用、悬空编号、首角标优先、换行分句、待补证
清单）、四个 dev 执行器确定性与入口护栏。
