# Wiki 质量评阅来源 ODS

RTW Knowledge 的 `producer=ridethewind.knowledge` 与 Wiki、搜索判定、发布事件共用 DataCenter 的连续 offset。本包使用自己的 `btw-warehouse-wiki-quality` consumer 与 PG 游标，完整保留每条 DataCenter EventSpec 的 JCS、输入哈希和技术收据；非质量事件显式标记 `technical_skip`，不会计作人审 Fact。未知 `knowledge.wiki.quality.*` 版本一律拒绝。将来的完整事实目录事件若属 `knowledge.wiki.fact-set.*` 或 `knowledge.wiki.fact-catalog.*`，在RTW生产者/数仓消费者冻结一致的类型与原Event读口前也阻断整个批次，不能跳过目录后把零散评阅冒作完整标签。

RTW 开发源 `feat/wiki-quality-human-judgment-20260916@6865022` 已固定 `knowledge.wiki.quality.judged.v1` 的九键外层 Event、三十二键单 Fact payload、原 Event 不含末尾 LF 的 SHA、RFC 8785 JCS SHA 与 Worker 私有 GET；`V1Verifier` 按该源 Golden 精确核缺漏/重复字段、版本、FactID、原引文 SHA 和自身 byte span、AI/人工修订、0–3/undetermined、来源撤回和历史重判。RTW 私有 GET 还按其不可变 Source/Wiki 对象复核当时引用。接纳时必须同时配置原字节 Authority 与正式 Verifier：先核 RTW 原 byte SHA，再核 RTW/DC 整 Event JCS 和 DC input hash，最后验 payload，才能写 `quality_verified`；缺席时整批在 PG 与 DC ACK 前拒绝。测试夹具 `syntheticVerifier` 仅证明事务行为，不能替代 RTW 真实评阅业务源。

`actor_id` 仅是当时获准的 RTW 管理员 JWT+名单 `userId` 字面值，可是字符串；它没有实时 UserCenter UID 资格。单 Fact 事件也不证明页面/SourceScope 的预期事实全集已列全，不能由 ODS 已收到几条评阅推导 D07 覆盖率或真人页面达标。正式 FactSet 清单、真实后台资格与外部来源的跨进程同父仍另签；产品主体不引入租户字段。

RTW默认关闭的完整目录源`feat/wiki-fact-catalog-20260916@95ce8fc`已经固定独立`knowledge.wiki.fact-set.frozen.v1` Event与原字节/JCS/目录payload三个SHA的静态Golden。新`ParseFactSetV1`从同一固定Golden核两份SourceRevision的完整范围JCS、每条FactID/原quote自身byte span、必覆盖与矛盾组、AI/manual及历史FactSetRevisionID；`HTTPAuthority.ReadFactSetEvent`从RTW Worker私有GET分别核三域。`facts_complete`只是管理员JWT名单对所列目录的声明，不证明所有事实真的被列出，也不等于UserCenter规范UID。现运行的`btw-warehouse-wiki-quality`游标仍**拒绝**FactSet家族，RTW不能开启该生产者类型；这些纯解码器/HTTP夹具尚未让数仓写目录ODS行或ACK。后续必须在同一连续DC producer prefix上先验真FactSet源、N条单Fact评阅修订来源及独立PG游标，再给评测Dataset聚合证据，不能用静态Golden装作同父。

ODS 游标、原始 Event 字节和所有技术位置在同一 PG 事务接纳，提交之后才 ACK DataCenter。ACK 丢失后的相同 batch 会核每条历史 EventID/hash/JCS/receipt/RTW 原字节后重试 ACK，改变过的收据或来源不能靠重放覆盖旧标签。`HTTPAuthority` 按固定私有 Worker 路径读取原 Event 字节、原 SHA 与 JCS SHA，重定向/坏回执拒；其普通 HTTP 测试仍是夹具。`bash internal/warehouse/wikiqualitysource/acceptance.sh` 使用独立 PG16 测试事务、RTW静态Golden与拒错；真实 RTW 管理 HTTP/Outbox→真实 DC Eventing→本包独立 ODS 同父测试只能显式设置 RTW 任务私有 `SEA_RTW_REAL_WIKI_QUALITY_FIXTURE` 才运行，生产者仍需在固定源码/同进程版本核源PG Event与DC ACK。默认无运行命令或产品页面质量激活。
