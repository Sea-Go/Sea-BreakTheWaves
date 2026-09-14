# WS07 全局来源覆盖发布

`CoveragePublisher` 只发布仓库已接纳的 `rtw.community.favorite` 来源前缀，不修改 RTW 业务主体、BTW usermodel 事实、H10 FeatureSpec/基线或推荐曝光与标签。独立 DC consumer 仍为 `btw-warehouse-favorite`；Graph 事实 worker 的 consumer 和 ACK 独立。

## 冻结输入与工件

`CoverageConsumer` 复用现有数仓 ODS 事务。它在 ODS 提交后、DC ACK 前持久记录原始读取窗 `(from_offset,to_offset,batch_hash)`；记录失败不 ACK，丢 ACK 可按原 EventSpec/hash 重投。`PublishPrefix(W,generation)` 只接已提交且有完整 batch 窗的 `[1,W]`，在同一 PG repeatable-read 快照导出 ODS 与 batch 记录；逐条回查 DC 不可变 receipt、调用 RTW 冻结来源校验器核 EventSpec、完整 SubjectRef、来源 hash。数仓没有权力重新分配主体。发布前使用共享 `sourcecoverage.VerifyBatchEvidence` 对每窗真实 EventSpec/offset/input_hash 重算 DC batch hash；不同 consumer 的窗可不同，比较根时只比较完整 EventIndex。

工件格式与 `internal/sourcecoverage` 单一合同一致：完整 EventIndex、BatchEvidence、主体 SparseIndex 均按全局 offset 升序逐行 JCS 编码并加 LF。全局 manifest 是 `GlobalPrefixRef.ManifestSHA256` 暂置空后的 JCS 对象；主体 receipt 是 `SubjectCoverageRef.ReceiptSHA256` 暂置空后的 JCS 对象，均无尾部 LF。相应字段填入对象自身的内容 SHA-256，并用 hash 文件名归档到隔离 S3。主体稀疏索引**从同一完整 EventIndex**派生；未出现的主体保存 0 字节对象，hash 为 SHA-256(empty)。只有绑定了已验全局根，该空切片才可代表可证零事件。

`GlobalPublication{Ref,ManifestURL}`、`SubjectPublication{Ref,ReceiptURL}` 是 Publisher 的输出。`BindingPolicyID` 固定为 `rtw.favorite.authority.v1`；`Origin="1"`、`ThroughOffset` 和所有索引 offset 是规范十进制字符串。对象地址形式：

```text
<S3Prefix>/warehouse-coverage/<generation>/event-index/<sha>.jsonl
<S3Prefix>/warehouse-coverage/<generation>/batch-evidence/<sha>.jsonl
<S3Prefix>/warehouse-coverage/<generation>/manifest/<sha>.json
<S3Prefix>/warehouse-coverage/<generation>/subject-index/<subject-jcs-sha>/<sha>.jsonl
<S3Prefix>/warehouse-coverage/<generation>/subject-receipt/<sha>.json
```

发布 receipt 在 `warehouse_favorite.coverage_publication` 与 `coverage_subject_receipt` 只增持久保存，同 generation 异根冲突。`PublishSubject` 从已经发布的完整索引再计算并核对 hash，不接受外部单独给的稀疏列表。此处对象 PUT/读回是隔离 S3 验收；正式并发 CAS、跨主机归档和大 W 分段增量发布尚待独立实现。本首片 `PublishPrefix` 最多 10000 行，且 W 必须恰好落在一个已记录 DC batch 窗末尾。

## 隔离验收

```sh
SEA_DC_PLATFORM_ROOT=/path/to/isolated/datacenter \
SEA_RTW_FAVORITE_ROOT=/path/to/isolated/rtw \
WAREHOUSE_FAVORITE_RUNTIME=/path/to/locked/warehouse-runtime \
warehouse/coverage/acceptance.sh
```

脚本建立隔离 PG16、真实 DC platform、真实 RTW Favorite 单主体 assert/retract、ClickHouse/dbt、SeaweedFS。真实来源链验证 W1/W2、ACK 丢失重投、独立 Graph consumer、空主体切片和缺 ODS offset。双主体 `[u1:1,u2:2,u1:3]` 使用同样真实 PG/CH/S3，但 RTW/DC 来源由隔离权威 HTTP 夹具给出，故它的“多主体业务来源”不称真实 RTW 生产行为；验证稀疏 `[1,3]`、`[2]`、空集、另一 consumer 不同 batch 分段、错误 RTW 主体、错 DC receipt、缺 offset、四类 S3 工件篡改、重放及 DWD 归属。`cross-domain/artifacts/` 留存关闭本地 S3 后可读的精确对象字节和 ref JSON；对象 URL 是本次隔离端口，不是持久线上地址。最终报告分别标注证据等级；仓库来源发布不等于 WS08 覆盖接纳、H10 特征、Serving 或线上质量已激活。
