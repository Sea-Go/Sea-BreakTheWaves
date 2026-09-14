# Sea 数仓：推荐互动数据闭环

此实现对应 BG-2026-09-13-r2、WS07-A/B/C/D 的首个推荐互动切片。批量业务处理由真实 ClickHouse SQL 执行，dbt 管理依赖与测试。原始批次先归档到任务独占 SeaweedFS S3，再由 ClickHouse 读取；样本由 ClickHouse `s3()` 直接写出 Parquet。Python 只负责 IO、进程、运行 fence 与工件检查。

当前是**隔离环境、合成数据的 L2 实现验证，以及 H10.b 的真实存储生产者/消费者联验基础**。没有接 DC 正式任务 lease/Outbox、真实网页/桌宠来源或生产环境，不代表 WS07 整组或全部 H10 的 L3 已完成。

## 可运行命令

macOS ARM64 已验证 ClickHouse 25.8.28.1、SeaweedFS 3.97、CPython 3.12.13、dbt-core 1.10.11、dbt-clickhouse 1.9.7、PyArrow 21.0.0。所有 Python 依赖在 `environment/requirements.lock` 固定；二进制官方地址和 SHA-256 由 `scripts/bootstrap.py` 固定，不执行远程安装脚本。

从仓库根执行，`CONTRACTS` 指向同一集成仓的 `contracts/jsonschema`。WS09 在独立分支开发时可指向其只读契约目录；本目录不复制权威 schema。

```sh
TASK_RUNTIME=$(mktemp -d -t sea-warehouse)
python3 warehouse/scripts/bootstrap.py --runtime "$TASK_RUNTIME"
CONTRACTS="$PWD/contracts/jsonschema"
"$TASK_RUNTIME/.venv/bin/python" warehouse/scripts/verify.py \
  --clickhouse "$TASK_RUNTIME/clickhouse" \
  --seaweed "$TASK_RUNTIME/weed" \
  --dbt "$TASK_RUNTIME/.venv/bin/dbt" \
  --contracts "$CONTRACTS" \
  --output "$TASK_RUNTIME/acceptance"
WAREHOUSE_CONTRACTS="$CONTRACTS" "$TASK_RUNTIME/.venv/bin/python" -m unittest discover -s warehouse/tests -p 'test_*.py' -v
```

可选 `--keep-services` 保留当前进程及隔离服务，输出实际 S3 endpoint 给消费者独立读取；用 Ctrl-C 结束该进程会关闭它创建的服务。不使用已有 ClickHouse、默认端口或远端凭据。Linux 服务接入、共享部署与规模参数需要单独验证。

## 实际目录与数据流

| 目录 | 唯一职责 |
| --- | --- |
| `environment/` | 锁定依赖、隔离 dbt profile、追加原始表 DDL |
| `models/ods/` | 固定批次 + 接入截止点，读取追加历史 |
| `models/dwd/` | 事件去重、请求、候选阶段、实际曝光、行为、实际服务特征 |
| `models/dim/` | 内容不可变修订，含有效时间/可用时间 |
| `models/dws/` | 完整身份与版本关联、资格、窗口成熟及标签修订 |
| `models/marts/datasets/` | 固定 split 的推荐互动样本 |
| `tests/` | SQL 质量与本机执行权测试 |
| `scripts/` | 官方环境引导、任务专用服务、dbt 执行、S3 归档/导出和验收 |
| `fixtures/` | 可重放的两批合成来源；`fixtures.py` 是生成器；新主体回归用 `PYTHONPATH=warehouse/scripts python3 -c 'from fixtures import write_scope_cases; write_scope_cases("warehouse/fixtures")'` 单独重建，原v1/v2批次保持原文 |

历史 `event_history` 使用追加 MergeTree；current 替换投影不是样本输入。每次 attempt 使用独立 schema，dbt 构建同一 target 受到单机文件锁保护。采样不平衡尚未引入，`sampling_probability=1.0`；没有把未展示候选或孤儿反馈补成负例。

## H09 输入与 H10.b 交接

输入 JSONEachRow 的粒度、字段在 `environment/landing.sql`。`domain + authority_id + tenant_id + subject_id + event_id` 标识不可变逻辑事件，`payload_hash` 是除 `batch_id/payload_hash` 外字段按排序 JSON 编码的 SHA-256；来源文件名与唯一 `batch_id` 一致。重复原文可重投；同事件异值或同逻辑曝光多个事件被质量检查拒绝。候选主键保留 stage、stage_invocation_id、attempt。

