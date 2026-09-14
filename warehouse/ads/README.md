# WS07-E 效果、质量与实验 ADS 首个冻结切片

日期：2026-09-14。状态：**隔离合成来源的 CH/dbt→SeaweedFS S3→PG 质量 ADS 子链 L2 INTEGRATED；WS07-E/H12 整体 PARTIAL**。本切片交付的是固定分子、分母、来源缺口和不可评状态，**没有线上 CTR、模型提升或实验胜出结论**。

## 工作区域与所有权

- [W0:ROOT] 独立 BTW 工作树 `/Users/edy/Sea/.codex-worktrees/sea-btw-ads-20260914`，基线 `8e1f56a`。[W1:WRITE] `warehouse/models/marts/ads/`、`warehouse/tests/ads_*.sql`、`warehouse/ads/` 专属连续来源 fixture、冻结/验收与本说明。[R1:READ_ONLY] 原 DWD/DWS 和训练集市、`internal/evaluation`、Sea-Docs、DC 查询合同及原始脏树。[D1:DEPENDENCY] 既有锁定 ClickHouse/dbt/SeaweedFS/PG16 工具，只读取其版本与接口。[G1:GENERATED] dbt target、日志和本次报告由脚本生成，不手改。[X1:EXTERNAL] 只用脚本随机端口启动的隔离 CH、SeaweedFS、PG；无共享/生产写入。[N1:OUT_OF_SCOPE] H09 正式来源、WS09-E 指标解释、WS09-F 查询页面、实验分流/结论和业务发布。[T1:TEMP] `/tmp/sea-ws07e-ads-acceptance.iIwfEt/run` 保留为验收证据。
- 主职责 [C4:PERSISTENCE] 是仓内 ADS 表、内容寻址 S3 文件和 PG 唯一 generation 收据；跨 [C3:DOMAIN] 展示/成熟/来源资格、[C7:CONTRACT] H10.a/H12 固定血缘与状态、[C8:VERIFY] 两代真实存储联验。WS07-E 不拥有正式 ExperimentSpec、EvalReport、可视化查询 schema 或推荐生效指针。

## 固定语义与交接

`ads_positive_evidence` 在完整 SubjectRef+impression 粒度核对有效阅读的行为窗口、来源分区/序号和 `available_at <= evaluation_cutoff`；`ads_recommendation_exposures` 只从 **DWD 客户端 `visible=true` 且有 `impression_id`** 的事件开始，精确关联同主体、请求、candidate、item 修订、stage invocation 和 attempt 的 **`served`** 阶段。其每行保存 impression/served 来源与可用时点、DWS 资格/成熟窗/标签修订、评测截止和 `evaluation_state`。没有可核对的 served、来源、成熟观察或标签时，`mature_label=null`，绝不补 0。旧服务端返回、未展示候选、孤儿行为不进入曝光行；可见但未成熟仍可计入“可见”诊断数，不进入成熟可评分母。

`ads_recommendation_quality` 按 cohort、来源分区、定义/标签修订与 `data_as_of` 保存可见数、served 可见数、成熟正/负数、pending、excluded、来源/标签缺口及 `mature_evaluable_denominator=positive+negative`。当前没有经 WS06/08 签收的 ExperimentAssignment，故 `cohort=unassigned`、`assignment_state=missing_authoritative_assignment`、`experiment_effect=null`；DC 页面不能从这些计数自行制造实验收益。定义版本为 `ads-exposure-r1`，不是 WS09-E 全部 49 指标族的替代实现。

来源门禁同时保留最终落地方向：

- `synthetic + synthetic_fixture_complete` 允许本地固定样例产生可评的**技术对照**，manifest 明写 `source_kind=synthetic` 和 L2；数字不代表真实用户效果。
- `observed + verified_complete + coverage_receipt_sha256` 是 SQL 的正式输入分支。冻结器还要求由 H09 owner 提供的**权威覆盖/成熟收据验证回调**；本切片没有该真实适配，故观察到的 H09 来源不能自报已批准并发布。验收仅用合同 fixture 在 SQL 中预览该分支，未冻结/注册为 observed。
- 其他来源/缺收据/过早 `available_at`、缺 impression/served 来源、未成熟均记 `coverage_unverified/source_unavailable/pending/excluded` 等状态。没有完整来源覆盖，成熟无正反馈不自动成为负例。

