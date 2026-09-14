# 推荐训练 CPU 候选基线（WS09-C）

状态：`LOCAL_VERIFIED`。本目录只接收 `training/src/sea_training/dataset.py` 已验证的 H10.b v2 冻结 Parquet 快照；训练器不重算数仓标签、不更改数据集 schema，也不激活推荐策略。运行环境复用父级 `training/uv.lock` 的 Python 3.12/CPU 依赖。

```sh
cd training
uv sync --locked --python 3.12
uv run --locked --python 3.12 python -m pytest -q recommend/tests tests
uv run --locked --python 3.12 python -m recommend /fixed/manifest.json \
  --expected-sha256 <64位清单哈希> --checkpoint /candidate/checkpoint.json
# 同一命令添加 --resume 才继续已有断点；--max-updates N 可控制本次最多更新数。
```

## 数据资格与模型语义

入口先按清单验证全部分片 hash、类型、行数、SubjectRef、真实曝光键、请求时间切分、特征可用时点、内容修订、成熟观察窗与标签状态，然后才形成训练输入。`synthetic/synthetic` 只可用于数值验收；`observed/observed_behavior` 是待上游来源验收的真实行为类型。其他 `label.source`（尤其 `teacher`）拒收。正例是冻结清单中的成熟 `POSITIVE`，负例只用与 `impression_id` 对应的成熟 `OBSERVED_NEGATIVE`；未曝光候选、未成熟展示和教师低分都不是负行为。当前 22 列行契约只能给出结构与时点证据，不能单独证明客户端确实显示了条目；真实验收仍需 H09/H10 的来源回执。

首版要求训练、验证、测试均有数据；训练分片同时含正/负例；所有行 `sampling_probability=1.0`。抽样、困难负例、倾向校正不在本 recipe 中，任何小于 1 的入样率都会拒收。按 request_time 固定 train→validation→test 时间窗；只用 train 更新参数，验证/测试只计算固定切分的经验目标，不把后续行为或特征回填。

双塔是两个**分开训练的二维仿射编码器**：用户塔读取 `user_interest`，item 塔读取 `item_quality`，二者点积产生候选相似度。当前形状仅证明配对梯度、训练和恢复，不代表已经学到可用的大规模用户/item 空间。LR 精排在双塔固定后读取 `[1, user_interest, item_quality, 交叉项, 双塔点积]`，用同一成熟目标的二元交叉熵 SGD+momentum 拟合。对外仅称原始 `uncalibrated_logit`；报告中的 `empirical_logloss` 是在当前抽样前提下对样本算出的训练目标，不是已校准概率或线上效果。缺少可信教师和校准所需的样本机制，因此本目录没有蒸馏、多头或校准工件。

## H11.a 候选与 H12 证据交接

每次更新后原子覆盖 checkpoint，内容包括 recipe、冻结 manifest SHA-256、训练行顺序 SHA-256、固定超参数、双塔/LR 参数、各自 momentum 状态、`random.Random` 状态、当前阶段/epoch/洗牌顺序/reader 游标、更新数及封套 digest。恢复时重新运行 H10.b 全量校验并核对输入、超参数、数值形状和封套完整性；初次执行若已存在 checkpoint 必须显式使用 `--resume`。同 seed 全跑与中途恢复所得 checkpoint 字节一致。

| 交接 | 本次可交内容 | 接收方验收前仍需 |
| --- | --- | --- |
| H11.a → WS09-D | `status=candidate` 的 checkpoint、固定清单/recipe/特征契约引用、双塔与 LR 参数、`score_semantics=uncalibrated_logit` | 将恢复态与 serving 工件分开；验证 user/item 成对导出、I/O/数值一致性及 DC 装载探测；不能自动成为 H11.b release |
| H11.a → WS08-F | 候选 user/item 空间和 LR 原始分数的实验输入 | 独立评测通过、同一 pair 的 item 索引与 user bundle 预构建、策略接纳与激活；旧空间回退须可查 |
| H12 → WS09-E | 固定 train/validation/test 行数、正负计数、经验 logloss、冻结 hash、更新数与恢复对照 | 成熟真实行为来源、分请求/冷启动等切片、召回/排序/资源对照、预设门槛和独立评测报告 |

局部复验：`uv run --locked --python 3.12 python -m pytest -q recommend/tests tests`，53 项通过（推荐测试 14 项、reader 既有测试 39 项）。覆盖梯度有限差分、同 seed 重跑、双塔中途及阶段切换后恢复、train-only 拟合、错来源/入样率/时间切分/未来特征/空 split、checkpoint 损坏与错形拒收。实际 CH/S3 成熟数据、DC serving、候选索引和生产灰度均未验收。