主体使用 `authority_id/tenant_id/subject_id`；事件去重及请求、候选、曝光、特征、行为的关联/唯一键均保留完整主体。不同主体可复用本地 request/impression/event id。sample_id 使用完整主体、request、impression和目标的JSON tuple做SHA-256，不能用冒号拼接。请求明确记录 feature_cutoff；`request_time <= feature_cutoff <= impression_time`，实际特征的 available_at 与 effective_at 均不晚于 cutoff。窗口从真实曝光开始，不能把 request、实际使用特征、曝光和阅读时间合成一个字段。

H10.b 使用 `sea.training-dataset.v2` 清单：DIM引用为结构化 `{item_id, content_revision}` 数组，source包含实际 `recipe_sha256`；22列行契约仍为v1。既有v1清单、源批次和历史结果保留。列、清单 schema 由 WS09 `contracts/jsonschema/` 唯一维护。生产者按列顺序 SELECT，验证物理 Arrow 类型/nullable/row count/hash，并交付每个 split 的固定文件、输入批次、DIM 修订、事件水位、接入 cutoff、dbt manifest/run_results 摘要。事件水位来自合成 fixture 明确设置，不能从“最大已到事件时间”推断真实来源完整性。

22 列互动契约明确 request_time、impression_time、feature_cutoff、feature_available_at、label_observation_end。样本按 request_time 切分；标签窗口锚点为 impression_time。当前 synthetic 的30分钟窗仅用于逻辑验收，不是产品阅读阈值。无数据变换拟合，`transform_refs=[]`，避免伪造训练 fit 工件。

S3 清单在所有分片经过物理校验后写出，**对象存在不等于领域接纳**。本机 LocalPublication 的claim绑定实际代码hash、固定批次hash、完整dbt recipe、schema/列契约hash及独立输出namespace。build收据核对实际完整DAG、SQL原文、vars、invocation和结果hash；publish重新核对当前attempt、输入/代码/recipe、v2清单、每片物理schema/rows/hash与完整split后CAS。空清单、错代、新执行权配旧结果、缺片及构建后代码变更都拒绝。该本机实现仍不是DC分布式执行权适配。训练 reader 独立验证通过后才可登记自己的接纳证据。

## 验收覆盖与剩余工作

`verify.py` 建四个完整 dbt generation（v1、v1历史重建、v2和主体冲突案例），并注入冲突与重复粒度两组必须失败的构建。结果在输出目录 `report.json`、各构建 `dbt.log`、`target/{manifest,run_results}.json`、S3 原始批次和 Parquet/manifest。

验证包含：重投不翻倍、孤儿保留且不参与训练、未曝光不当负例、未来特征排除、请求执行期间可用特征保留、PENDING 不当负例、迟到标签新代、current v2 已 FINAL 后 v1仍能完整重建且 Parquet hash相等、旧清单文件不变、取消后不能发布、同事件异值与 Join 倍增被拒绝。另验证错误主体候选被排除、跨tenant相同ID并存且行为不串联、冒号组合不会碰撞sample_id。本机多进程claim、发布绑定和工件损坏测试单独运行。

后续按任务文档接入：H09 真实生产者及批次覆盖收据；H04 分布式 lease/取消/Outbox；多来源分区水位；正式 FeatureSpec/标签 owner审阅；有界源表扫描和对象流式大分片；搜索 qrels/三路索引、共现/候选/用户特征和 ADS；真实训练与实验收益。当前 IO 客户端把微型夹具/导出读入内存，不能直接宣称具备大数据吞吐或线上质量收益。

官方依据：[dbt集成](https://clickhouse.com/docs/integrations/dbt)、[adapter 1.9.7发布](https://github.com/ClickHouse/dbt-clickhouse/releases/tag/v1.9.7)、[ClickHouse固定发布](https://github.com/ClickHouse/ClickHouse/releases/tag/v25.8.28.1-lts)、[S3表函数](https://clickhouse.com/docs/sql-reference/table-functions/s3)、[SeaweedFS固定发布](https://github.com/seaweedfs/seaweedfs/releases/tag/3.97)。
