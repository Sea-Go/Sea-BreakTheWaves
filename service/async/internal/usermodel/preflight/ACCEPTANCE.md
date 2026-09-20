# BTW SubjectRef v2 阶段一 usermodel 预检证据

固定输入：BTW 开发集成 `f51723e0a1db3b5029342ef8207fb2255be79cec`；七份 `migrations/usermodel/001..006` 源 SHA-256 `fdb6615e728a8a0714cb8b5c491199fa67b0e1d84f8f9d8facbb1f5545e4ab97`。隔离 PostgreSQL 16.14 应用七份 SQL 后，`pg_catalog` 的 23 表列/PK/FK/唯一索引契约 SHA-256 为 `89cb6c0c8447389c617a4d7531a80db052d0da51de448cf8c6ec265c21e9e5d1`。逐表键见 `SCHEMA_INVENTORY.md`；19 表含完整三字段主体，1 表为外部未映射事件，2 表仅有 authority/tenant，1 表是无主体 coverage prefix。

初版远端 HEAD `1150534` 的只读审查发现五处 P1 漏报，**该轮不能合入**。当时空库 `6feed342e4361ed7e74a6093625a699de69b899e168fca985bbf206656bbc564` 与原负例 `11ce826cf16f99682e794c853ed67151e0a7e5076ed11d9770bc7a19a2b2f66d` 只留历史 provenance，不作为最终预检结果。

最终验证命令：`bash internal/usermodel/preflight/test-postgres.sh`。脚本创建并停止 `/tmp` 隔离 PG16，先从精确 `1150534` git archive 构建旧 CLI，再以 `GOMAXPROCS=2` 执行本包与 CLI 的 `go test -mod=readonly -p=1 -race -count=1 -v`、同范围 `go vet` 和 CLI build。每个 P1 负例在独立随机 schema 中用旧 migration 合法插入；旧 CLI 实际返回 L1=0/L2=0，新 CLI 在相同库的只读 RR 快照中命中目标 L2=1。CLI 默认离线拒绝、stderr 单行 JSON、显式只读审计均通过；无生产连接。

| P1 负例 | 旧 1150534 | 新规则与结果 | 紧凑内容 SHA-256 |
| --- | --- | --- | --- |
| 状态 V=2，Outbox 版本 0/2 | L1/L2=0 | `outbox_version_gap` L2=1；保留唯一键、count、max，并补 V>0 时 min=1 | `d6be2bc334d4263b6656b3a859b309ae8753f6c998423708d1d4196f5662624a` |
| coverage_event 指向 pending 且 accepted_version 为空 | L1/L2=0 | `coverage_event_version` L2=1；要求 accepted 状态及 `IS DISTINCT FROM` 版本一致 | `8f0dbf5543ed106d24ce948b85d1ed2fb330710a8fe96b1b35a5003e5c1a5c60` |
| 声明正序号 source_sequence=1 的事件无整条 watermark | L1/L2=0 | `missing_source_watermark` L2=1；从事件反查 | `58bc832bd1953a60bfd308038908a1b71ff7066ee8858c50544ba2e2932ec256` |
| 已绑定 unmapped 与 binding 同外部键指向不同主体 | L1/L2=0 | `bound_unmapped_binding_disagreement` L2=1；两边主体状态行均存在 | `a356a7c44e0225dbd8e3da17eedd6f9ff3b89a91284d077e71077c26f31b007f` |
| 已绑定 unmapped 缺 binding | L1/L2=0 | 同一规则 L2=1 | `d1336cdfd2568446a6475faeda9aa5eafc2d88f2f42dd181bd98d05a7440bc4c` |
| 已接纳非 retract、无已接纳 successor，却缺 active_fact | L1/L2=0 | `missing_current_active_fact` L2=1 | `a3550c670b0c73b84e8dc7f9fcbcdeb6862ae72eb7a8ae9e06f5ee067588213f` |

后续集成审查提出“真实 Store 会在 PG 存 `source_sequence=0`，原 `IS NOT NULL` 会误报”的假设。固定 `f51723e` 源码实际用 `NULLIF($13,0)` 存 PG `NULL`，旧 SQL CHECK 也禁止物理 0；因此该假设已撤回，`>0` 是更显式的合同表达，在有效旧 schema 上与原条件等价，**不计作第六项 P1 修复**。真实 `Store.Append` 正例证明逻辑输入 0、PG `NULL`、stateVersion=1、Outbox=1、active=1、watermark=0；旧 `1150534` L1/L2/L3=0、原 `IS NOT NULL` 判断命中 0、修后 L1/L2/L3=0，23 表合计 4 行，报告紧凑内容 SHA 为 `d1b7cce3805dc7cebb14cb0f2496d26cc1a6aefad8227990d102831fe0d2d639`。正例最初两次测试失败分别由 mixed-case fixture schema 未正确设置 pool search_path、把 PG NULL 扫入 `int64` 引起；这两处测试夹具错误已修，最终聚焦与全量隔离 PG16/race 均通过。

防误报验证：`contiguous_sequence=1/max_seen_sequence=3` 且尾部 pending 的合法水位空档，旧 L1/L2=0、新 L1/L2/L3=0；已接纳纠正再撤回的前驱链最终没有 active 行，新预检也不报。活动 Ontology head 与主体 state 存在而缺 projection 时，旧 L3=0、新 `ontology_projection_missing` L3=1，仅标重建候选。原 synthetic/冲突/断键负例仍被阻断，新紧凑内容 SHA 为 `35e2b8454d50c1bcf1dee8516ca60bbec16082cb83406100989435063d68b9a5`（L1=9/L2=3），不归并早期样例。

报告 `report_sha256` 是将该字段置空后用 Go `json.Marshal` 生成紧凑 JSON 的 SHA-256，含全部 finding、逐表扫描行数及总行数；它**不是 stdout 文件字节 SHA，也不提供安全匿名性**。最终七份 migration 空库的 `report_sha256` 为 `466533c9d054b72110ee0577064a0a3eb589d9c907237fa3b1761ab55970cca9`，L1/L2/L3=0、23 表扫描合计 0 行；实际 CLI `report.json` 字节（已填 hash 字段与换行）的 SHA-256 为 `34b8a84c1424293d877525fb558c7eb7b58b7fbbf3fa7a28d58f4267aa441658`。原 synthetic/冲突/断键负例 23 表合计 13 行、内容 SHA `35e2b8454d50c1bcf1dee8516ca60bbec16082cb83406100989435063d68b9a5`；这些空/异常行数及两种空库 SHA 在最后全量 PG16/race/vet/CLI 重跑后与 `502f736` 相同。合法非空且 state_version=0 的独立 fixture 合计 1 行，紧凑内容 SHA 与空库不同，测试有明确断言。聚合计数可能揭示小群体，需当作受限证据；报告不含 DSN、UID、turn、event_body 或 payload 值。

局部状态：`LOCAL_VERIFIED`（旧版对照、隔离 PG16/race/vet、只读 RR 规则、CLI 离线门禁与受控 JSON 日志）。真实 BTW 生产表、线上 RTW UID 签发、外部 Recommend pair 批准、旧不可变工件/hash、触发器行为、Collector→DC 下钻和完整 OBS-r3 链路均为 `NOT_VERIFIED`。本阶段没有迁表、双写、v2 行或 serving 切换；独立分支留给集成负责人审查与合入。
