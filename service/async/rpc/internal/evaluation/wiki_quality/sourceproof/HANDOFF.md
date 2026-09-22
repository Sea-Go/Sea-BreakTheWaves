# Wiki 质量完整来源证据聚合 v1

状态：**L1 typed 合同与 synthetic Golden**，固定 BTW 开发基点 `229638cf1d63a31882db9470d95cc73d47ce6337`。本子包仅写 `internal/evaluation/wiki_quality/sourceproof/`。RTW FactSet 源已在独立开发头`95ce8fc`固定静态v1 Event并合入其知识开发头`1510e1e`，但仍默认关闭；既有 `btw-warehouse-wiki-quality` 连续 ODS 对 `knowledge.wiki.fact-set.*` 明确 fail-closed，**没有真实目录Event进入当前消费者**、N条当前判断批次/管理员资格、实时UserCenter UID或生产启用证明。本包是独立BTW typed投影，没有把尚未接线的RTW原字节读口猜成正式Warehouse适配器，也不计算 D07 页面达标率。

上述首段记录的是原 L1 基点。后续 BTW 开发头 `d53d288` 已在配置双 FactSet 权威源时让同一消费者按 v2 ODS 父行/sidecar 连续提交并 ACK；旧默认 nil 仍拒绝目录家族。下述新 Reader 切片以此开发头为基点，**未跑 RTW/DC/BTW 真 FactSet 来源轮**；不能把 L1 的历史状态误当当前 v2 代码状态。

## 唯一聚合输入和输出

`Freeze(ctx, Input{Catalog,Judgments,Prefix,Withdrawals}, PrefixAuthority)` 接一个**显式 FactSetRevisionID**及目标 `WikiRevisionID + SourceScopeRevision`。Catalog 固定完整获准 `SourceRevisionID+ContentSHA` 列表、按 RTW 已固定公式由 `SourceRevisionID\x00paragraph:N\x00quoteSHA` 得出的 FactID/required、已由 RTW 权威 Reader核原 payload JCS 字节/SHA，以及 Catalog 原 EventID/原发射字节 SHA/Event JCS SHA/真实 DC offset。每个 required FactID 必有独立 `JudgeRevisionID=HeadAtCutoffRevisionID`、同 Wiki 目标和 scope 的原判断 EventID/raw/JCS/offset；不能拿目录 Event 或一条判断替全部 Fact。Catalog/判断 Event 的原字节进入输入核 SHA/JCS，聚合 Receipt 只保留哈希及引用。

`Prefix` 的批次范围必须从 offset1 连续到 `through_offset`，每批携 JCS batch/hash 与 ODS receipt SHA；`selected` 只列目录及其判断 Event 的真实位置，逐一对应上述 EventID/JCS，不能独自冒充完整前缀。`cutoff_offset` 限定所有判断位置和目标/Source 撤回快照。真正的 `PrefixAuthority` 需要从**同一个** DC Producer、独立 Wiki ODS PG cursor 和已 ACK 前缀实际读回批次索引、提交/ACK watermark后返回同 JCS index SHA；nil、空洞、错 membership、未 ACK到through或证据SHA不一致均拒根。当前测试 verifier只是合成 seam，不能给正式系统资格。下一 Reader须在 cutoff 下重建 `(WikiRevisionID,FactID)` 每条当时当前 judge head，验证没有同截止内较新的漏入 Event；本包只能核输入所钉 head ID 与判断 ID相同，不能从活动 head猜历史版本。

`WithdrawalSnapshot` 对目标 Wiki 和完整 Catalog 来源表逐个保留 `available|withdrawn|unknown` 的**截止时点**状态；撤回/待定时聚合仍可留 source proof，但 `quality_state=not_evaluable`。`facts_complete_declared=true` 明确是 RTW 管理员 JWT+名单的目录**声明**，原 Source quote byte span/FactID/JCS和获准范围核对不能机械证明没有漏事实；`rtw_actor_id`保留原 JWT userId 字面，不冒称 UserCenter UID。任何成功 L1 fixture 输出也固定 `evidence_level=rtw_dc_source_proof_only,quality_state=not_evaluable,activation=none`，`DecodeReceipt(raw,sha)`拒重哈希自改 observed/未知 tenant 字段。

