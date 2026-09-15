# RTW 源版本 V 与 DC 已确认截止 C 的对账交接

本叶子工作区仅修改 `internal/evaluation/wiki_quality/sourceproof/` 的来源校验和真实来源验收脚本；RTW Worker 私有源版本读口和 DC 只读 ACK 页是依赖，不修改二者。产品当前按单平台处理；固定 `tenant_id=platform` 只承接已有 v1 事件/仓库兼容字段，不构成租户模型或用户身份。RTW 用户 UID、管理员 JWT 的 actor ID、模块/修订 ID 各有不同语义，这条内部对账不能把它们合成一个 SubjectRef。

`ReadDCCurrentAlignment` 先调用既有 `ReadAcknowledgedPinnedSource`，在显式 `cutoff_offset=C` 核完整 DC Producer ACK/批次和 BTW 同一 PG ODS 原字节父行/FactSet sidecar；随后再读一次 ODS 固定前缀并核 JCS SHA 不变，找同一 RTW 模块在 `1..C` 已运输事件中的最大 `aggregate_version=V`。它向 RTW Worker 现有 service-token 私有 `POST /internal/v1/knowledge/wiki-quality/source-version-witness/read` 要**读时版本正好为 V**的候选；不使用公众 UID/JWT。RTW 返回较新版本、缺版本、未交付事件、读时对象版本改变或任何私有读失败，均拒。

成功候选必须在 `1..V` 无洞、无重复、全部已送达；每个事件的 EventID/type/whole Event JCS SHA 与 DC/ODS 截止内同模块对应位置逐项相等。RTW 历史 FactSet Event 的原 raw/JCS/payload SHA 对既有 SourceProof 已核目录，所有获准 SourceRevisionID/ContentSHA 与历史目录逐项相等；required Fact 当前 Judge head 必须就是 `C` 截止内最后已运输的修订及原 Event raw/JCS SHA。Source/Wiki/模块的读时撤回位必须逐个有同一事件前缀内的 `knowledge.content.withdrawn.v1` 支持，单改对象状态而未交付撤回事件不得通过。候选最多4096事件、每页最多128；超界拒，不能把截短清单当完整前缀。

输出 `DCCurrentAlignment{SourceVersion:V,CutoffOffset:C,DCIndexSHA256,ODSEvidenceSHA256,RTWCandidateJCSSHA256,RequiredHeads,SourceAvailable}`。`SourceAvailable` 只表示本次**同一已确认事件截止**下 RTW 的 Wiki/来源/模块没有撤回，且 required 当前判断 head 已运输；`quality_state` 始终为 `not_evaluable`。它不证明管理员事实目录没有遗漏、真人质量、一致性、D07 页面达标或生产激活，也不替原 `Freeze` 套用 observed 标签。后续数仓/评测 owner 只能以明确 `C,V` 和四域原 SHA 交接，不得用后来活动 head 倒填历史截止。

本地 `go test -race ./internal/evaluation/wiki_quality/sourceproof -run '^(TestDCCutoffAlignment|TestRTWSourceVersion|TestRTWRealWikiDCCurrentAlignment)'` 在未给真实环境时只验证合成 source/DC/ODS 竞态反例并跳过真轮；`go vet`另核。真实父轮必须由 RTW Holder 创建一个独立空0700普通目录并显式传 `SEA_BTW_WIKI_ASOF_EVIDENCE_DIR`，由原 `real-source-acceptance.sh` 在旧仓库测试、SourceProof 和可选 DWD 包**同一 PG 未停机**时调用 `TestRTWRealWikiDCCurrentAlignment`。成功才将两份 O_EXCL/0600 文件 `rtw-source-version-candidate.json`、`dc-current-source-alignment.json` 留在该目录，且脚本关闭 PG 后 `pg_ctl status=3`；失败不得产生绿色交接。源真实结果和 RTW 父桥、Docs 总验收应记录各自 Git HEAD、真日志/原文件 SHA 和 stop/status，不能从单包 fixture 推断跨仓验收。
