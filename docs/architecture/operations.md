# BTW 运行与接口合同

## 进程和依赖

在线只有统一的 go-zero API；Worker 分别运行接纳、加工、反馈和索引建设角色。BTW YAML 的模型依赖只有 `DataCenterURL`。API 与加工 Worker 启动时向 DataCenter 读取当前表示模型、空间、维度、检索集合及逻辑生成模型别名；编码、排序、物品投影和生成调用均走这个地址。PostgreSQL、S3 兼容对象存储、Elasticsearch、Neo4j 和 Milvus 仍由 BTW 持有各自的数据职责。首次部署先由 Worker 建索引并追赶来源变化，再开放 API 流量。

DataCenter 工作负载凭据由部署层以只读文件挂载，文件路径通过 `DATACENTER_WORKLOAD_TOKEN_FILE` 传入运行环境；BTW YAML 不保存凭据。客户端每次调用读取文件，支持部署侧轮换；DataCenter 将凭据摘要绑定到一个 active 服务账号，并在生成调用中继续执行其现有幂等、额度和审计流程。缺凭据、DataCenter 未启用模型运行合同或表示空间不匹配时明确失败，不生成假向量／假答案。

fast 每次请求不调用模型；但当前 API 启动仍需从 DataCenter 读取模型与索引合同，DataCenter 在启动时不可用会阻止 API 就绪。进程已启动后，fast 继续由全文、内容图和 PostgreSQL 完成，不再向 DataCenter 发模型请求。

公开路径不含 BTW 发布版本号。接口从 `api/btw.api` 生成；带 `DO NOT EDIT` 的类型与路由只从该契约更新。DataCenter 的 `/v1/model/manifest` 与受控编码／排序／物品塔接口给出当前部署的模型合同；BTW 不再保存物理模型路径、模型名或表示维度配置。

## 搜索等级

`POST /api/search` 接收 `request_id、ontology、scenario、query/context、limit`，以及可选 `search_level`。等级只能是 `fast`、`balanced` 或 `deep`；不传使用 `balanced`，其他值返回 400。`/api/answer` 的同一参数只影响取证阶段；推荐接口不接受它。资料范围、候选预算和总超时始终由服务器场景配置。

| 等级 | 在线步骤 | 交付 |
| --- | --- | --- |
| fast | 原问题一次 Elasticsearch BM25／短语检索，再用 Neo4j 查询直接内容关系；不调用表示或生成模型 | 物品、章节和核实的原文位置 |
| balanced | 一次模型查询规划，最多额外两种表达；各表达并行查 Elasticsearch、Neo4j、Milvus Dense／学习型 Sparse，先表达内再表达间融合 | 排序物品、多路得分和出处；规划失败明确降级 |
| deep | 最多四个子问题；加入独立多向量、三跳有据关系和有限窗口精确核查；按证据缺口最多三轮、十二条不同查询 | 多处引用；依据不足和未执行完的步骤明确标识 |

普通搜索返回 `hits、scores、citations、status、path、warnings`。`path` 只记录实际完成的通道和轮次；不能以高亮、相似分或模型生成文本冒充保存原文。部分中间件故障返回 `degraded`，全部通道失败返回依赖错误，用户取消终止本轮。deep 的证据覆盖由模型判断，但返回前核对其引用 ID 与实际读取的正文。搜索结果并不自动生成长篇回答。

推荐仍使用 `POST /api/recommend` 和生成场景的 `/api/recommend/stream`。网站列表在召回前排除 Book 和衍生对象，不调用生成模型；计划场景读取原文、生成结构化计划和引用。`POST /api/answer` 及 `/api/answer/stream` 在搜索证据上交付有据答案。

## 完整原文与 Kafka

电子书上传使用 `POST /api/books?request_id=...&filename=...`，body 为原始 EPUB、UTF-8 TXT 或 Markdown。返回 202 表示原文件和任务已持久接纳，不表示内容理解及索引已完成。不支持的格式仍保存原文件，任务标记明确错误。其他读接口：

- `GET /api/items/{id}`、`/api/items/{id}/structure`：当前物品、有限关系及分页自然目录。
- `GET /api/sources/{revision}/content?section=...&start=...&end=...`：读取保存的规范化章节正文，位置采用 Unicode 码点半开区间。
- `GET /api/sources/{revision}/file`：原文件流和单个字节 Range。
- `GET /api/jobs/{id}`：阶段、尝试、错误及已处理章节覆盖。
- `POST /api/events`：曝光、点击与明确反馈；服务端生成候选不算曝光。

网站内容事件的业务字段为 `producer、event_id、source_id、revision、operation、source_url、media_type、body 或 object_ref、occurred_at`。这里的 revision 是**文章内容更新顺序**，用于拒绝晚到旧内容和保留原文出处，与 Git 代码发布无关。对象引用只指向已配置的存储，不从 URL 抓取网页。原文与 PostgreSQL 事务接纳成功后才确认 Kafka；模型与索引故障由 Worker 重试。删除首先修改权威可见性，旧索引行不会重新激活内容。

## Worker 与部署切流

```sh
go run ./service/async/rpc/cmd/worker -f service/btw/api/etc/btw-api.yaml -command prepare
go run ./service/async/rpc/cmd/worker -f service/btw/api/etc/btw-api.yaml -command consume
go run ./service/async/rpc/cmd/worker -f service/btw/api/etc/btw-api.yaml -command process
go run ./service/async/rpc/cmd/worker -f service/btw/api/etc/btw-api.yaml -command build-index
```

`prepare` 使用 DataCenter 当前表示合同建立 BTW 的索引与约束；`build-index` 从当前可见原文重建向量、全文和内容图，并追赶期间的来源变化。`status -id`、`retry -id` 处理 BTW 自有任务。`samples` 与 `behavior-samples` 导出有出处的合成／行为样本；模型训练、装载和物理推理后端由 DataCenter 管理，BTW Worker 不再提供模型训练或文件激活命令。

三档对照使用同一 JSONL 查询集执行 `go run ./service/async/rpc/cmd/capacity -mode api -api-url <隔离环境地址> -queries-file <查询集.jsonl>`。每行至少包含 `query` 和 `ontology.user_id`，可包含 `relevant_ids`、`scenario`、`limit`。程序实际请求三个等级，报告成功、降级、依据不足、p50／p95／p99 和吞吐；只有提供相关物品标注时才报告 Recall 与 NDCG。后端磁盘、RAM、索引规模需另从实际服务记录。

新的 Git 提交与 DataCenter 已就绪的模型合同进入独立环境；BTW 按该合同完成索引构建和来源追赶，经真实中间件验收后由部署平台整体切流。DataCenter 改变表示空间但 BTW 尚未重建索引时应拒绝旧请求，不能静默混用。BTW 不提供模型登记、激活或回退命令；旧数据库、原文和被忽略的本机私有配置不因此删除。

此前在隔离环境对 PostgreSQL、Elasticsearch、Neo4j 与真实模型推理做过小样本三档搜索；那组实验尚未经过新的 DataCenter 模型网关。新合同目前有双仓库的隔离适配测试，仍需服务身份、物理 Milvus、Kafka、S3、ClickHouse 和完整部署联测；具体结果见 [验证报告](validation.md)。