双来源、两个 required Fact/一个 optional Fact、Catalog offset5、两判断offset6/8、前缀1–10、cutoff8的合成根 SHA=`5e81c4a5bbacf398d1b3a91e930be36012fd6fdd821bc7a9259e7c15202723c0`。根SHA、目录 payload JCS SHA、各原 Event raw/JCS SHA 和 ODS batch/receipt SHA 分域；此数值仅锁合同字节。反例检验缺 required 标签、重复 Fact/Event、跨目标/scope、旧 head、错原 SHA/JCS、单选中事件假前缀/批次空洞、过早 cutoff、伪 watermark与撤回资格。

RTW 生产者的静态 FactSet Event/Worker 私有原字节 GET与现有AdminJWT名单声明已冻结在开发分支，**新目录事件正式派发仍默认关闭**。数仓 owner须先让**同一** `btw-warehouse-wiki-quality` consumer识别 FactSet家族，按整个共享 producer 连续位置核 RTW 原 Event raw/JCS/payloadSHA、PG 同事务落 Catalog/技术跳过并 ACK，之后才能开RTW派发。此后 RTW+DC 的实际 Authority Reader才能把目录 Event与 N 条判断 Event的独立 offset/原字节及截止 head投此包；现有评测 `wiki_quality.AuthorityReceipt` 的单Event SourceProvenance不足以代替这份聚合证据。即使这一链路真通过，D07人类事实真值仍需完整目录声明的审阅资格、逐 Fact 评阅一致性/质量样本和人工 Release边界另签。

本地仅跑 `GOMAXPROCS=2 go test -p 1 -mod=readonly -race -count=1 ./internal/evaluation/wiki_quality/sourceproof` 顶层0，日志SHA=`84798a2f361cb53033fbdf6b8a9ad469b560f3c0ad72929ecda5020103abc1d4`；`go vet`顶层0/空日志SHA=`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`，diff check退出0。未启PG/CH/Ollama/Next，也未接真RTW/DC Reader。

## 显式目录版本的真实来源投影切片

`AuthorityReader.ReadPinnedSource(ctx,PinnedRequest{ModuleID,PageID,WikiRevisionID,SourceScopeRevision,FactSetRevisionID,CutoffOffset})`只返回`SourceProjection`。`PGODSReader`在**同一个** PostgreSQL repeatable-read/read-only快照中读 `btw-warehouse-wiki-quality` 的 committed cursor 与 `ridethewind.knowledge` 从 offset1 到显式 cutoff 的每个 append-only `ods_event`，连 `ods_fact_set` sidecar一起读；v1/半迁移 schema 在查询前硬拒。当前单次 cutoff 最大4096个位置，超界硬拒以避免无界载入。逐位置核连续 offset、DC Event规范JCS/InputHash、accepted技术Receipt/offset、verified Catalog或Judgment原Event字节/rawSHA/JCS与父行status。Catalog sidecar 的 RevisionID/WikiID/Scope/payloadJCS SHA和 Event v1 payload逐一相同；技术skip遇未知质量/目录家族拒，不能吞作普通技术事件。

RTW Worker 私有GET给**原发射**目录与每条目标判断 Event，逐个和已提交ODS父行原字节/rawSHA/Event JCS SHA对照；`RTWHTTPReader`另用管理员认证调用现有 `/v1/knowledge/modules/{module}/wiki-pages/{page}/fact-set-revisions/{FactSetRevisionID}` **显式历史**GET，把完整目录payload规范JCS、EventID/rawSHA/JCS/FactSet payload JCS四域回比。管理员认证账号字面仅沿 RTW `actor_id`保留，不是已验证UserCenter UID。原 Judgment v1没有 FactSetRevisionID/SourceScopeRevision；投影先核目标Wiki及同 Catalog FactID、SourceRevisionID/contentSHA、locator、Source byte start/end、原quote/quoteSHA、Wiki内容SHA，才派生该scope。目录必需事实须每条有判断；同 Fact 重评按已运输前缀中的 `JudgeRevision=1..K`和`BaseJudgeRevisionID`逐前驱核对，缺中间版本或同 Fact 换 JudgmentID 均拒。

