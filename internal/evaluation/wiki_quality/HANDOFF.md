# Wiki 人工事实质量评测：Dataset/Manifest 交接

状态：**L1 确定性评测合同与合成事实夹具已验；RTW 单事实 EventSpec v1 和独立 Wiki ODS 已固定，但完整 FactCatalog/SourceScope 资格、真实评测与 DC 成本权威仍欠签**。固定基点为 BTW 内容开发集成 `a92a600e10a7dca13233b1f07314e7a4d855b9a8`。本包不调用 LLM、自评 Agent、模型 Gateway、活动知识索引，也不写 RTW 编辑 head 或发布 Release。既有本机真实 DC Wiki 模型两轮只用同一 14 字节 `one fixed fact`，Wiki 正文也逐字复制；本包对缺人类事实全集/标签的同类输入返回 `not_evaluable`，不能给页面质量或 D07 阈值。

## 输入与权威边界

`EvaluateCase(ctx, Input, Verifiers)` 最窄输入为目标**不可变 WikiRevisionID**、其 `ai_accepted|manual_revision` 种类、当时模块/页面/base/选定 SourceRevisionID 顺序、RTW 源原文字节与整修订 SHA、模型候选（AI 版）或前一 AI 修订（人工版）、RTW 人工完整事实集合及标签、可选 DC 用量。每条事实携 `FactID,SourceRevisionID,paragraph:N,original_byte_start/end,source_quote,original_byte_sha256`；quote 必须是该原修订同段中的**精确原字节子串**。CRLF 仅用于段落定位，四空格代码与 quote 的原对象 byte span 不会被 TrimSpace 变成别的文字。`SourceRef` 段落存在不等于事实全集、正确性或人工批准。

评阅按 `(wiki_revision_id,fact_id)` 分版本，不把人工新修订的标签折回旧 AI 修订。`Review` 的 `review_version,rubric_version=sea.wiki.fact-coverage.v1,rtw_actor_id,facts_complete,labels_complete` 与每条 `assessment,grade,wiki_claim_text,wiki_claim_sha256,wiki_span_provenance,reason,base_judge_revision_id` 是 BTW **typed 计算对象**，不是 RTW 单事实 `knowledge.wiki.quality.judged.v1` 的源 Event JSON。RTW v1 rubric 的 0 为 `missing|conflict`，1 为 covered 但表达/引用不完整或有歧义，2 为 covered、事实与引用忠实但限定不足，3 为 covered、限定清楚且可人工维护；`undetermined` 无 grade 且理由必填。Grade 由 RTW 人工给出，BTW只核枚举/引用的同修订 SourceRef、计数和原文字节，不由模型判断真假，也不设未经 D07 测量的比例硬门禁。必覆盖事实中有 `undetermined` 时覆盖分数单独为 `not_evaluable`，不把待判当作零；已判缺失/冲突才进入明确分母。`RTWActorID` 保留管理员 JWT `userId` 经当时 RTW 名单放行的**原字面**（可为 `test-admin`、中文等），不冒称 UserCenter 规范 UID或实时撤权资格；Wiki 质量域没有租户字段。

真实 `human_admin` 必须由 RTW **只读** `AuthorityVerifier` 返回 `AuthorityReceipt`：核已固定单事实 Event 原 JCS hash、连续 DC 真 offset、typed Review JCS hash、SourceScope JCS hash、**另签版本化 FactCatalog 的应覆盖事实全集与标签完整性**及 SourceRevision 资格。RTW v1单 Event/Worker 原 Event 私有 GET 已冻结原事实 `source_byte_start/end`和 `source_quote_sha256`，却仅签 Wiki claim 的**文字与 SHA**，不签某个具体 Wiki byte span；本包的 `derived_first_match` 只按目标 Wiki 原对象找首次精确匹配，不能标成 `human_signed`。Source quote byte span 是另一个真正由 RTW 源签的哈希域。管理员撤权/FactCatalog仍待RTW owner，任意 `human_admin` 字符串不等于权威凭据。`CostVerifier` 是另一个 DC owner 端口，独立核同 ModelConfigurationID 的真实 Provider token/时延收据；即使人工事实资格通过，未经 DC 核实的调用成本仍是 `not_evaluable`。合成夹具标 `synthetic_fixture`，仅可证明计算口径。

真实 HumanAdmin 的 `AuthorityReceipt.FactCatalogRevisionID + FactCatalogJCSSHA256` 必须由被注入的 RTW 只读 verifier **实际回查完整目录和资格**后返回，且 Catalog SHA 不能用单事实 Event JCS SHA 代替。当前 RTW v1没有完整 Catalog，正式 verifier 不能产这份回执，因此真实页面 `human_admin` 保持 `not_evaluable`。本包测试中的伪 verifier 与伪 Catalog SHA 只演练资格开关的确定性逻辑，不代表 RTW 已批准某个样本。

先查完整事实集合和每条标签，再计算覆盖分子/分母。缺全集、标签、原源资格、已撤回/不可读 Wiki、AI 模型/Prompt/业务技术出处、RTW Event/真实 offset/历史 ActorID或权威回执时，Case 仍冻结明确原因但事实人数、覆盖率、引用忠实度均保持 `not_evaluable`。哈希与版次不相符、错源 FactID、错 quote byte span、伪 Wiki claim SHA/`human_signed`位置、非法 grade/assessment 或人工目标 base 与所声明的前一 AI RevisionID 不一致则直接拒错。`manual_revision` 可没有新的 `compile_id`、模型配置或技术 Job；若给 `PreviousAI`，其 RevisionID 必须是人工目标的 base，改稿统计只比较这两个固定原文。不存在 Markdown 首字符必须为 `#` 的门禁。

