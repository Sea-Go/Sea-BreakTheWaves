# Wiki FactSet 离线来源数仓 v1

本目录只把已运输的 Wiki FactSet 目录和逐事实管理员判定做成可重建的来源视图。它不计 D07 页面达标率，不生成训练标签、Dataset、Serving、活动指针，也不改变 RTW 的 Wiki 编辑头或人工 Release。

```text
RTW 原 Event / 历史 FactSetRevisionID
             │ 原发射 JSON / raw SHA / Event JCS / payload JCS
             ▼
DC ridethewind.knowledge 连续 accepted Event + 原技术 Receipt + ACK 前缀
             │
             ▼
BTW wikiqualitysource: PG ods_event(全部 offset) + ods_fact_set(目录 FK)
             │  PG 只读 Repeatable Read, SourceProof Worker/Admin GET + DC ACK 页
             ▼
wikiqualitydwd.Freeze: 明确 WikiRevisionID / FactSetRevisionID / Scope / cutoff
             │  同 SHA 第三次 ODS 只读快照；四个 O_EXCL/0600 原文件
             ▼
新 ClickHouse generation: prefix(含技术 skip) / catalog facts / judgment versions
             │ dbt: 来源前缀 DWD、目录 DWD、判定 DWD → 来源 ADS
             ▼
每个 Fact 在 ACK cutoff 的 last TRANSPORTED K，quality_state=not_evaluable
```

## 来源、粒度与事实语义

PG owner 是既有 `internal/warehouse/wikiqualitysource`：同一 `btw-warehouse-wiki-quality` consumer 在 `ridethewind.knowledge` 的真实连续位置写 append-only `ods_event`，目录事件另在同一事务附 FK `ods_fact_set`；先 PG commit、后 DC ACK，ACK 丢失则对原 Event/JCS/Receipt/sidecar 字节重放验等，不另开消费者。开启 FactSet 前必须先通过 ODS v2 schema；旧 nil 配置拒整个目录家族而非记技术 skip。技术 skip 的 offset 留在 `prefix.jsonl` 仅证明 `1..cutoff`，不进入事实表或标签数。

离线出口 `internal/warehouse/wikiqualitydwd.Freeze` **直接调用**当前 `sourceproof.AuthorityReader.ReadAcknowledgedPinnedSource`：它已分别核 RTW Worker 原 Event、Admin **显式历史** FactSet GET、PG 只读 RR `ods_event` + sidecar、DC 原 ACK 批次与逐页索引。出口再次读取固定 cutoff 的原 PG 前缀，并要求整个 `ODSRow` 原字节 JCS 指纹相同；已提交 cursor 可自然向前，但同 cutoff 的 EventSpec、RTW raw、DC Receipt 和 sidecar 任何一字节变化都拒。`Write`只在任务拥有的空目录 O_EXCL 建 `prefix.jsonl`、`catalog-facts.jsonl`、`judgments.jsonl`、`manifest.json` 四个 0600 工件；Manifest锁三流原 SHA、ODS 全证据SHA、DC ACK索引SHA、RTW目录/目标/Scope、cutoff 和保底水位。不存在发布日期或激活指针。

`catalog-facts.jsonl`每 Fact 一行，保持完整获准 SourceRevisionID/contentSHA、原 SourceQuote byte span、required/conflict_group和单一 FactSetRevisionID；`judgments.jsonl`每**已运输**管理员判定版本一行，独立 FactID/JudgeRevisionID/Event rawSHA/Event JCS SHA/DC ReceiptSHA，同目录中的 Fact、目标 Wiki 内容SHA、来源 quote/span逐项核。它保留管理员 `claimed_grade` 和 `assessment` 作为源主张，`actor_id`仅是当前 RTW AdminJWT 的 userId 字面，不是 RTW UserCenter UID。`facts_complete_declared=true`是管理员对固定获准范围的声明；机械核 Source 成员资格、引用字节和所有 required 判定仍不能证明人类没有漏事实。因此三流、dbt DWD与ADS都固定`quality_state=not_evaluable,evidence_level=rtw_dc_source_proof_only,activation=none`。`last TRANSPORTED K`只取 ACK cutoff 内同 Fact 最后的事件版本，不能替代 RTW 当时已产生却尚未运输的 as-of head、截止撤回资格或 D07 真人质量真值。

## ClickHouse/dbt 交接边界

`acceptance.py --bundle <Frozen.Write空目录> --expected-manifest-sha256 <Go Frozen.ManifestSHA>`是本机 CH/dbt 加载接口；caller 必须从权威父轮结果固定并交付 SHA，loader 不用本仓 fixture 的 SHA 代替真来源。读取器严格核四文件原 SHA、字面 JSON 键、连续 prefix/计数、每条 DC accepted Receipt 位置/InputHash、RTW original Event JCS、目录/判定父 offset和来源 Scope；只把经过白名单校验的 RevisionID/Scope/cutoff带入 dbt vars。Loader 使用安全新 generation 名，先查 `system.databases` 再执行不带 `IF NOT EXISTS` 的 `CREATE DATABASE`；如果旧 generation 已存在，就在**首笔 INSERT 前拒绝**。ClickHouse `MergeTree`本身没有唯一约束，`source_prefix_continuous` 的 count/uniq是落地后的拒错测试，不能叫加载幂等。同一冻结源可构建另一**新**generation并核相同原工件 SHA；旧 generation 不续写、不覆盖。

