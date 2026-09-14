# 冻结数仓数据的训练侧消费

此目录是 WS09-A / H10.b 的首个实际消费者。使用 Python 3.12，依赖由 `uv.lock` 固定。
SQL清洗、事件Join和成熟标签计算由 `warehouse/` 提供，本程序不重新实现上游SQL。

```sh
uv sync --locked --python 3.12
uv run --locked pytest -q
uv run --locked sea-dataset /path/to/manifest.json --expected-sha256 <manifest-sha256>
```

默认 schema 读取提供者 `contracts/jsonschema`，wheel自动包含同一源的生成副本。
`--schema-dir` 可选择明确版本的契约；生产训练应传入登记清单的期望hash，不能按目录发现latest输入。
CLI仅验证数据，输出结构化报告；它不会启动训练、修改模型、发布策略或接纳仓库业务状态。

`open_dataset` 支持本地/Arrow文件系统URI及调用方注入的S3兼容 `S3FileSystem`。
接收顺序为 manifest/schema → 全部声明文件下载与hash/size校验 → Parquet类型/行数 → 行资格 → 返回可消费快照。
所有文件先流式复制到任务私有临时目录，训练只读这些已验证字节；源文件在验证后改变不会影响该快照。
退出上下文自动回收快照，调用方须在上下文内消费；临时磁盘容量需按完整数据集和键索引预算。

```python
from sea_training.dataset import open_dataset

with open_dataset("manifest.json", expected_manifest_sha256=expected_hash) as dataset:
    for batch in dataset.iter_batches("train", batch_size=8192):
        consume_fixed_batch(batch)
```

检查包括：声明/文件/schema/hash一致、完整主体、真实曝光键唯一、请求分组不跨split、特征读取时点、
按实际曝光起算的完整观察窗、标签状态、有限数值/采样概率和仅train拟合声明。
跨分片键检查使用本机临时SQLite索引，避免把全部ID放进Python内存。
首个行契约目前只支持推荐互动22列；搜索、序列/稀疏特征和数值训练器仍需对应任务实现。

验证等级：单元测试使用明确合成Parquet；真实CH/dbt/S3交接由两端独立联验另记，
它也不能替代真实客户端采集、DC调度、成熟业务数据或模型效果验收。