`fixtures.py` 在 ADS 自有目录从原 B/C 合成语义派生**新的连续来源批次**：原公共 fixture 的精确重投使唯一序号缺 2，不能据它声明来源前缀完整。ADS 的 `ads_v1/ads_v2` 固定文件把新合成事件序号重新排成 1–34/1–36，不修改原 B/C 文件；`freeze.py` 再逐分区核对 batch/hash、同位置不可冲突及从 1 开始的连续前缀，旧缺序文件有专门拒收测试。此修正只为合成验收建立可证分母，不推断真实 H09 完整性。

`freeze.py` 校验固定 source batch hash、recipe 与 `project_revision`、dbt invocation/所有模型和 SQL tests、行粒度/label-null/汇总对账后，以内容 hash 写 S3 `rows.jsonl`、`rollup.json` 和 `manifest.json`，回读相同字节；manifest 记录 generation、cohort、定义/标签修订、评测截止、**逐分区连续水位**、输入 hash、dbt manifest/run_results hash、各文件 hash/行数与不可评实验状态。独立 `schema.sql` 的本地 PG 收据只允许同 generation 同 manifest 重放，异 hash 冲突；不替代 DC 正式 execution fence 或 H12 EvalReport。

## 隔离验收

在已有锁定 macOS ARM64 runtime 上执行：

```bash
ads_run_parent="$(mktemp -d /tmp/sea-ws07e-ads-acceptance.XXXXXX)"
/tmp/sea-ws07c-ws08c-runtime-verified-20260914/.venv/bin/python warehouse/ads/acceptance.py \
  --runtime /tmp/sea-ws07c-ws08c-runtime-verified-20260914 \
  --output "$ads_run_parent/run"
PYTHONPATH=warehouse/ads python3 -m unittest discover -s warehouse/ads/tests -p 'test_*.py' -v
```

最终强验收退出 0，固定证据为 `/tmp/sea-ws07e-ads-acceptance.iIwfEt/run/report.json`、两代 `frozen_*/manifest.json`、S3 数据目录和 PG16 数据目录；任务创建的三个服务均已停止。v1 七条 visible/served 中 **2 正、2 负、2 pending、1 future_feature excluded**，成熟分母 4、连续来源水位 34；晚到的有效阅读与窗口推进后 v2 为 **3 正、3 负、1 excluded**，成熟分母 6、连续水位 36。v1 rows SHA-256 为 `b5fbf7d010519fe0cddd2193e80709246d096843967e0c1a750723388c9ac87a`；v2 为 `657d067ebba812373a5c3daf30c5543da5597dfa55528e39921cb09b24fc978e`。两个 S3 manifest SHA-256 分别为 `1d48abd8642f763f00910745459584ecc914aca811d99c9914a04ab7272a41af` 和 `703ebf23afc6e18a2ef234575c89df06edf9709e30739a247f8c2d7d92a692cd`，已与本地文件及 PG 实际两行收据核对。

补数后按旧 recipe 重建的 v1 明细与旧文件逐字节一致；PG 同 generation 异 manifest 拒收。额外构建验证未获批准的 observed 全部不可评、带覆盖收据的 observed **合同 fixture 仅 SQL 预览**可评，以及 impression 来源分区缺失时标签保持 `null`。四个纯 Python 合同反例与 dbt ADS 粒度/成熟来源/汇总测试通过。原 H10.b 数仓整链在同分支复跑 `warehouse/scripts/verify.py` 退出 0，v1/v2 固定 Parquet 仍为 4/6 行、冲突/重复事实粒度反例通过；证据为 `/tmp/sea-ws07e-warehouse-regression.RYOWsh/run/report.json`。原 `warehouse/tests` 9 项 Python 测试也通过。

正式 H09 的客户端可见性回执、逐来源覆盖与成熟证明、权威 assignment/variant、WS09-E 固定指标/分组不确定性、WS09-F 查询、真实流量及实验观察期尚未验收。现有 `internal/evaluation` 没有从 ADS 取得可追溯 score/已批准 release，不能用本切片计数冒充 R06 AUC、实验 lift 或正式 EvalReport。