`SourceProjection.Transported`刻意使用 **截至 cutoff 已运输前缀中的最后判断** 语义，不包含现有`JudgmentProof.HeadAtCutoffRevisionID`。RTW在截止位置前可能已创建但尚未运输下一判断，活动head也可能在截止后移动。ODS committed cursor不能替 DC ACK；ODS同源全前缀索引SHA也不能替 DC 原 batch/交付收据，故本 Reader 没有 `PrefixAuthority.VerifyPrefix` 实现、不调用`Freeze`、不发页面质量 observed/D07。

DC owner 的只读口已在独立候选 `feat/event-ack-prefix-20260916@3702503f13d1748dbddccfb7377cc5f3654013ac` 固定：`GET /v1/event-consumers/{consumer}/acknowledged-prefix?producer=ridethewind.knowledge&cutoff=N&from_offset=M&limit<=128`。它按同一 DC PG snapshot证明 consumer ACK watermark≥N，返页内完整 Event/whole-JCS InputHash、原ReceiptID/ReceivedAt及覆盖页的旧 `delivery_receipt` batch边界/hash/AcceptedAt。BTW的`HTTPDCAckReader`按现有平台 service token只读GET；`ReadAcknowledgedPinnedSource`对固定cutoff从1逐页取完，同 producer/consumer/cutoff、无跳 offset、页间重复 batch receipt 相同，每个DC Event JCS/InputHash/ReceiptID/ReceivedAt和**第二次**ODS不可变前缀快照逐位相等，并重算截止内已完整取到的原batch hash。穿越cutoff的末批只能由 DC 服务器校其完整原批，BTW核同批receipt和交叉页一致性。它仍只产生 `AcknowledgedSourceProjection`，标签仍是最后**已运输**版本，不宣称 RTW as-of head。

此 DC 接口独立叶子已签真PG但**尚未并入固定 DC 开发头、未同RTW/BTW做真来源轮**；本 BTW HTTP 调用测试是同形 `httptest` fixture。未完成三仓交付前不称 live Freeze 或可启用 D07。

RTW owner 还需给截止条件下的 `(WikiRevisionID,FactID)` 历史 as-of head/资格与撤回快照的可读证明，或约定可检验的源事件水位/并发锁边界；活动head GET不能回答过去截止。DC交付水位和RTW as-of资格都签过，才把已运输标签升成`JudgmentProof.HeadAtCutoffRevisionID`、补历史`WithdrawalSnapshot`、调用`Freeze`得到仍为`quality_state=not_evaluable`的源证据根。管理员`facts_complete=true`是获准Source范围的声明，不是无漏事实的客观真值；D07真人审阅另由样本、评阅资格和Release边界验收。

本叶子验收仅为**L1 RTW静态原Event fixture + BTW真实PG读代码/DC只读HTTP接缝的纯来源fixture/race**，没有启动PG/RTW/DC三仓真轮。双Source/双required Fact、Catalog offset1/判断offset2–3/重评offset4用同一 v1 JSON结构，cutoff3和4分别选最后已运输判断；负例包括缺完整位置、DC InputHash篡改、Catalog sidecar scope替换、历史FactSet换目标、Worker原字节替换、判断Source byte span错配、缺required Fact和重评前驱缺失。DC分页反例还含未ACK、水位提前、跨页换 producer、InputHash替换、旧batch hash错误与重复收据冲突。最初ODS/RTW代码单包 race退出0原日志SHA=`4019df027eff0792e484104d53f452d0acc527830b510df899d86f7182947e9e`；加入DC页口后`GOMAXPROCS=2 go test -p 1 -mod=readonly -race -count=1 ./internal/evaluation/wiki_quality/sourceproof`退出0，原日志SHA=`8fd51654dc2cdb319603e16a6fa720b852385d4aa3d2f4dfeff2d7dd62036ded`；`go vet -mod=readonly`退出0/空日志SHA=`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`，`go mod verify`输出`all modules verified`，staged diff check退出0。验收层级止于代码/fixture，不是已上线真实 AuthorityReader或真人质量验收。


