# H10 BuildCovered v2 候选阶段验收

日期：2026-09-15。状态：**固定已验覆盖引用/内存状态与本地 HTTP 对象适配的合同测试 LOCAL_VERIFIED；H10 总体 PARTIAL**。这是 `covered_*` 独立切片，不改 v1 `Runner.Build`、FavoriteCandidate、`usermodel.FeatureBaseline`/Watermark 或既有 ID。

`CoveredBuilder.BuildCovered(subject,spec,generation,revision,GlobalPrefixRef)` 只调用一次 `CoveredStateReader.CoveredStateAt(ctx,subject,prefix,cutoff)`。该小接缝要求 WS08 在同一 PG repeatable-read 快照内先独立验完整全局 `[1,W]`，再按 W 重放该主体全部历史，返回已验 `SubjectCoverageRef`、W 时活跃贡献、W 后事件键、状态版本与当前完整性。H10 不调用 `Current.Active`，也不能把 `SubjectReceiptHash` 等格式/哈希检查当作自身重验了来源完整性。`UsermodelCoveredReader` 已在WS08锁定的 `CoveredStateAt` 类型上做字段映射，不复制其历史折叠 SQL。

本切片核 `GlobalManifestHash` 与已验主体 receipt、精确 SubjectRef/前缀、稀疏索引对象的实际 SHA-256、JCS 逐行内容、offset 顺序/主体/行数及 W 时贡献的 EventID/来源哈希。W 后尾部仅允许大于 W 的已接纳或 pending 键；未知动作/状态、错误哈希和错主体均拒绝。值计算目前只支持已规范化 `fact/count` FeatureSpec，其他模式明确拒绝。输出 `sea.user-feature-baseline.covered.v2` 内容寻址候选与不可变读回，状态始终 `candidate_default_off`；不产生已接纳 FeatureBaseline、FeatureSnapshot 或 ServingBundle。

`GOFLAGS=-p=2 GOMAXPROCS=2 go test -mod=readonly -race -count=1 ./internal/warehouse/featurebaseline ./internal/sourcecoverage`、`go vet` 与 `go mod verify` 通过。固定反例为 offset1 assert、offset2 pending、offset3 已接纳 retract，覆盖 W1 时候选值仍为 1，尾部保留 2/3，`CurrentComplete=false`；可证空主体切片为 0/missing。伪全局根、未验 reader、篡改稀疏对象、来源哈希不一致、错尾部和未实现 spec 都不写候选。`RunnerCoveredObjects` 复用现有 S3 HTTP `getFixed/putFixed`，以本地 HTTP 对象替身验证读回和篡改拒收。

第二阶段 `bash internal/warehouse/featurebaseline/covered_test_postgres.sh` 在WS08已推 `9741363` 上自启停隔离PG16，race测试退出0，证据 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T//sea-h10-covered.ufydLV/`。实际 PG 先 Append offset1 assert、offset2 pending、offset3 已接纳 retract，未验证全局前缀时H10拒绝；固定来源 Proof经WS08 `VerifyPrefix/VerifySubject` 写入PG后，`UsermodelCoveredReader` 读取W1历史仍返回assert1与尾部2/3，H10冻结值1且仍default-off；改写对象字节后再次Build拒绝。该测试连接的是**固定来源 Proof→真实WS08 PG历史读→H10→本地HTTP对象适配**，不冒充真RTW/DC权威网络或SeaweedFS。WS08 的 `CurrentComplete` 仅说明PG已知尾部，DC鲜度仍是Serving独立门禁。真实RTW/DC Proof、正式SeaweedFS、Accept v2、FeatureSnapshot与Serving尚未同次联验；签名与本地PG接纳不能自动证明线上来源完整。