## 哈希和对象交接

`Scope.HashDomains` 仅在 AI 目标记录 RTW **业务 CompileInput**、DC **整个 Jobs.Submit**、RTW **Accept Result**、DC **13 键成功技术 Manifest** 和 DC **整个技术 Result** 的 SHA；人工目标不得把旧 AI Job 摘要冒作本版技术结果，旧 AI 修订由独立 Case 保留。每个 SourceRevision 原对象SHA、模型候选/Wiki 原文SHA、事实原 quote SHA、人工 typed Review JCS SHA、质量 Case JCS SHA、质量 Report JCS SHA、Dataset Root JCS SHA 又是不同域；不得拿摘要同名比较或从活动索引找历史字节。质量 Case 的原文对象只留 SHA/版本引用，事实 quote与人工标签按稳定 FactID 次序进 JCS；下游须从 RTW 同修订原对象读回才可重评。

`EvaluateCase` 输出 `{ManifestJCS,ManifestSHA256,ReportJCS,ReportSHA256}`。CaseID 由无自身 ID 的 Case JCS 算出，公开读 `DecodeCaseManifest(raw,sha)` 复核 JCS、自身 ID 和字面修订。`FreezeDataset(revision,cases)` 对 CaseID 排序，保留目标 WikiRevisionID、事实 ID 集、rubric/review version/typed Review SHA、人类 grade 来源、RTW 原 Event JCS SHA/真实 DC offset/Producer/EventID/`rtw_actor_id`与各 Case/Report SHA；同一 Wiki 可在不同人工 review_version 留两条历史，重复 `(WikiRevisionID,review_version)` 拒。`human_admin` Case 只有 `observed` 且 RTW 权威证据齐全才标 `grade_source=rtw_human_event`，否则字面 `unverified_human_claim`，下游 reader 同版拒误标。Root 对无自身 SHA 的 DatasetManifest JCS 取 SHA，`DecodeDatasetManifest` 再核完整排序、键、hash、状态与 `activation=none`。真实 Wiki ODS 需要独立连续 cursor；不得借用人工 qrel 消费者或把非 Wiki 源 offset 缺口补造为 Fact 位置。

已核真实 HumanAdmin 的 Case、Report 和 Dataset Entry 都另列 `fact_catalog_revision_id/fact_catalog_jcs_sha256`；与单判断 Event hash 分域，公开 Case/Dataset reader 对 `observed` 无目录 proof 或把目录 SHA 冒作 Event SHA 的输入拒错。当前单 Event 的 ODS `quality_verified` 只说明该判断的字节与来源通过，不能从 N 条事件自行补出完整 FactCatalog。

合成固定 Golden：双来源/五事实 AI Case JCS SHA=`086d450bf409c599705911f8a69360cfb7e85b1ce7b20808846af020123dc60f`；同页人工新修订与 AI 组成的 Dataset Root SHA=`84422bcb811b165136d47c97459ec27fc89c4826ca9cde0140a935b57299b6f5`。这两个 SHA 只锁**合成合同字节**，与 RTW v1单事实 Event raw/JCS Golden又是不同摘要。五事实中四条必覆盖：已覆盖2、缺失1、冲突1、undetermined1，所需事实覆盖为 `2/4`；重复原文段落1，AI token夹具297输入+471输出=768总 token。人工版的 FactID 相同但 WikiRevisionID、新评阅version、等级和改稿原文均独立。测试还拒 CRLF byte shift、四空格丢失、重复 Wiki claim 第二匹配/伪 `human_signed`与SHA、漏引高等级、SourceRevision换版/撤回、标签不完整、人类权威/成本伪报、JCS篡改和重复 Dataset entry。

固定独立分支此次增量代码验证：`GOMAXPROCS=2 go test -p 1 -mod=readonly -race -count=1 ./internal/evaluation/wiki_quality` 顶层 0，日志 SHA=`df996ba3a97d3a6efb36c291c88e2cb506c3ce3ea16c3eae8a921f41b9604473`；`go vet ./internal/evaluation/wiki_quality` 顶层 0/空日志 SHA=`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`。基础模块三包race、`go mod verify` 在前一源码轮已通过，本次没有重型PG/CH/模型作业。真实RTW Event/完整FactCatalog、DC成本和人工页面质量仍未由此轮验证。

## 下一 owner 和验收边界

RTW 已固定 `knowledge.wiki.quality.judged.v1` 的**单事实**原源 EventSpec/golden、服务器导出的 FactID 公式、固定 SourceRevision 引文原字节/paragraph定位、AI 与人工 Wiki 修订双主键/Judge CAS 和按 EventID/SHA 私有读回。下一 writer 须另签**版本化完整 FactCatalog/SourceScope 收据**，按目标 WikiRevisionID/获准 SourceRevisionIDs 列全应覆盖事实及评阅批次；当前 JWT+名单 `actor_id` 资格不等于 UserCenter 实时撤权。BTW/数仓已用独立 Wiki ODS/cursor 接原 Event 与DC连续offset，但还需把完整目录权威接入此包的 `AuthorityVerifier`；DC owner 提供同 native `app_user`、同已发布配置、Provider 用量/时延的 `CostVerifier`。Web 工作台按目标 RevisionID 展示每事实的原来源/quote span、人工标签和改稿，人工 Release 仍由 RTW 管理员操作。页面完整质量、管理员浏览器与三路同版搜索/问答 qrel都尚未签；此包的 synthetic 分数不得替代那些验收。