## 内容开发集成与二次ODS原字节门禁

独立候选`feat/wiki-sourceproof-live-reader-20260916@d6c45e8504b5940ef3ec0921dc5f0b64c802cad8`三笔`b0cee84/e4594cf/d6c45e8`从固定内容开发头`d53d288`出发，远端洁；根按序cherry到现有内容开发头`c77cc1c`为`af78c61/45bb157/717c43d`。只读同行未发现P0–P2；其P3指出二次ODS快照原先只对`offset/EventID/DC InputHash`索引，即使DC原receipt wire另变而ID/JCS不变也可误接。开发修复`eeb8b4e2b014e37b63be43c480d120dcbe666db7`同时冻结第一次完整`ODSRow`前缀JCS SHA（包含原Event/原Receipt字节、原RTW Event、目录payload与sidecar），第二次逐行verify后重新hash并核相同；已提交cursor可以自然向前，固定cutoff的证据不能变。纯fixture二次只给DC技术Receipt追加JSON合法空格时，原索引不变仍确定性`ErrPrefix`；同包race、vet与diff check顶层0。

DataCenter已有开发集成只读ACK页`feat/platform-observability-20260914@ebea5f8d`，它在旧025未迁030/停用trigger和坏RawQuery时拒假证据，单仓另一PG16测试已通过。这里的BTW Reader仍只通过`httptest` DC同形页/静态RTW原Event与fixtureODS测接口：**内容开发新HEAD尚未在同一真PG16/RTW Admin Worker/DC ACK接口三仓同父调用`ReadAcknowledgedPinnedSource`**；原第177节RTW→DC→BTW真Catalog ODS L3发生在Reader合入前的`d53d288`代码，并不自动给本新Reader签L3。RTW as-of判断head和截止撤回资格需独立来源证明；在签过之前不填`HeadAtCutoffRevisionID`、不调用`Freeze`或D07。

## 显式同父真实Reader脚本合同

独立叶子`feat/wiki-sourceproof-real-source-20260916`新增`real-source-acceptance.sh`与`TestRTWRealFactSetAcknowledgedSource`。脚本只在RTW Holder显式`SEA_BTW_SOURCEPROOF_READER_ROOT`指向本叶子、且同时提供旧11键`SEA_RTW_REAL_WIKI_FACT_SET_FIXTURE`和新12键`SEA_BTW_SOURCEPROOF_REAL_FIXTURE`时运行；它启动**一个**隔离PG，先运行旧`TestRTWRealWikiFactSetSource`落全前缀父行/sidecar并ACK，随后不关闭PG、也不清表地运行新SourceProof真Reader测试，最后stop/status留原日志与PG数据目录。默认旧Holder仍调用原`wikiqualitysource/acceptance.sh`；两种fixture的结果路径不同，都是测试专用同目录的0600/O_EXCL文件。

新12键仅比旧11键多`admin_token`，它是RTW当前AdminJWT的显式**历史FactSetRevision GET** bearer；`rtw_token`继续只用于Worker原Event GET，`dc_token`继续只用于DC ACK页。新测试先逐字面拒重复、未知`tenant_id`、缺Admin与两fixture交接值不一致；旧仓库结果必须已记录同N Catalog1/Judgment3/技术N−4/ACK N。Reader随后用真实RTW Worker原字节、Admin历史目录、BTW PG repeatable-read ODS和DC service-token ACK页在显式N同父核目录/两条required Fact已运输判断；另核baseline Judgment在目录前且不被选为目标。结果固定`quality_state=not_evaluable`及`human_catalog_verified=false,d07_evaluable=false,production_verified=false`，包括ODS完整原字节证据SHA、DC ACK Event索引SHA和批次收据数量，不输出任何token。它仍未证明RTW截止时的未运输head、Source撤回资格、真人无漏事实或D07。

