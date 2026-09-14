# WS09-D：推荐候选 serving 导出

本目录是 H11.a 的第一个 `LOCAL_VERIFIED` 切片。输入是 WS09-C 已完成的 `sea.recommend.cpu-towers-lr.v1` 恢复态 checkpoint 和同一份 H10.b 冻结数据清单；输出是**候选** serving 包。它不注册 DataCenter 模型、不触发业务指针或 H11.b release。搜索实验词法排序器由 `assess_search_candidate` 返回 `NOT_SUPPORTED`，不能充当 Dense、学习型 Sparse 或 Multi-vector 编码器。

在 `training/` 目录使用锁定的 Python 3.12 环境：

```sh
uv run --locked --python 3.12 python -m export /frozen/manifest.json \
  --expected-manifest-sha256 '<64位输入清单sha256>' \
  --checkpoint /candidate/checkpoint.json \
  --expected-config export/training-config.example.json \
  --preprocessing export/preprocessing.identity.v1.json \
  --output-dir /candidate/serving-<唯一ID>
```

`--expected-config` 必须是**该次训练实际使用**的选项，示例文件仅适用于默认 `seed=7, epochs=3, learning_rate=0.05, momentum=0.8`。命令先通过 H10.b reader 核对清单和全部 Parquet 分片，再重算训练行顺序 hash，核对 checkpoint 封套、数据、recipe、配置、形状、进度和 `candidate/done` 状态。训练器当前直接读取 `user_interest` 与 `item_quality` 原始浮点值，并不消费 `transform_refs`；因此这里要求显式的 `identity-f64` 声明，并拒绝清单中有变换引用的候选，以免导出一个训练时未使用的预处理。缺声明、变换错误、错形、非有限参数、输入 hash/配置失配都失败且不写正式候选目录。

输出目录首次创建后不得覆盖；四个小文件按内容哈希和大小固定，最后才放 `manifest.json`：

| 文件 | 用途 |
| --- | --- |
| `weights.json` | 8 个双塔参数、5 个 LR 参数；无 optimizer、RNG 或 reader 游标 |
| `preprocessing.json` | 输入顺序、恒等变换、缺失/非有限拒绝策略 |
| `model_config.json` | `feature_contract_id`、输入与精排特征顺序、双塔维度 2、dot 相似度、pair/space ID、`uncalibrated_logit`、`calibration=none` |
| `probes.json` | 三组固定数值输入和训练/独立 serving 输出，对照绝对误差不超过 `1e-12` |
| `manifest.json` | `candidate`、`activation=none`、`registration=none`、清单/训练行/checkpoint hash、训练配置、Python 3.12/锁文件 hash、pair/space、所有文件 hash 和大小 |

`pair_id` 和 `space_id` 由配对双塔权重、预处理、空间维度与度量确定，不能由调用方随意填写。LR 输出是未经校准的原始 logit，不作为概率使用。`load_candidate()` 在读取时重新核对文件 hash、预处理、配置、pair/space 与参数形状；`score()` 是独立推理实现，导出阶段同训练器的 `tower_score`、`ranker_logit` 比较。对照只证明导出计算相同，不证明推荐收益、成熟样本、预构建或实际 DC 装载。

交接给 H05/DC 的是候选包路径或对象存储内容引用、`manifest.json` 本身的 SHA-256、四个文件 hash、`model_config.json`、`probes.json` 和 `score_semantics`。当前尚没有已验证的 DC 推荐双塔/LR 自定义权重装载与探测适配，不能把 `probes.json` 当作实际 DC 探针响应。交接给 WS08-D/F 的是候选 `pair_id/space_id`、维度/度量/特征顺序和未经校准分数；WS08 需另行完成用户/item 预构建、兼容与业务接纳，只有其 owner 可批准 H11.b 生效。H12/WS09-E 只能将本地数值对照登记为导出一致性证据，正式评价需要冻结成熟数据、业务分组和实际服务结果。

CLI 的 stdout 是机器可读的结果清单；阶段事件以 Python `logging` 统一 JSON 格式写 stderr，包含 `service/component/source/event/status/dataset_manifest_sha256`。这里没有实际 Job/Trace Provider、指标或 Collector 回读，故 OBS-03/04/07 不得标已接纳。局部验证命令和边界见 [`acceptance/2026-09-14-local.md`](acceptance/2026-09-14-local.md)。
