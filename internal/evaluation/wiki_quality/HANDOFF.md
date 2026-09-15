# Wiki 人工事实质量评测：Dataset/Manifest 交接

状态：**L1 确定性评测合同与合成事实夹具已验；真实 RTW 人工 EventSpec、事实全集资格、DC 用量与数仓同父未验**。固定基点为 BTW 内容开发集成 `a92a600e10a7dca13233b1f07314e7a4d855b9a8`。本包不调用 LLM、自评 Agent、模型 Gateway、活动知识索引，也不写 RTW 编辑 head 或发布 Release。既有本机真实 DC Wiki 模型两轮只用同一 14 字节 `one fixed fact`，Wiki 正文也逐字复制；本包对缺人类事实全集/标签的同类输入返回 `not_evaluable`，不能给页面质量或 D07 阈值。

## 输入与权威边界

`EvaluateCase(ctx, Input, Verifiers)` 最窄输入为目标**不可变 WikiRevisionID**、其 `ai_accepted|manual_revision` 种类、当时模块/页面/base/选定 SourceRevisionID 顺序、RTW 源原文字节与整修订 SHA、模型候选（AI 版）或前一 AI 修订（人工版）、RTW 人工完整事实集合及标签、可选 DC 用量。每条事实携 `FactID,SourceRevisionID,paragraph:N,original_byte_start/end,source_quote,original_byte_sha256`；quote 必须是该原修订同段中的**精确原字节子串**。CRLF 仅用于段落定位，四空格代码与 quote 的原对象 byte span 不会被 TrimSpace 变成别的文字。`SourceRef` 段落存在不等于事实全集、正确性或人工批准。

评阅按 `(wiki_revision_id,fact_id)` 分版本，不把人工新修订的标签折回旧 AI 修订。`Review` 的 `review_version,rubric_version=sea.wiki.fact-coverage.v1,reviewer_uid,facts_complete,labels_complete` 与每条 `assessment,grade,wiki_claim_text,reason,base_judge_revision_id` 是 BTW **typed 计算对象**，不是尚未冻结的 `knowledge.wiki.quality.judged.v1` 源 Event JSON。RTW v1 rubric 的 0 为 `missing|conflict`，1 为 covered 但表达/引用不完整或有歧义，2 为 covered、事实与引用忠实但限定不足，3 为 covered、限定清楚且可人工维护；`undetermined` 无 grade 且理由必填。Grade 由 RTW 人工给出，BTW只核枚举/目标 Wiki 原文字节 span、计数和 citation 的同修订 SourceRef，不由模型判断真假，也不设未经 D07 测量的比例硬门禁。必覆盖事实中有 `undetermined` 时覆盖分数单独为 `not_evaluable`，不把待判当作零；已判缺失/冲突才进入明确分母。`ReviewerUID` 是 RTW 当时 Actor UID，不代表实时 UserCenter 资格；Wiki 质量域没有租户字段。

真实 `human_admin` 必须由未来 RTW **只读** `AuthorityVerifier` 返回 `AuthorityReceipt`：核源 Event 原 JCS hash、连续 DC 真 offset、typed Review JCS hash、SourceScope JCS hash、事实全集/标签齐全及 SourceRevision 资格。EventSpec exact keys、FactID 的服务端派生公式、管理员撤权/签名与原事件私有 GET 仍由 RTW writer 冻结；本包没有自行猜 wire 键或把任意 `human_admin` 字符串当凭据。`CostVerifier` 是另一个 DC owner 端口，独立核同 ModelConfigurationID 的真实 Provider token/时延收据；即使人工事实资格通过，未经 DC 核实的调用成本仍是 `not_evaluable`。合成夹具标 `synthetic_fixture`，仅可证明计算口径。

先查完整事实集合和每条标签，再计算覆盖分子/分母。缺全集、标签、原源资格、已撤回/不可读 Wiki、AI 模型/Prompt/业务技术出处、RTW Event/真实 offset/Actor UID或权威回执时，Case 仍冻结明确原因但事实人数、覆盖率、引用忠实度均保持 `not_evaluable`。哈希与版次不相符、错源 FactID、错 quote byte span、非法 grade/assessment 或人工目标 base 与所声明的前一 AI RevisionID 不一致则直接拒错。`manual_revision` 可没有新的 `compile_id`、模型配置或技术 Job；若给 `PreviousAI`，其 RevisionID 必须是人工目标的 base，改稿统计只比较这两个固定原文。不存在 Markdown 首字符必须为 `#` 的门禁。