旧RTW GET/新DC ACK页代码合同、fixture拒重复/未知字段聚焦单测、`bash -n`和diff check已核；新测试在无显式fixture时只会SKIP。纯SourceProof包`go test -race -count=1`退出0日志SHA=`3b14073973181e8eb936112edfd9a81c653a88e20689c69fec32edfafcfec0b5`，vet退出0/空日志SHA=`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`；这份纯包结果和下述同父真PG验收分开。

## 唯一同父真RTW→DC→BTW Reader验收

固定输入为独立 RTW Holder `ae43acbb6aa63984609f8f5649eb2959333d3863`、DC 开发头 `ebea5f8dfbb0a600b3faca90c235f2ad4a8417b5` 与 BTW 本叶子 `dd0bb3147dd00e51ed8db03f6b29fa6ab7619365`；显式 selector 把原 11 键和 AdminJWT 12 键 fixture交给本脚本。**只跑一轮**临时 RTW/DC/BTW 真PG源：RTW `TestRealHTTPKnowledgeWorkflow` PASS原日志SHA=`a79fb2cce070b0e595b2e7ab57cf9b123f42714f45558fb68a579a6c3c03dfc8`；BTW同PG先运行 `TestRTWRealWikiFactSetSource` PASS日志SHA=`c2941379548629257970bc788295e896373fed137b4d9c9c2b0e3861ca3d26ad`，后运行 `TestRTWRealFactSetAcknowledgedSource` PASS日志SHA=`2d67dcac78c6f3d6bf859a1419c4e02c70e5b7a12d303aafef9734c19398bad1`，两包vet空日志SHA=`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`。证据目录分别是 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T/sea-knowledge-acceptance.WrC7Ja` 与 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T/sea-wiki-sourceproof-real.EyIvks`；两处stop.log SHA同为`ca19178a35ab4153b75b494963b66ce8243e1b94c173107c87b44d09db23652d`，分别再次执行`pg_ctl status`均返回3/no server。

RTW留存的`observability/wiki-fact-set-cross-source.json` v3 SHA=`e8f9bec02e2b1a9552cccfb9ea43cfcbec5b400af3039b07fa76e8852cf0b653`，锁真DC生产者连续14个事件=目录1、判断3、技术10、同一 BTW质量consumer ACK14、完整DC历史批次1。报告的`sourceproof_reader_verified=true`，Reader与RTW独立回查DC Event索引SHA同为`b9546c3d22642b6a9a0e5211b0fcfdf98ac08e03c58066730809527290735ce1`，Reader同一ODS原字节前缀SHA=`55e4f3c5fc5663a84b04165e6a19c76b49c7a6e7e168c4a2a44e9429a7d0a15a`。RTW在运行时读取SourceProof另一个0600/O_EXCL结果并断言目标两Fact分别有最后**已运输**判断、目录+两目标Event共3个被选、`quality_state=not_evaluable`以及人工完整性/D07/生产三个false；RTW报告签其结果原字节SHA=`b55ea564fad41622c995ed8c2a7ec3d72f0f5fd2acf9782c1e50e7ed0799ab3e`。该独立结果文件位于RTW测试`t.TempDir`，测试退出后已被框架清理，因此现在**不能再单独重算它的原字节SHA**；留存的RTW报告和测试断言给出当轮交接证据，但不应写成可持久复核的SourceProof结果字节工件。

这轮签的是RTW历史目录/Worker原Event、DC完整ACK前缀、BTW同PG ODS父行/sidecar与Reader投影的L3来源链。截止时RTW是否另有未运输Judge head、Source撤回资格和人类判断的一致性仍无独立权威证明；Reader继续只返回`AcknowledgedSourceProjection`，**不调用Freeze，不产生D07页面质量达标结论**。

## 后续脚本与收据门禁修复

