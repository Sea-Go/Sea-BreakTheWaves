# devseed —— 冻结种子集 → 只读 Store 的装载器（dev 形态）

`service/search/rpc/internal/devseed` 把冻结种子集语料
（[testdata/index/seeds](../../../../../testdata/index/seeds/)，冻结
声明见该目录 README）装载成检索侧可直接查询的只读 `retrieval.Store`：

```
corpus/*.md ──loadCorpusDocs──▶ Doc{source, revision=sha256[:16], structure_ref}
    │ fakerepr.Encode（种子 = doc_key‖structure_ref‖revision_id，镜像 cmd/indexer）
    ▼
QuantizeF32/EncodeImpact/QuantizeMulti ──buildSink──▶ sink{manifestID/<内容寻址ref>, manifest.v1.json}
    │ retrieval.Load（解码三路）
    ▼
Store ──AttachSource（DeriveTree：markdown→结构树，RTW structure.Derive 的 dev 替身）──▶ Corpus
```

## 消费方

[cmd/retr_eval](../../cmd/retr_eval/)（评测入口）、
[cmd/search_demo](../../cmd/search_demo/)（管线演示入口）、
[internal/pipeline](../../internal/pipeline/) 的种子集测试。抽出共享包
是为了避免 seeds 装载口径出现多份拷贝（cmd/retr_eval 曾自带完整副本，
本包即其上提）。

## API

| 入口 | 说明 |
| --- | --- |
| `LoadCorpus(seedsDir) (*Corpus, error)` | 跑通上图全链路；任一环节失败即整体失败 |
| `Corpus{Store, Docs, ManifestID, EncoderID}` | 只读检索库 + 语料清单 + 工件元数据（供演示/对账输出） |
| `DeriveTree(source, revisionID)` | markdown → `evidence.TreeJSON`（空行分块；导读/分隔线/尾注跳过；段落计数口径与种子集构造一致） |

## dev 口径与同步义务

- `ReleaseID` 冻结为 `retr-eval-v1`：manifest_id 是内容寻址（含
  release_id），改动会改变 manifest_id 并使已记录的评测输出不可对照
  （cmd/retr_eval/README.md 样例）；
- 假编码口径在 [internal/fakerepr](../fakerepr/)（镜像
  service/async/rpc/cmd/indexer 的 fake.go，双边同步义务见其包注释）；
- 结构树派生是 RTW `structure.Derive` 的 dev 替身，真实化后由冻结修订
  侧产出、本包只消费；
- 只消费工件形状，不做索引生产/切换（归 M2 indexer 与
  artifact.Switcher 层）、不做持久化与网络 IO。

## 验收

```sh
go vet ./service/search/rpc/internal/devseed/...
GOCACHE=/tmp/gocache-e2e go test -race ./service/search/rpc/internal/devseed/... ./service/search/rpc/cmd/search_demo/... ./service/search/rpc/internal/pipeline/...
```
