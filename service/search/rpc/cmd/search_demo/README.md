# search_demo —— 端到端检索管线演示入口（dev 形态）

`service/search/rpc/cmd/search_demo` 把
[internal/pipeline](../../internal/pipeline/) 跑成可交互的端到端演示：
加载冻结种子集（[testdata/index/seeds](../../../../../testdata/index/seeds/)，
经 [internal/devseed](../../internal/devseed/)：假编码→工件量化→装载→
补源）→ 建只读 Store → 装配默认管线 → 交互式查询 → 打印三档
（fast/balanced/deep）× 两种交付（summary/tools）的结果块。

每个结果块依次打印：

- `route`：判档路由（`suggest × user → effective`，MaxTier 只升不降）；
- `pack`/`top`：候选数与前 5 候选的 RRF 分数与路分数快照；
- `answer`/`cites`（summary 交付）：格式化答案（`[n]` 角标 →
  `[n](#cit-n)` 锚点链接）+ 脚注式引用列表；
- `tools`（tools 交付）：EvidencePack 直返标记（不经 B6 摘要）。

## 用法

```sh
# 交互式（TTY 下带提示符；管道/参数则逐条执行）
GOCACHE=/tmp/gocache-e2e go run ./service/search/rpc/cmd/search_demo

# 指定档位与交付（all 为默认，展开为三档 × 两交付的完整矩阵）
GOCACHE=/tmp/gocache-e2e go run ./service/search/rpc/cmd/search_demo \
  [--seeds testdata/index/seeds] [--tier fast|balanced|deep|all] \
  [--delivery summary|tools|all] [查询…]
```

## 样例输出（2026-10-06，integration/llm-wiki-20261006 @ 5c069b8）

```console
$ echo "海洋观测中观测网络建设的核心要点有哪些？" | \
    GOCACHE=/tmp/gocache-e2e go run ./service/search/rpc/cmd/search_demo --tier fast --delivery summary
search_demo dev: seeds=testdata/index/seeds docs=20 encoder=fake-encoder.v1 manifest_id=e6c948f0031769afc4e0c8c3bce293d6 tiers=[fast] deliveries=[summary]

> 海洋观测中观测网络建设的核心要点有哪些？
--- tier=fast delivery=summary ---
route  : suggest=fast × user=fast → effective=fast（MaxTier 只升不降）
pack   : 11 candidates
top    : doc-02(rrf=0.0164 d=0.198 s=0.000 m=0.000) doc-06(rrf=0.0161 d=0.168 s=0.000 m=0.000) doc-16(rrf=0.0159 d=0.142 s=0.000 m=0.000) …
answer : 根据 11 篇文档……
         「城市交通的第 1 个要点是控制全链路的误差累积，……」[1](#cit-1)
         「气候能源的第 1 个要点是量化不确定性并公开披露，……」[2](#cit-2)
         「气候能源的第 1 个要点是对齐跨团队的口径定义，……」[3](#cit-3)
cites  : [1] doc-02 §城市交通研究纪要（第 1 卷）/城市交通的背景与约束 ¶0 — 城市交通的第 1 个要点是……
         [2] doc-06 §气候能源研究纪要（第 2 卷）/气候能源的背景与约束 ¶0 — 气候能源的第 1 个要点是……
         [3] doc-16 §气候能源研究纪要（第 4 卷）/气候能源的背景与约束 ¶0 — 气候能源的第 1 个要点是……
```

解读（dev 口径，详见 pipeline README 的"dev 口径声明"）：

- `s=0.000`：查询与文档的 sparse term 空间不相交（假编码种子不同），
  fast 档命中全部来自 dense 路；balanced/deep 档可见 `m>`0 的 multi 路；
- 引用列表 = 证据包前 3 候选的首条 Locator（`summary.StubSummarizer`
  的确定性口径），quote 为文档首段整段（B3 dev 命中定位近似）；
- 含触发词（比较/分析/为什么）的查询会把低档请求升到 deep（MaxTier
  只升不降），`route` 行可直接观察。

## 验收

```sh
GOCACHE=/tmp/gocache-e2e go test -race -count=1 ./service/search/rpc/cmd/search_demo/...
GOCACHE=/tmp/gocache-e2e go vet ./service/search/rpc/cmd/search_demo/...
```

同步义务：seeds 装载与假编码口径在 `internal/devseed` +
`internal/fakerepr`（与 cmd/retr_eval 共享；后者另镜像
service/async/rpc/cmd/indexer 的假编码，双边同步声明见其包注释）。