前述真PG轮固定在BTW`dd0bb31`字节；同行只读审查后，叶子再收紧两处测试门禁。退出陷阱现在保留原测试失败码、要求`pg_ctl stop`成功，且紧接着`pg_ctl status`必须返回**3/no server**才能让脚本保持退出0；`started`在`pg_ctl start`前置位，覆盖启动返回错误却已留下postmaster的情况。任务专用假`pg_ctl`探针依序确认stop成功/status3可退出0、仍运行/status0必退出1、stop失败/status3必退出1、start失败仍尝试stop且退出1；此探针没有启动真PG。旧Warehouse结果另严格固定八个字面JSON键，拒重复/未知`tenant_id`，把全部三条quality EventID和目标两条Catalog quality EventID数组与新12键fixture逐项对齐；旧11键fixture、新12键fixture及旧结果文件均必须是**精确普通0600**文件，0400/0700反例拒。

修复后的纯SourceProof包race退出0日志SHA=`594de0c107c327d416f06e7bc0d3696b94f68db41d3d423fba9b4e298c82f638`，vet退出0/空日志SHA=`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`，`bash -n`及diff check退出0。**这组新代码尚未重新跑三仓真PG**；上段L3不能自动转签到后续修复提交。


## 内容开发集成最终同父复验

独立BTW真源叶子远端`feat/wiki-sourceproof-real-source-20260916@62b73ef4798cbfa33f051eb198c2fb3cdc443bb0`洁；主任务把`dcc986e/dd0bb31/0f33cab/6334476/62b73ef`五笔精确合入既有BTW内容开发分支为`ece6039/feb0962/c4a340d/7516682/3fd2827`。最终本包纯race PASS/testSHA=`0464c6a59f7a2586c2a2ee11631078293686841d64f3276d89952abe66fb2faa`，vet空SHAe3b0、bash-n/mod verify/diff0；真源script的EXIT trap现在**失败必拒exit0**，旧八键技术结果逐质量EventID数组和精确0600门禁已在主线代码实际运行。之前独立叶子基旧dd的本机L3只说明原父轮；下面是从新的BTW开发代码头另签。

固定RTW知识开发`4d723a2`×DC平台开发`ebea5f8`×本BTW内容开发`3fd2827`新开两侧PG16同父：真RTW14 Producer offset、Catalog1＋目标质量2＋旧基线质量1＋技术skip10，DC历史批ACK1/同consumer cursor14；本脚本**同一PG**先旧Warehouse真源测试 PASS/testSHA=`2d4dfbfe84fc1988e6f032e8daa29949ceaebe645d6fd18a7b68a508b0e909ad`，再本包SourceProof真历史Reader PASS/testSHA=`8e31638f53183f784caa15086a491360f4761c80f090f7a29a57aaef0b787afa`，vet空SHAe3b0、BTW PG stopSHA=`ca19178a35ab4153b75b494963b66ce8243e1b94c173107c87b44d09db23652d`/status3；证据`sea-wiki-sourceproof-real.xvFFPk`。RTW父脚本exit0/testSHA=`1aaa665968fee12400c941814f2f14678eca55ee9273cfab020c97383e047a45`、0600 reportv3SHA=`1cce5c7fc63054955741df136eabe5d87c83f4de4365af345153cb1a8176282d`、PG stopca/status3，证据`sea-knowledge-acceptance.OzBtUK`。RTW独立DC GET与Reader全DC索引 SHA均为`390a5273eb25d29fc4e726c81bebfa40ed0c360638f5575b578ea2895f4cabab`，Reader ODS前缀索引SHA=`4c8c302359d38599905f56c9c5927d325578af02165499b09e7e649c4bb2c3ec`；报告`sourceproof_reader_verified=true,sourceproof_result_sha256=638923b0b4e706a016127ef57c158838acefdf3b6c768d9ebba113009ecbb182`。旧＋新0600结果测试退出后随RTW`t.TempDir()`删除，报告的结果SHA只可由当轮源断言背书，不再作为留存原字节工件。

这只接纳**BTW真实ODS+DC已ACK源与RTW原事实目录/判断最后已运输投影**的本机L3。结果仍`quality_state=not_evaluable`且`human_catalog_verified=false,d07_evaluable=false,production_verified=false`；未证RTW业务as-of头/截止撤回资格、真人目录列全/逐事实质量、CH/dbt离线DWD/ADS或生产部署，`Freeze`和Dataset仍不调用。
