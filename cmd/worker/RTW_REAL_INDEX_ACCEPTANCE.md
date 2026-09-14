# H06 真实 RTW 接纳消费者联验合同

状态：**待跨仓执行 / H06 整体 PARTIAL**。`TestRTWRealProviderIndexDispatch` 仅在 `SEA_RTW_REAL_INDEX_FIXTURE` 指定了 RTW 测试进程写出的 JSON 时运行。普通 BTW 测试不连接任何 RTW 服务，也不把 HTTP 替身当作真实接纳。

RTW 父测试在隔离 go-zero HTTP/PG16 中另建一个**未 claim、未接纳、未发布**的 build，并与 BTW 共享 `objects_dir`。Release 必须固定一条非空 source，source+wiki 经 `chunk_size=64,chunk_overlap=0` 分片后最多两个 chunk；`chunk_profile` 为 `index-paragraph-v1`。三路 `retrieval_profiles` 的完整合同与 `fixedIndexSettings()` 相同：dense `fixture_model/tokens_v1/dense_space/2`、sparse `fixture_model/tokens_v1/sparse_space/100`、multivector `fixture_model/tokens_v1/multi_space/2,mask=valid,aggregation=sum_maxsim`。测试会从真实 RTW `GetRelease/GetRevision/GetBuild` 和共享内容寻址目录重新读取这些冻结输入，不接受一个预制 READY/index 结果。

Fixture JSON 字段：

```json
{
  "base_url": "http://127.0.0.1:18080",
  "worker_token": "synthetic-test-worker-token",
  "objects_dir": "/tmp/isolated-rtw/objects",
  "build_id": "build-from-rtw",
  "release_id": "release-from-rtw",
  "module_id": "module-from-rtw",
  "source_revision_ids": ["source-revision-from-rtw"],
  "wiki_revision_ids": [],
  "chunk_profile": "index-paragraph-v1",
  "chunk_size": 64,
  "chunk_overlap": 0,
  "result_path": "/tmp/isolated-rtw/btw-real-index-result.json"
}
```

从 BTW 根目录运行下面的现有脚本。脚本自行启动、销毁独立 PostgreSQL 16 的 content/session 数据库；RTW 父测试应等 BTW 子进程返回后再读取结果。`worker_token` 只在临时0600 fixture 中使用，不写入 BTW 账本或结果文件。

```bash
SEA_RTW_REAL_INDEX_FIXTURE=/tmp/isolated-rtw/btw-real-index-fixture.json \
GOFLAGS='-run=^TestRTWRealProviderIndexDispatch$' \
bash cmd/worker/acceptance.sh
```

消费者先向真实 RTW 提交过期 `ClaimBuild`，验证拒绝且 build 仍未 claim；再领取当前 fence，提交旧 fence 的 `FAILED` 结果并验证拒绝。它调用真正的 `Preparer` 从 RTW 固定修订生成本地 chunk，运行 tRPC-Agent-Go Graph/Runner 与三路 BTW local-exact 数值索引。DC 仅使用 typed Represent/技术 Job HTTP 替身，其 Complete 回调必须从真实 RTW 读到同一 IndexManifest Ref/hash 的 `READY`；随后要求 DC ACK 与 PG outbox delivered，并再次用真实 RTW `AcceptBuild` 验证同 fence/同 Ref 幂等。

成功后才写 `result_path`（0600）：

```json
{
  "build_id": "build-from-rtw",
  "index_manifest_ref": "sha256/<hash>",
  "index_manifest_hash": "<hash>",
  "dc_ack_ref": "sha256:<hash>",
  "dc_ack_hash": "<hash>",
  "rtw_state": "READY",
  "rtw_generation": 1
}
```

RTW 父测试仍应凭自己的真实 PG/API 状态核验这些字段、RTW outbox 事件、固定 Release 与未发布指针，而非信任 BTW 结果文件自证。即使此测试通过，正式 DC BGE-M3、Milvus 三路索引、人工发布、真实 Collector/Tempo/Loki 和容量验收仍是 H06 后续门禁。
