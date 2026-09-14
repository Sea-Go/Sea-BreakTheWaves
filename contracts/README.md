# Sea 算法数据契约

`jsonschema/training-dataset-manifest.v2.schema.json` 是当前 H10.b manifest schema；v1保留为历史归档，当前消费者明确拒绝v1（缺少可验证DIM覆盖语义）。
`recommend-engagement.columns.v1.json` 固定首个推荐互动样本的22列、顺序、Arrow类型与缺失规则。
消费者实现位于 `training/`，warehouse 只生成实例，不维护第二份 schema。

首个行契约 `sea.recommend-engagement.v1` 验证合成或观察数据中的 `effective_read` 二分类样本。
这不代表其他搜索/推荐目标已支持；未知行契约必须明确拒绝。

时间含义：

- `request_time`：请求开始，也是 split 半开窗口 `[start,end)` 的分组基准。
- `feature_cutoff`：该请求实际固定特征的读取截止点，满足 request_time ≤ cutoff ≤ impression_time。
- `feature_available_at`：本次引用特征当时可用时间，不晚于 cutoff。
- `impression_time`：符合该产品展示规则的真实曝光时间，不能用下发时间补造。
- `label_observation_end`：impression_time 加目标窗口，不能超过成熟水位或 split 上界。
- source `ingest_cutoff` 与各分区 `event_time` 水位分别保存；前者不代替事件覆盖。

完整 SubjectRef 以 authority_id/tenant_id/subject_id 三列传递，关联使用完整三元组。
同一目标对一个曝光只产生一个合格样本；标签修正与补数产生新的数据集revision。
此版本包含原始批次、SQL运行/版本、DIM修订、generation、列、切分、工件hash和训练拟合引用。
记录不含真实供应商凭据，S3连接由调用方注入文件系统。

数据集通过验证只证明本契约和输入文件满足断言，不证明用户效果、全部源覆盖或模型质量。

manifest v2 将 source.dim_revisions 固定为 item_id/content_revision 结构化二元组，必须覆盖每条样本，禁止冒号拼接与无关引用。行契约及22列仍为v1。升级要重新导出新清单，旧清单/文件不可原地覆盖。
