# go-zero 在 BTW 中的开发规范

本页规定搜广推与 Agent 能力怎样进入现有 go-zero 服务。与 [tRPC-Agent-Go 规范](trpc-agent-go.md) 配合执行；一次功能横跨两边时使用 [联合开发规范](framework-integration.md)。这里的目录／领域约束属于 BTW，框架事实另列来源，不重新讲 Go 命名风格。

## 1. 契约是外部接口的修改入口

| 要增加或改变的内容 | 必须修改 | 必须接通 |
| --- | --- | --- |
| 搜索输入、范围、结果／引用字段 | `api/btw.api` 的 SearchRequest／Result | API mapping → Search 公开能力 → 真实召回／原文消费者 |
| 推荐场景、Ontology、计划输出 | 同一 `.api` 的推荐接口 | Recommend 场景、算法／Agent 与结果映射 |
| 内容上传、阅读、任务状态、反馈 | 同一 `.api` 的 content 分组 | Async content 持久接纳、读取、任务／Worker；接纳不等于加工完成 |
| 样本与索引重建 | Worker 的 samples、prepare、build-index 命令 | BTW 导出样本并建设自身索引；训练与模型装载由 DataCenter 负责 |
| 仅替换算法／模型实现 | 所属领域、装配与配置 | 原用例实际使用；语义未变不新增协议 |

- 路径、方法、请求来源（body/query/path）、字段可选性和响应含义在 `.api` 中表达；JWT、中间件、分组、前缀等沿所属服务的 DSL 约定。鉴权身份与客户端提交的用户字段不能混为同一可信来源。
- 当前不运行内部 RPC，也不保留旧 proto 生成链。只有用户另行要求跨进程服务边界时才引入 `.proto`，按 goctl 和字段编号兼容规范落实；不得因为领域路径包含 rpc 就新增网络中转。
- 对算法参数定义未传、零值及空集合的含义。返回字段必须来自真实候选／模型／证据产物，不能为填充 schema 生成虚假分数、引用或成功状态。
- 传输对象在接入与结果边界转换；领域算法不接收 HTTP 请求对象，Tool 不把 `.api` 请求体原样当作工具参数。相同语义的进程内数据直接传递，不为每层复制一套模型。

## 2. 生成物与可编辑实现

1. 修改契约后，用 `goctl api go -api api/btw.api -dir service/btw/api -style goZero` 生成到统一工程；沿相邻产物的生成风格、模板和协议包布局执行，不从框架示例另起项目。
2. 文件头是编辑边界：`Code generated ... DO NOT EDIT` 的 routes/types/pb/client/server 等产物从其生成源更新；`Code scaffolded by goctl. Safe to edit` 的入口、handler、logic 按职责填充。具体以该文件头为准，不仅凭目录名猜测。
3. 生成后保留现有开发者实现，检查只涉及本次契约的差异；不得通过手改生成 client 或复制 pb 绕过生成链。
4. 同步真实输入和结果映射。协议能编译但领域调用仍丢失 Ontology、范围、预算或引用，视为功能未接通。
5. go-zero 库版本与 goctl 版本分别核对，不因库升级批量重生成所有服务。工具不可用时定位／准备匹配工具，不声称已生成，也不将手写结果标为生成结果。

本次核对的库为 `v1.10.3`，现有部分生成文件头为 goctl `1.10.2`；这是当前证据，不是永久锁定要求。纯规范编辑不执行业务生成器。

## 3. 各层怎样承接算法与 Agent

