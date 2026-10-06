# BTW 三档搜索验证记录

本记录对应当前工作树。代码发布由 Git 和部署平台负责；文章来源修订是业务数据。本轮同时修改 DataCenter 的模型网关，其他 Sea 项目、旧数据库和被忽略的私有配置未操作。两个工作树均尚未提交或部署。

## 已执行的验证

| 验证 | 实际结果 | 边界 |
| --- | --- | --- |
| API 与生成工程 | `api/btw.api` 经 goctl 生成到 `service/btw/api`；路由测试验证 `/api/search` 的 `search_level` 穿透到领域请求 | 上游网站和桌宠未切流 |
| 当前 Go 全量回归 | BTW 与 DataCenter 各自 `go test -race -count=1 ./...` 通过；离线理解缓存使用 DataCenter 提供的模型身份 | 通过的是代码及隔离合同测试，不等于新路径的物理联测 |
| 旧搜索全量回归 | 先前隔离 PostgreSQL、Elasticsearch、Neo4j 下 `go test -race -count=1 ./...` 通过 | 发生在迁移至 DataCenter 单地址之前，不能当作新网关物理验收 |
| DataCenter 单地址合同 | BTW 的受控地址客户端和 DataCenter manifest、编码／排序代理、绑定服务账号、Chat 审计复用均有隔离测试 | 尚未在一套真实部署中连接 BTW Worker、DataCenter、推理后端和物理 Milvus |
| fast | 真实 ES＋Neo＋PG 小样本返回物品与 2 处可回读引用；路径含全文和直接关系，无表示或生成模型 | 零模型调用的断言由编排替身测试覆盖；实测样本不是召回率基准 |
| balanced | 真实生成模型一次规划、真实 BGE-M3 编码，全文、内容图、Dense 和学习型 Sparse 各自产生候选，引用回读通过 | 向量索引使用 Exact 数值参考，不是物理 Milvus；没有目标语料排序效果数据 |
| deep | 真实模型在一个小样本中首轮就取得足够证据；路径包含独立多向量和有限窗口精确 MaxSim，引用回读通过 | “首轮不足→改写→再查”的行为由可重复的编排测试覆盖，尚无真实语料的多轮取证实测 |
| 原文与恢复 | 隔离 PG／本地对象存储覆盖 EPUB 目录、跨章节原文、17 MiB 文件、重复／乱序／删除、过期租约和索引追赶；真实 ES＋Neo＋PG 测试覆盖来源到两种外部投影及删除 | 本地对象存储不是物理 S3；不代表跨集群恢复 |
| 排序与表示 | 原先 BTW 的 Python 六项测试已迁到 DataCenter 并再次通过：真实 BGE-M3 三种输出、选择性编码、LightGBM 搜索排序训练与加载、固定用户物品投影 | DataCenter 现在拥有模型装载与训练脚本；不证明生产个性化或搜索效果提升 |
| 三档压测入口 | 分段 HTTP 响应测试确认完整响应计时；另以真实 PostgreSQL、Elasticsearch、Neo4j、BGE 与生成模型对同一单物品执行 20／5／3 次 fast／balanced／deep 请求，并追加 10 次 deep 重复 | 在线样本只有一个物品，向量索引是 Exact；延迟不是生产 SLA 或真实语料质量证据 |
| 公开检索 Benchmark | BEIR SciFact 5,183 篇、300 查询的 Elasticsearch 全文 Recall@10 为 0.6065；24 篇正例必入子集对照真实 BGE 三种表示；1,000 来源合成图测试 Neo4j 1／2／3 跳 | 三组实验衡量的对象不同，不能拼接为三档端到端质量或亿级容量；见[实验方法与结果](benchmark.md) |
| 容量数值参考 | 一万条合成根物品、32 维、每物品两条子向量，20 次本机 Exact 查询，p50 11.79 ms、p95 15.68 ms | 只作为数值参考，未测中间件联机性能或真实长书子向量规模 |

[当前 Go 回归记录](validation-current-go.txt) · [迁移前 Python 回归记录](validation-current-python.txt) · [迁移前物理与三档调用记录](validation-current-physical.txt) · [搜索 Benchmark](benchmark.md) · [万级原始输出](capacity-current-10000.json) · [百万估算](capacity-current-1000000.json) · [亿级估算](capacity-current-100000000.json)。历史模型与中间件日志不代表新 DataCenter 网关已物理验收。

## 物理测试环境与剩余验收

本轮在 `/tmp` 的隔离数据目录启动单机 Elasticsearch 9.5.4、Neo4j Community 2026.09.0、PostgreSQL 16.14，并运行真实 BGE-M3 推理服务和已配置的生成模型。Neo4j 临时测试使用的 Java 26 运行时会提示不在其推荐范围，因此这里只把通过的查询行为作为小样本证据。测试后关闭本轮启动的进程；这不是部署在生产环境的集群。

外部集成测试只有显式设置隔离环境变量才运行：

```sh
BTW_TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
BTW_TEST_ELASTICSEARCH_URL="$TEST_ELASTICSEARCH_URL" \
BTW_TEST_NEO4J_URI="$TEST_NEO4J_URI" \
BTW_TEST_NEO4J_USER="$TEST_NEO4J_USER" \
BTW_TEST_NEO4J_PASSWORD="$TEST_NEO4J_PASSWORD" \
go test -race -count=1 ./...
```

迁移前真实生成模型联测使用测试专用环境变量；它不验证新的 DataCenter 模型入口。当前可选真实模型联测和 BEIR 表示实验都使用 `BTW_TEST_DATACENTER_URL` 或 `-datacenter-url` 加部署层的 `DATACENTER_WORKLOAD_TOKEN_FILE`，从 DataCenter manifest 获取模型合同。DataCenter 当前模型服务身份、BGE 后端及网关各有隔离测试，完整部署联测仍待执行；被忽略的本地凭据不进入仓库。

仍缺物理 Milvus、Kafka、S3、ClickHouse 的完整组合验收，以及真实中英书籍／网站语料上的短语、稀有名词、跨章节证据、真实多轮查询和单通道故障数据。现有 Benchmark 已提供单路与小样本在线基线；下一步仍需在同一完整标注语料上分别测三档 Recall、NDCG、p50／p95／p99、并发吞吐、模型调用量、磁盘与 RAM，实测后再制定延迟目标。

三档在线压测入口为 `go run ./service/async/rpc/cmd/capacity -mode api -api-url <隔离地址> -queries-file <查询集.jsonl>`。没有相关性标注时不输出质量分。百万、亿级容量 JSON 都明确标记 `executed_items=0`；它们未计真实 Elasticsearch 全文索引、Neo4j 内容图、书内对象和长书的实际子向量数，不能推出亿级生产能力。
