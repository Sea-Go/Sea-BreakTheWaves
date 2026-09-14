# 三路原值评分与 H10.b 判断覆盖

`three_lane_scoring.py`只读调用方固定 SHA-256 的 BGE-M3 表示 manifest、原字节 JSONL 分片和独立 `open_search_dataset` 已接纳的 H10.b qrel 快照。manifest 的模型锁、三路 profile、tokenizer、dataset ID/修订/来源行数/仓库生成号与 qrel 必须逐项一致；query 与 chunk 的身份、文本 SHA-256、内容发布时间必须与 qrel 行吻合。缺维、非有限数值、空间漂移、无有效 token、重复 query、同一逻辑 chunk 的多修订与任何 hash 错误直接拒收。首版只读本地内容寻址表示目录；qrel 可由独立 reader 从本地或隔离 S3 读取。

三路对每个 query 的**同一批**冻结 chunk 评分，仅排除其 `content_available_at` 晚于 `query_time` 的内容：Dense 为1024维原值点积；Sparse 为升序token ID的学习权重交集内积，零分候选仍在全集；Multi-vector 对每个有效 query token 取全部有效 chunk token 点积最大值，再对有效 query token 数取平均。评分器不再归一化，输入需满足已锁 profile 的 L2/none 合同。排序固定为分数降序，再按`document_id/document_revision/chunk_id`升序。`raw_scores`列全体时间可用候选，`top_k`是其中前K项，包含原值、判断掩码与未判断时的null等级。

调用：

```bash
PYTHONPATH=training/search:training/src uv run --project training --locked --python 3.12 \
  python training/search/three_lane_scoring.py \
  --representation-manifest <output>/manifest/<sha>.json \
  --representation-sha256 <sha> \
  --qrel-manifest-uri <H10b manifest URI> --qrel-sha256 <sha> \
  --split test --top-k 10 --output <new-report.json>
```

隔离匿名SeaweedFS qrel额外传`--s3-endpoint 127.0.0.1:<port>`，此时URI是`bucket/key`。`--output`必须是现有目录中的**新文件**，写单个UTF-8 sort-key紧凑JSON、不覆盖旧文件。父验收应对train/validation/test各运行一次，直接对test报告逐路比较Go内核原值与TopK。

报告顶层和每query/每lane的`evaluation.status`均固定`not_evaluable`，Recall/MRR/nDCG为null。`judged_coverage`明确TopK及候选全集中的已判断数与未判断TopK键。H10.b当前没有证明候选全集判断完整的权威收据；即使本次TopK恰好全已判断，也不能把未判候选当等级0或报告正式检索质量。已知手算fixture和当前仓库qrel均为合成输入，数值对照只能验三路实现与版本合同，不能证明业务提升、真实人工相关性、ANN规模或DC模型激活。

2026-09-15 独立真实权重回放：锁定的BGE-M3 CPU编码产物`/private/tmp/sea-bge-qrel-three-lane.ZMGToD/run/manifest/719b4994b37133a8c6a82e60b333f3d015c4a8d1d9f965f7a2cb7c0a226a68d9.json`与H10.b v2合成qrel SHA`10ae33b8dd5b5fc634493a5eddfdbf47105c16c7c9ced57279eee96c13b1bdb8`原字节接纳。三个split各2个query，输出单文件SHA分别为train`43ceef142786924eb266ace93b65875ce32951df9a7c935b26e72733582e3e58`、validation`2129f2177fea038258775f4ecaf88dd15c36329d9b0a324c04396f026d2a8d82`、test`200d3e2c4f9229dc85efa33e4ebbcc2b0379975f99b8e15482ef8888c3703355`。test每query的6个时间可用chunk里只有2个有judgment，三路Top3各仅1个已判，其余保持`grade=null`，所有正式指标不可评。锁定模型权重验证由编码端负责；本端校验表示工件原字节、模型锁/profile及评分算术，不把模型向量一致性当作人工相关性。
