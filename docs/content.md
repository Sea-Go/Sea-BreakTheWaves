# 内容构建与来源契约

此模块落实 WS06-A 的固定输入、可追溯分块、构建账本和就绪对账部分。它不拥有 RTW 的正式修订或活动发布指针。根 Go module 与 runtime/SDK 共用，数仓和训练语言环境保持独立。

## 区域和依赖

- W1：`service/async/rpc/internal/content/`、`service/common/artifacts/`、`service/common/corpus/`、`service/async/rpc/internal/content/test-postgres.sh`及本文；root是唯一writer。
- R1：RTW权威 `api/knowledge.api`、DC H04/H05、其他领域与原始在途工作树。
- D1：tRPC-Agent-Go核心v1.8.1与pgx/v5；不修改模块缓存或复制框架internal。
- G1：provider DTO由runtime任务生成，content只消费。
- X1/T1：随机端口任务专属PG16与对象目录；无生产部署。
- 主职责C2/C3/C4，跨区H03固定修订、H04执行权、H06工件、后续三路检索消费。

## 已实现流程

`NewPreparer`读取已认领的RTW build、固定release和每个确切revision。manifest对象hash、profile、完整修订集合及DC/RTW attempt/epoch/expiry必须一致。它通过生成SDK读取HTTP产品对象，不能直连RTW数据库。外部IO和分块都在PG事务外；最后登记引用时再校验本地租约和tombstone。准备完成仍为BUILDING。

`NewChunker`复用公开 `knowledge/chunking.NewFixedSizeChunking`，先沿RTW `paragraph:N`定义划分来源段落，再使用框架的UTF-8安全切分与overlap。片段保留原文对象、原始段落字节范围、规范化rune span及前后邻接。修订和分块profile参与chunk身份；重复文本共享encoding key，保留不同chunk ID与来源。采用固定排序且不写生成时间，重放生成相同manifest字节。

分块profile在PG首次绑定精确参数和parser/chunker版本；同ID更换参数会拒绝，必须使用新ID。未知格式、坏UTF-8、NUL、hash不匹配或任一必需正文为空均使整个准备失败，不能静默剔除后缩小覆盖分母。当前正式支持UTF-8 text/plain及text/markdown；PDF/OCR与更细Markdown标题结构仍未实现。

表结构由 `service/async/rpc/internal/content/schema.go` 的 GORM Model 声明，并由 `content.Migrate` 初始化。

`Store`保存内容领域的固定输入、当前DC fence、分块引用、各lane引用及Outbox。它不分配DC任务或私自创造执行权。并发重试只接受当前epoch；相同工件可重放，不同工件不能覆盖。取消版本和永久修订tombstone拒绝后续构建；旧失效事件不能回退状态。最终READY和Outbox同事务，最后SQL再次校验数据库时间，避免等待/Outbox写入拖过租约后提交。

`Reconciler`注入固定修订读取接口，从原始H03对象读取三个profile，重新获取权威原文并用已绑定分块规则重建预期ChunkManifest，要求其完整hash等于提交清单，再核验每lane的generation、输入hash、ChunkManifest、完整逻辑chunk集合和实际分片hash。每一路必须注入其真实 `LaneVerifier`，完成数值/空间/shape检查及独立查询；没有默认通过实现。first/middle/last固定查询探针只证明可读就绪，不代替召回效果评测。只有三路验证通过才产生与RTW H06 v1同形的IndexManifest，并提交本地READY/Outbox。已接纳READY重放复用原工件，不重新运行模型或近似查询。

`internal/artifacts`仅承接RTW/BTW共享的`sha256/<hash>`不可变对象合同。检查过框架artifact.Service：其身份为session+filename+整数版本（允许latest），无法直接表达此跨服务hash引用；因此这里使用窄Store，不替代Runner的session artifact服务。当前只有开发Local适配，不作为生产S3实现。

## 验收

在仓根运行 `CONTENT_KEEP_EVIDENCE=1 bash service/async/rpc/internal/content/test-postgres.sh`。脚本自行创建并停止专属PG16，每例独立schema，执行content/artifacts的race和vet。直接go test未提供CONTENT_TEST_POSTGRES_DSN时会跳过PG测试，不等于完成真实数据库验收。

已通过：CRLF/中文/重复字符/overlap来源位置；输入排序与重放同hash；重复文本不丢修订；空/坏必需输入整批拒绝；16代并发fence；三路缺失、缺片、重复/外来ID、错空间、损坏对象、查询失败；取消、过期、乱序tombstone和新代不得复活；Outbox写入耗时导致过期时READY及Outbox全回滚；同profile偷偷变参拒绝。

本页原始READY测试中的lane对象和verifier仍是合成结构替身；Dense/Sparse/Multi-vector已在各自独立目录完成局部真实数值/引擎验收，但尚未同代接入本Reconciler，不能宣称三路算法READY或WS06-A全项完成。下一步仍需：Wiki编制Agent、三路索引worker及真实同代对账、RTW结果/Outbox派送恢复、生产对象存储、领域失败/重试与跨lease重绑已准备结果。当前READY仅为内容领域记录，不会自动调用RTW发布接口。

## 独立审查修正

5779b3a的独立审查复现：经公开RecordChunks登记自洽的伪清单，可把3块缩成1块、把位置改为paragraph:999或修改chunk ID后仍READY。正常Preparer生成正确，但对账不应信任另一生产者自报覆盖。现已收起RecordChunks登记入口，并在首次READY前用RTW固定ID的权威原文重新生成expected manifest，与提交hash精确比较；三个反例均拒绝，状态/Outbox不推进。真实PG16全content/artifacts race与vet复验通过。现有READY的丢收据重试继续复用已接纳工件，不重复近似查询。


观测接口补充：Preparer及Reconciler必须接收已安装的公共Telemetry Bundle；开始和最终结果围绕固定build、release、attempt/lease记录，不在纯分块函数与PG helper散落日志。对账拒绝时保留error_code与候选工件hash供查验；成功只在对应Store提交后记录。真实PG测试已覆盖错误清单与取消/过期的结构化终态。`content_prepare`已通过tRPC GraphAgent/Runner执行Preparer，Graph完成事件只输出chunk Ref/数量并继续等待Runner完成；本地worker进程和隔离真实DC/RTW联验均验证准备阶段跨服务traceparent，RTW build仍BUILDING。异步Outbox Link、三路worker、Collector/DC查询仍缺，OBS整体仍为LOCAL_VERIFIED局部。