dbt DAG 的四个模型分别为完整 `dwd_wiki_source_prefix`、每 Fact 的 `dwd_wiki_fact_catalog`、每版本的 `dwd_wiki_fact_judgment`、以及每 Fact 选 cutoff 内最后已运输判定的 `ads_wiki_fact_source_evidence`。四个 data test核全位置无洞/无重复、原 Event/Receipt/skip SHA和状态、事实/判定与父行三SHA链接、required Fact皆有判定且ADS取真实最大运输 offset。ADS 显式保留 `admin_claimed_grade`而从不产生 `observed_quality`、`d07_score`、UserCenter UID或训练标签。目标明确为`WikiRevisionID+FactSetRevisionID+SourceScopeRevision+ACK cutoff`；当前 RTW 产品没有租户字段，数仓也不引入虚构 `tenant_id`。

## 当前验收和剩余交接

叶子从 BTW 内容开发 `3fd2827614a0106c6fe2698331b4d2fac5d8ca8b` 出发。Go synthetic 双 Source、双 required Fact、目录offset1、两初判offset2/3、同Fact修订2在offset4、普通技术 skip offset5，分别冻结截止3和5：三流与Manifest两代**原字节** Golden锁于`fixtures/cutoff-*/`，Manifest SHA分别为 `9f79112c2271c1209b89fa0f7a07a80760e09efb0705f80f21cd804314649735`、`6697b6fc2fcb08f245ba65393f108a7e9002733c355448e96f6912bc34ea29e9`。Go race/focused 与 vet退出0；缺 DC ACK、第三次ODS原Receipt字节改动、同目录缺 Fact/旧重评等通过固定 SourceProof及本叶子反例拒。Python loader 五反例另在**还未 CH INSERT 前**拒同generation、未知字段/伪租户、非法 dbt ID、缺 prefix、伪 DC InputHash 和技术 skip 冒充质量。锁定 dbt 1.10.11/ClickHouse adapter 1.9.7 的 parse 检出4模型+4测试共8节点。

真正隔离 CH/dbt 的**合成**验收两轮已结束，实例均关闭：

| 轮次 | 本机证据 | 验收 | 限界 |
| --- | --- | --- | --- |
| synthetic cutoff3+5 | `/private/tmp/sea-wiki-factset-dwd-source-20260916/report.json` SHA `f0c334d0d33d0e7082ca637bcb6ec989c83daf8afd37bbfef163cea47528a7cd` | 各 dbt `PASS=8,ERROR=0`；cutoff3 prefix3/catalog2/judgment2、cutoff5 prefix5/技术skip1/judgment3；两个 Fact 的最后已运输 K 从`1/1`变`2/1`，同gen重插首笔拒 | `L2_synthetic_Go_JCS_real_CH_dbt` |
| 通用 bundle cutoff5 | `/private/tmp/sea-wiki-factset-dwd-bundle-20260916/report.json` SHA `a8e4d3eb76c125f455712cc1d4483fb2c4fb85163859c2a76d208afeffe93f27` | 显式 Golden ManifestSHA进入通用`--bundle`，真CH/dbt`PASS=8,ERROR=0`且后二次同gen重插拒 | 输入仍是**同一 synthetic fixture**，报告`source_authority_independently_verified_here=false` |

两实例的`ch-stop-status.json`均 SHA `719f747e91eff9b2704b89fdd29a4a9a1bc9d42cd8b002021e1bacef8580bd37`，独立显示`process_exited=true,endpoint_unreachable=true`。这两轮没有把真实 RTW/DC/PG14事件再输进 CH；既有 SourceProof Reader的前一真PG链只能证明 Reader，不能自动给本离线 CH 层签同父 L3。生产 ClickHouse、生产 ODS、线上D07/数据仓调度尚未验证。

下一交接是由 RTW Holder/BTW root 在**一个同父**临时PG/RTW/DC来源轮让真实目录1、质量判定3、技术skip10按共享 producer ACK14；在 PG/DC/RTW仍可读时，用已核`sourceproof.PGODSReader`、`RTWHTTPReader`、`HTTPDCAckReader`和由原 Catalog payload给出的明确目标/Scope调用 `wikiqualitydwd.Freeze`，把四原字节工件 `Write`到新任务目录并交其 `ManifestSHA`。父轮再以`acceptance.py --bundle <该目录> --expected-manifest-sha256 <该 SHA>`构建**新的**隔离CH generation，连同RTW/DC/PG真实测试、ACK水位、PG stop/status3、CH进程关闭、CH/dbt Manifest/results原SHA一起验。该轮可签来源同父PG→CH；即使通过，仍不把 Admin完整声明或技术ACK升为`observed`/D07，也不发布Dataset/Serving。