完整目录树见 [项目目录及职责](../SKILL.md#项目目录及职责)。下表是写入责任，不能把全部算法都塞进 logic 或把全部计算都包成 Agent。

| 位置 | 在本项目写什么 | 向下交付什么 |
| --- | --- | --- |
| API handler | 参数接入、调用用例、JSON／SSE 写出，处理连接与写入错误 | 请求 Context 与已解析输入；不执行 SQL、召回、排序或工具循环 |
| API logic | mapping 与场景用例；调用注入的领域公开能力 | 完整 Ontology、范围、请求 Context 与业务结果 |
| API svc | PostgreSQL、blob、Milvus、DataCenter 模型客户端、内容、搜索、推荐、场景 Runner | 同进程稳定依赖及关闭所有权；不再装配旧 RPC client |
| 领域公开入口 | Search／Recommend 的 engine 与 Async content | 可跨 Go internal 边界调用的完整能力，无旧类型别名 |
| Search 私有实现 | 检索组合、RRF、真实排序、证据和 Agent 生成 | 列表／答案／计划及实际路径，无 HTTP writer |
| Recommend 私有实现 | 场景目标、根物品列表、重复簇去重和交付选择 | 有限候选上的列表或受限生成 |
| Async catalog／processing | GORM 状态、内容理解、样本与索引任务 | 完整来源和可恢复产物，不生产用户塔或运行模型训练器 |

API 使用真实公开工厂，领域通过所需接口接收依赖；Worker 独立装配。go-zero 约束契约、接入、用例和装配，不要求额外 RPC 进程。新增 Agent 要进入实际 svc／Worker 依赖并被用例消费，不能只创建工厂文件。

## 4. 配置怎样接到框架能力

| 配置类别 | 归属与接入 | 必须验证 |
| --- | --- | --- |
| HTTP 服务 | 本域 `api/internal/config` 嵌入 `rest.RestConf`，由入口加载 | 路由、请求大小、超时及中间件对算法／长流的实际影响 |
| DataCenter 模型服务 | BTW 运行配置只接收 DataCenter 地址；svc／Worker 用部署层身份读取当前 manifest，再装配表示、排序及生成消费者 | 模型名、空间、维度及逻辑路由取自 DataCenter；缺身份或空间不匹配时明确失败 |
| 检索与存储 | 沿所属配置进入 API svc／Worker／领域工厂；共用实现接收所需部分 | BTW 的 PostgreSQL、对象存储、Milvus、Elasticsearch 与 Neo4j 仍保持各自数据所有权 |
| 场景预算 | 由服务端场景配置进入用例，再传算法与 Agent | 候选数量、调用次数、总时限同时受控；各层不各自补不一致默认值 |
| Worker | 当前 Worker 配置与任务入口 | 后台任务实际选择、数据位置、恢复与停机；不复用前台请求的短生命周期 |

配置类型与无密钥示例随功能更新；深层算法／Tool 不自行读取整份全局配置或新建连接。框架自带限流、超时、追踪等先按现有服务启用方式组合，不另外复制一套同等中间件。

## 5. 超时、取消与请求身份

- 从 HTTP request Context 到用例、Runner、Tool 及存储／模型传递同一取消关系；中间可以创建更短的子期限，不能改用后台 Context 延长前台请求。
- HTTP、RPC 和模型调用预算要匹配任务。当前 `RestConf.Timeout` 及 RPC 配置 Timeout 使用毫秒，路由 `rest.WithTimeout` 接收 `time.Duration`；单位转换集中在装配处，不能误当相同字段单位。
- SSE／流式 RPC 按所用框架版本检查生效的拦截器与路由设置；不要假设普通 RPC 的超时配置对所有流具有相同语义。长流仍需业务总预算，不能只全局关掉超时。
- 身份在可信接入位置建立，经当前协议传入用例，再用于 Runner 的 user/session 与工具权限。Ontology 表达画像，不自动授予其声称的数据范围；框架 Session ID 也不替代权限判断。
- 重试只放在明确负责的位置：模型或 RPC 重试不能重复产生业务写入、曝光或任务提交。框架组件的默认重试与业务重试一并计算总预算。

## 6. 错误、普通响应与流式响应

- 输入错误、不可用依赖、计算失败和取消保留不同业务含义。领域返回原因，HTTP 在统一输出位置转换；不能每层记录相同错误再返回空列表当成功。
- 普通 JSON 在业务结果形成后输出。模型完成事件不单独证明引用齐全、原文可读或业务数据已提交。
- SSE 路由通过项目可生成／维护的配置方式启用当前 go-zero 支持的流式能力；`rest.WithSSE()` 是当前库的路由选项，不代表只设置响应头就已完成全链路配置。不得直接改 `DO NOT EDIT` 路由文件。
- 输出前可用普通 HTTP 错误；响应开始后按流协议发送失败终态并结束，不能再拼一个普通 JSON 错误体。SSE data 按协议序列化，protobuf 的调试 `String()` 不能作为 JSON 编码器。
- gRPC 接收端区别 `io.EOF` 与传输错误，发送／写出失败取消本次运行；不能将所有 `Recv` 错误都视为正常结束。EOF 只表示传输结束，业务成功仍由约定终态和结果决定。
- Runner 事件由业务适配转成稳定的结果／过程协议，再映射 JSON／SSE；不要把框架 Event、Graph State 或供应商响应直接作为公共接口。

当前 `handler/stream` 直接消费领域事件，公共 SSE 来自同一 Runner；终态、取消、写出错误有公开入口测试。改动这条链时按 [联合流式规范](framework-integration.md#错误终态与跨进程事件) 完成对应部分。

## 7. 数据、Worker 与关闭

- schema 由本领域现有 GORM 所有者维护，框架 Session 的 schema 由其后端负责。连接可以共享，业务表／会话表责任不能混合；根级 migrations 与目录边界沿项目现有约束。
- 一次内容输入先达到约定持久接纳点，再确认消费；索引、内容理解与样本生产在 Async Worker 执行，模型训练在 DataCenter。HTTP 接纳成功不能伪装成模型处理和索引已完成。
- Worker 入口在 `service/async/rpc/cmd/worker`，业务在 content 与 internal/catalog、processing 中；在 Worker 里使用 tRPC 框架也不需要额外启动一个 HTTP Agent 服务。
- 应用关闭时停止新工作、取消并等待本应用所拥有的在途工作，再释放依赖。go-zero 服务停止、Runner 关闭、外部 Session／数据库关闭分别有所有者；不要假定一次 `Stop` 或 `Close` 自动完成另一框架的所有资源回收。
- 具体 Runner 资源边界见 [联合生命周期](framework-integration.md#配置预算状态与生命周期)。数据库、消息确认和模型训练仅按当前任务的授权范围实施。

## 按影响执行验证

先验证受影响路径：参数到领域输入、算法实际选择、业务结果到协议输出、失败和取消。SSE 测试读取公共流至 EOF，检查终态及完整结构化结果；算法效果另外按 [能力落地规范](writing.md) 评测。

目录／import／schema 改动追加对应检查；以下不是所有任务的强制全量测试清单：

```sh
go test ./service/search/...
go test ./service/recommend/...
go test ./service/async/...
go test ./service -run TestServiceImportBoundaries
go test . -run 'TestAncillaryScriptAndMigrationDirectoriesStayAbsent|TestApplicationSchemaStaysInDomainGORMOwners'
```

## 规范来源与版本核对

编码／评审前实际加载 [官方项目结构](https://go-zero.dev/zh-cn/concepts/project-structure/)、[官方代码规范](https://go-zero.dev/zh-cn/community/code-style/)；接口变化读取 [API DSL](https://go-zero.dev/reference/api-dsl/) 或 [goctl RPC](https://go-zero.dev/zh-cn/reference/cli-guide/rpc/)。本页描述它们在 BTW 中的应用，不替代原始来源。

用 `go list -m -json github.com/zeromicro/go-zero` 核对 Dir／Replace，生成器另查版本。2026-09-23 已按当前 `v1.10.3` 检查 [RestConf](https://github.com/zeromicro/go-zero/blob/v1.10.3/rest/config.go)、[HTTP 路由选项](https://github.com/zeromicro/go-zero/blob/v1.10.3/rest/server.go)、[RPC 配置](https://github.com/zeromicro/go-zero/blob/v1.10.3/zrpc/config.go)及[客户端配置转换](https://github.com/zeromicro/go-zero/blob/v1.10.3/zrpc/client.go)。

BTW 目录、schema 和当前 API→进程内领域约束来自 [仓库架构说明](../../../../docs/ARCHITECTURE_SEARCH_RECOMMEND_ASYNC.md)、实际入口及边界测试。通用代码格式沿原框架规范，不在此另列命名教程；上游 PR 流程和覆盖率目标不自动变成本项目每次修改的额外任务。
