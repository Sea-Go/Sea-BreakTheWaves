# 真实 BGE-M3 三头冻结值与 Go exact 内核对照

日期：2026-09-15。隔离 BTW 分支 `feat/three-lane-bge-parity-20260915` 基于 `e28ce58`；此切片只增加 `internal/retrieval/three_lane_parity/`，不改 Dense、Sparse、Multi-vector 现有实现、训练器、数仓或 Docs。

验收入口（报告路径须尚不存在；四个 `SEA_BGE_*` 输入缺一则真工件测试会 Skip）：

```bash
SEA_BGE_THREE_LANE_MANIFEST=/private/tmp/sea-bge-qrel-three-lane.ZMGToD/run/manifest/719b4994b37133a8c6a82e60b333f3d015c4a8d1d9f965f7a2cb7c0a226a68d9.json \
SEA_BGE_THREE_LANE_SCORES=/private/tmp/sea-bge-qrel-three-lane.ZMGToD/score-test.json \
SEA_BGE_THREE_LANE_SCORES_SHA256=200d3e2c4f9229dc85efa33e4ebbcc2b0379975f99b8e15482ef8888c3703355 \
SEA_BGE_THREE_LANE_REPORT=/tmp/new-go-parity-report.json \
GOFLAGS=-p=2 GOMAXPROCS=2 go test -mod=readonly -race -count=1 -v ./internal/retrieval/three_lane_parity
```

Loader 按**原始字节** SHA-256 校验内容寻址 manifest、query/chunk JSONL 分片、仓内 `model.lock.json` 和 `profiles.json`，并核模型完整 revision、三路空间/合同、1024 维 Dense 与 ColBERT、稀疏有序正权重、全有效 token mask、有限数值和 qrel identity。Python 输出使用 sort-key 紧凑 JSON 的浮点写法；Go 不把它误称 JCS 或重编码浮点后比较来源哈希。缺分片、错空间、错维度/文本 hash、缺头、错 mask、非有限数值全部拒绝。

验收 Adapter 把冻结模型响应按输入 ID 注入现有三路 `Encoder.Represent`，实际走各自 `Build→Search` 的本地 exact 工件和内核，Go 不再调用模型，也不重写 Dense 点积、Sparse 倒排或 Multi-vector MaxSim。每个 `(document_id,document_revision,chunk_id)` 用逐段 UTF-8 hex 加 `!` 编成唯一 `corpus.Chunk.ID/RevisionID`：与 Python 的三元组字典序一致，且按 Python scorer 的每 query `candidate_set` 精确装载时间可用修订；这只是验收装配，不证明线上服务已实现 `content_available_at` 过滤。Multi 的 `TokenTopK` 设为全部 103 条 document 有效 token 行，使候选覆盖全部文档并复用完整 `mean_maxsim`；不是生产小预算或 ANN 测试。

结果：test split 2 个 query × 6 个可用 chunk，Dense 的 12 个原分数最大绝对差 `9.992007221626409e-16`、完整 TopK 一致；Multi-vector 的 12 个原分数最大差 `4.440892098500626e-16`、完整 TopK 一致；Sparse 的 12 个原分数差为 0。BTW 倒排按正式实现只返回 `score>0`，两 query 合计排除 10 个 Python 全候选中的零分项；**正分候选顺序/TopK 一致**，Python 含零分全候选 TopK 与 BTW 原样不等。报告明确 `sparse.topk_equal=true`（正分语义）、`sparse.python_full_topk_equal=false`、`zero_score_excluded=10`，不能把后者遮盖为全 TopK 一致。机器报告 `/tmp/sea-bge-go-parity.T680uQ/report.json` 与完整 race 日志 `/tmp/sea-bge-go-parity.T680uQ/go-test.log`。表示 manifest SHA 为 `719b4994b37133a8c6a82e60b333f3d015c4a8d1d9f965f7a2cb7c0a226a68d9`，独立 Python scorer SHA 为 `200d3e2c4f9229dc85efa33e4ebbcc2b0379975f99b8e15482ef8888c3703355`，H10.b qrel manifest SHA 为 `10ae33b8dd5b5fc634493a5eddfdbf47105c16c7c9ced57279eee96c13b1bdb8`。

Encoder 代码独立 HEAD `357aaeb`，Python scorer 独立 HEAD `0e1ffb9`；表示工件与 scorer SHA 在上述验收中固定。Go 定向真实工件与错误注入用例以 `-race -count=1` 退出 0；另运行 `go test -mod=readonly -count=1 ./...`、`go vet ./...`、`go mod verify`、`git diff --check` 均通过。

此对照只证明锁定 BGE-M3 **已有**三头值在小合成语料上经 Go 内核计算的数值/排序与独立 Python scorer 相符。H10.b 缺完整候选人工判断范围，Recall/MRR/nDCG 仍 `not_evaluable`；没有新训练权重、业务提升、ANN 规模、线上时间过滤或生产模型调用验收。状态 `LOCAL_VERIFIED / overall PARTIAL`。