## 哈希和对象交接

`Scope.HashDomains` 分别记录 RTW **业务 CompileInput**、DC **整个 Jobs.Submit**、RTW **Accept Result**、DC **13 键成功技术 Manifest** 和 DC **整个技术 Result** 的 SHA。每个 SourceRevision 原对象SHA、模型候选/Wiki 原文SHA、事实原 quote SHA、人工 typed Review JCS SHA、质量 Case JCS SHA、质量 Report JCS SHA、Dataset Root JCS SHA 又是不同域；不得拿摘要同名比较或从活动索引找历史字节。质量 Case 的原文对象只留 SHA/版本引用，事实 quote与人工标签按稳定 FactID 次序进 JCS；下游须从 RTW 同修订原对象读回才可重评。

`EvaluateCase` 输出 `{ManifestJCS,ManifestSHA256,ReportJCS,ReportSHA256}`。CaseID 由无自身 ID 的 Case JCS 算出，公开读 `DecodeCaseManifest(raw,sha)` 复核 JCS、自身 ID 和字面修订。`FreezeDataset(revision,cases)` 对 CaseID 排序，保留目标 WikiRevisionID、事实 ID 集、rubric/review version/typed Review SHA、人类 grade 来源、RTW 原 Event JCS SHA/真实 DC offset/Producer/EventID/ReviewerUID与各 Case/Report SHA；同一 Wiki 可在不同人工 review_version 留两条历史，重复 `(WikiRevisionID,review_version)` 拒。Root 对无自身 SHA 的 DatasetManifest JCS 取 SHA，`DecodeDatasetManifest` 再核完整排序、键、hash、状态与 `activation=none`。真实 Wiki ODS 需要独立连续 cursor；不得借用人工 qrel 消费者或把非 Wiki 源 offset 缺口补造为 Fact 位置。

合成固定 Golden：双来源/五事实 AI Case JCS SHA=`6ceeda9bc97639684a9d2ea29a8f490130b9e209f8d3b3d08ad1ec54ff8e78a8`；同页人工新修订与 AI 组成的 Dataset Root SHA=`e995ec05366b6d910854a7d5f4ef0112e73822b85fc054f7b58fdacfa69efa2c`。这两个 SHA 只锁**合成合同字节**。五事实中四条必覆盖：已覆盖2、缺失1、冲突1、undetermined1，所需事实覆盖为 `2/4`；重复原文段落1，AI token夹具297输入+471输出=768总 token。人工版的 FactID 相同但 WikiRevisionID、新评阅version、等级和改稿原文均独立。测试还拒 CRLF byte shift、四空格丢失、漏引高等级、SourceRevision换版/撤回、标签不完整、人类权威/成本伪报、JCS篡改和重复 Dataset entry。

固定独立分支验证：`GOMAXPROCS=2 go test -p 1 -mod=readonly -race -count=1 ./internal/evaluation/...` 三包顶层 0，日志 SHA=`dbef4d0ca79c223ab6f45195142cd63ab368ab8011e9d0c6e05e84f3fb1da9e6`；`go vet ./internal/evaluation/...` 顶层 0/空日志 SHA=`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`；`go mod verify` 为 `all modules verified`。未用这轮测真实 RTW Event/PG、DC 模型或 CH/dbt。

## 下一 owner 和验收边界

RTW writer 应先冻结 `knowledge.wiki.quality.judged.v1` 的**原源 EventSpec/golden**、服务器导出的 FactID 公式、固定 SourceRevision 原对象/paragraph定位、人类审核资格/撤权、AI 与人工 Wiki 修订双主键与人工 Judge CAS，并提供按原 EventID/SHA/offset 私有读回。BTW/数仓 reader 再独立以该精确合同实现 Wiki ODS/cursor 和此包的 `AuthorityVerifier`；DC owner 提供同 native `app_user`、同已发布配置、Provider 用量/时延的 `CostVerifier`。Web 工作台按目标 RevisionID 展示每事实的原来源/quote span、人工标签和改稿，人工 Release 仍由 RTW 管理员操作。真实 RTW Event→DC→Wiki ODS/CH/dbt、管理员浏览器与三路同版搜索/问答 qrel都尚未签；此包的 synthetic 分数不得替代那些验收。
