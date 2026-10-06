# BreakTheWaves

面向完整文本物品的搜索、推荐和有据 Agent。外部每次提供 Ontology；BTW 保存来源与原文，完成内容加工、索引、检索、排序和反馈。一个 go-zero API 直接调用 Search、Recommend、Async 领域能力，后台 Worker 处理资料和索引。

## 搜索

`POST /api/search` 使用 `search_level` 选择三档：

- `fast`：Elasticsearch 全文、短语和精确名称，配合 Neo4j 直接内容关系；不调用表示或生成模型。
- `balanced`：生成模型进行一次有界查询规划；对原问题和规划表达并行查询 Elasticsearch、Neo4j、Milvus Dense 与学习型 Sparse，融合后在已配置且可用时使用搜索排序模型。
- `deep`：拆解复杂问题，增加独立多向量和更深的内容关系，按已回读的证据缺口追加受限检索，核查有限窗口的 MaxSim。

三档都只检索已接纳的书籍和网站文本。搜索返回物品及可回读引用；`POST /api/answer` 在证据上生成回答。推荐继续按网站列表或有据计划交付。

[搜索架构 HTML](docs/architecture/search.html) · [推荐架构 HTML](docs/architecture/recommendation.html) · [搜索 Benchmark](docs/architecture/benchmark.md) · [运行与接口](docs/architecture/operations.md) · [实际验证范围](docs/architecture/validation.md) · [项目开发规范](.agents/skills/breakthewaves-workstyle/SKILL.md)

## 运行与工程边界

公开接口由 `api/btw.api` 经 goctl 生成到 `service/btw/api`；handler、logic、svc、config、types 保持 go-zero 分工。领域能力留在 Search、Recommend、Async，共享稳定表示和 Elasticsearch／Neo4j／Milvus 技术适配。Kafka 只传输入和行为，PostgreSQL 保存业务状态，S3 兼容存储保存完整正文，ClickHouse 聚合反馈。

```sh
goctl api go -api api/btw.api -dir service/btw/api -style goZero
go run ./service/async/rpc/cmd/worker -f service/btw/api/etc/btw-api.yaml -command prepare
go run ./service/async/rpc/cmd/worker -f service/btw/api/etc/btw-api.yaml -command build-index
go run ./service/btw/api -f service/btw/api/etc/btw-api.yaml
```

准备凭据和隔离中间件见 [部署说明](deploy/README.md)。代码发布由 Git 和部署平台切换整套环境；BTW 不登记、激活或回退模型／索引发布代次。文章来源修订属于业务输入，用于乱序更新及原文定位。

BTW 的模型配置只有一个 DataCenter 地址。API 与 Worker 启动时从 DataCenter 读取当前表示／排序合同，生成调用也经 DataCenter 的逻辑模型路由；部署层单独挂载可轮换的工作负载凭据，不在 BTW YAML 保存供应商、模型名称或密钥。详见 [运行合同](docs/architecture/operations.md)。

隔离的 PostgreSQL、Elasticsearch、Neo4j 与真实 BGE／生成模型已完成小样本联合验证；向量通道在该测试中使用精确数值参考实现，尚未联测物理 Milvus。Kafka、S3、ClickHouse 的联合验收及规模压测也未完成，不能从小样本推断生产容量。详见验证记录。
