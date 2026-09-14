# 真模型摘要的一格十二路径矩阵见证

日期：2026-09-15。基线 BTW 集成提交 `373add6`。状态：**独立发布快照的一格功能交付已观测，语义质量门禁失败；其余 11 格未执行**。本见证只消费一份 RTW 耐久答案报告与一份以相同 SearchID 精确绑定的 DataCenter 用量报告，不读另一快照的 Tools 报告。

`cmd/search-summary-matrix-witness`先以原始字节 SHA 对照两份源报告，核 SearchID/AnswerID、quote/answer、RTW 引用 pack ref、模型配置/interaction、实际 token 用量、RTW PG 三张耐久表与历史读面相符；强制 `quality_gate=not_passed_unsupported_interpretation`、`activation=none`、`relevance_state=not_evaluable_no_qrels`。使用现有 `evaluation.SearchVariants()` 固定输出 12 格顺序，仅 `fast/low/summary` 写 `delivery_execution=observed` 和实测模型/端到端成本；其余每格为 `not_executed`，无 SearchID/模型用量/候选。所有格 `S09.recall_at_k`、`S11.mrr_at_k`、`S12.ndcg_at_k` 都只有 `state=not_evaluable,value=null`，矩阵推荐 `incomplete`。

本轮使用集成验收的 [DC↔RTW 用量报告](/private/tmp/sea-summary-live-integrated-20260915/combined-model-usage.json) SHA `0fbae009cabb756f0a5ff44bc8501829c7873236e416d44ead1a7ec71361f840` 和 [RTW 原答案报告](/private/tmp/sea-summary-live-integrated-20260915/summary-search_515b0c91-94b4-49ec-839d-6a8eda93908d.json) SHA `f03d64402c0b4875451c20c75f66cfd6ada2fbe47f9e1fa037b5050bc8894716`。生成的 [十二格见证](/private/tmp/sea-summary-live-integrated-20260915/summary-matrix-witness.json) SHA `6e5a1ada0ab2f6eb47e336f8417177f1f4afeec6a98ff8d9a235e25b36766531`：实际模型 usage `1696` prompt、`64` completion、`1760` total tokens，模型 `4802ms`、RTW 端到端 `7795ms`。原文只有 `Evidence`，模型答 `"Evidence" appears in the source text, ranked highly among search results.`；“ranked highly”不由这段原文证明，故即使引用结构和 RTW 耐久化均通过，也不能称答案可信或上线可用。

执行命令：`go run -mod=readonly ./cmd/search-summary-matrix-witness --usage-report <同轮DC↔RTW报告> --rtw-report <其原RTW答案报告> --output <新0600路径>`。以这两份真实私有文件设置 `SEA_SUMMARY_USAGE_REPORT`、`SEA_SUMMARY_RTW_REPORT` 后，`go test -mod=readonly -race -count=1 ./cmd/search-summary-matrix-witness` 和对应 `go vet` 退出 0；反例拒绝改写 quality gate、RTW 源字节 SHA，或把 `tools` 交付标成 summary。

本文件是**执行见证而非正式相关性评测输入**。RTW 报告不含冻结 qrel、判断池完整性、真实 query family/split/请求时间，也没有可供十二路径共同使用的同版全语料案例；因此这里没有为了调用 `EvaluateSearchMatrix` 编造 `CaseIdentity`、切分时点或假语料快照。引用 pack ref 只约束这一条已发布来源的持久证据，不等于可横向比较的整个检索语料。要进入正式 S09/S11/S12，须另交同版全候选判断覆盖与真实评测案例；要填另外 11 格，须各自真实执行并保留独立交付/成本收据。
