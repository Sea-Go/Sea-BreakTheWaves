# Sea 检索评测种子集（M0，已冻结）

## 冻结声明

本种子集于 **2026-10-06 冻结（v1）**，后续一切检索 / 召回 / 排序的**超参与策略调参，只允许在本集上评估**：

- 禁止把评测结论反写入本目录任何文件（不得为让指标好看而修改语料、查询或标注）；
- 禁止在本集上训练后再在本集上报告同一指标（如需训练集，必须另行切分）；
- 需要修订标注或扩充规模时，**新建版本目录**（如 `seeds-v2/`）并在其中声明与 v1 的差异，本目录内容保持不变；
- 对本集的任何改动都必须能通过 `scripts/gen_seeds_test.go` 的逐字节一致校验（即：改动只能来自生成脚本的显式版本升级）。

## 规模统计

| 项目 | 数值 |
| --- | --- |
| 文档数（`corpus/doc-00.md` .. `doc-19.md`） | 20 |
| 查询数（`queries.jsonl`，`q-00` .. `q-49`） | 50 |
| qrels 行数（`qrels.txt`，仅 rel>0） | 140 |
| 正文段落总数（每篇 8-15 段） | 230 |
| gold 相关性分布 | rel=3 × 50，rel=2 × 50，rel=1 × 40 |
| 每条查询 gold 文档数 | 2 篇 ×10 条，3 篇 ×40 条 |
| locator_spans 总数（每条 2 个） | 100 |
| 主题域 | 5（海洋观测 / 气候能源 / 城市交通 / 精准农业 / 深空探测），每域 4 篇文档、10 条查询 |

## 目录结构

```
testdata/index/seeds/
├── README.md            # 本文件：冻结声明 + 构造口径
├── corpus/              # 20 篇确定性合成中文 markdown
│   └── doc-00.md .. doc-19.md
├── queries.jsonl        # 50 条查询 + gold（doc_rels + locator_spans）
├── qrels.txt            # trec qrels 风格：qid 0 docid rel（仅 rel>0 行）
└── scripts/
    ├── gen_seeds.go     # 确定性生成脚本（go:generate 风格，手动运行）
    └── gen_seeds_test.go# 逐字节一致 + 自洽性校验
```

## 文件格式

### queries.jsonl（每行一条查询）

```json
{"qid":"q-00","text":"海洋观测中观测网络建设的核心要点有哪些？",
 "gold":{"doc_rels":{"doc-00":3,"doc-05":2,"doc-10":1},
         "locator_spans":[{"doc":"doc-00","para_index":0,"quote":"海洋观测的第 1 个要点是…"}]}}
```

- `doc_rels`：分级相关性 **0-3**（3=主题主文档，2=域内次文档，1=干扰项；0 分不写入）；
- `locator_spans`：定位证据，`quote` 是 `doc` 第 `para_index` 段中**逐字出现**的连续引文（取该段首句），指向的文档相关性必 ≥2。

### qrels.txt（trec qrels 风格）

空格分隔四列 `qid iteration docid rel`，第二列固定为 `0`（trec_eval 惯例的 iteration 字段），仅保留 rel>0 的行，按 `(qid, docid)` 升序。

## 构造口径

1. **语料**：每篇文档固定为「一级标题 + 导读引用 + 3 个二级章节（背景与约束 / 方法与路线 / 实践与展望）+ 部分章节内的三级子标题 + 尾注」。正文段落数按 `8 + (docIdx*5) % 8` 取 8..15；句子由固定谓语池（12 条）与后果池（12 条）经 `docIdx / paraIdx / sentIdx` 取模选择，句式如「主题 X 的第 N 个要点是…」。
2. **段落计数口径**：`para_index` 按渲染后 markdown 中**空行分隔的块**计，跳过标题（`#` 开头）、导读（`>` 开头）、分隔线（`---`）与尾注（`<!--` 开头）块；段落为单行文本，0 基、全文档连续编号。
3. **查询**：5 主题域 × 5 侧面 × 2 问法角度 = 50 条。问法角度 0 为「核心要点有哪些」，角度 1 为「主流方案如何比较与取舍」。
4. **gold 相关性**：主文档 `doc(域, (a+g)%4)` rel=3；次文档 `doc(域, (a+g+1)%4)` rel=2；当 `(a*2+g)%3==0` 追加一篇域内干扰文档 rel=1；当 `(a+g)%3==1` 追加一篇**跨域**（`(域+2)%5`）干扰文档 rel=1。两条件互斥且不与主/次文档撞车。
5. **locator_spans**：固定 2 个，指向 rel≥2 的主/次文档；段落下标 `(docIdx*7 + a*3 + g*2 + spanIdx*5) % 段落数`，引文取该段首句。
6. **确定性**：生成过程不涉及时间、随机数、环境变量与网络；`queries.jsonl` 中 `doc_rels` 由 `encoding/json` 输出（键序自动排序），`qrels.txt` 显式排序。

## 再生成与校验

```sh
# 在仓库根目录（输出目录可用 -out 覆盖，默认即本目录）
go run ./testdata/index/seeds/scripts/gen_seeds.go

# 校验：再生成逐字节一致 + 段落/引文/qrels 自洽
go test -race ./testdata/index/seeds/scripts/
```

注意：Go 工具链在 `./...` 通配时会忽略 `testdata` 目录，上述命令需**显式指定包路径**。

## 指标计算

qrels 的消费方为 [service/search/rpc/internal/evalseed](../../../service/search/rpc/internal/evalseed/)（nDCG@10 / Recall@100 / CappedRecall@100 / MRR@10），口径与公式出处见该包 README。
