# BTW 消费 RTW 当前人工发布快照：局部验收

日期：2026-09-14。固定 RTW 提供者 `7519ecc`，BTW 从其 goctl 类型重生 `internal/clients/ridethewind/wire.gen.go/source.lock.json`。`GetCurrentSearchSnapshot(module_id)` 只向 RTW Worker 发送模块 ID；release/generation/发布版本、三路索引和有效修订都由 RTW 当前人工发布指针签发。HTTP 成功后客户端核对模块身份及三路数量，`RTWSearchSnapshotProvider.Current` 进一步拒绝非正整数/非规范十进制指针、缺失或多余 lane、错误内容寻址键及重复/空修订，再复制为 `search.Snapshot`。身份、会话和搜索/答案 ID 仍属于可信产品入口，不从此快照接口推断。

隔离组件 race 测试覆盖错模块、`0`/`07` 指针、缺 lane、伪造对象 key 和重复修订。两仓进程联验设置 `SEA_BTW_CITATION_CONSUMER_ROOT=<BTW集成工作树>` 运行 RTW `service/knowledge/scripts/acceptance.sh`：真实 RTW go-zero HTTP/PG16 从当前发布返回含 source 与 wiki 两个有效修订的三路快照，BTW生成客户端取得该快照并与RTW发布状态逐字段核对，再以**该真实快照**读取原文、提交同search_id引用、提交已验证答案并读回。首次联验发现旧手工fixture只列 source、漏了已发布wiki修订；修正fixture为完整有效集合后，完整RTW race/vet脚本及BTW子进程通过。此反例说明“候选所属有效修订子集”和“权威当前完整有效集合”不可混为一谈。

状态：H03/H07**当前快照到引用/答案子链**在隔离真实RTW进程/PG/HTTP达到`INTEGRATED`。仍使用结构性三路IndexManifest；BTW正式可信HTTP ScopeResolver/公开流、实际BGE-M3三路索引、真实用户/服务间身份、Collector→DC下钻未验。不能把这一子合同写成H07整体验收。

### 两种搜索交付的同版接口复验

在上述真实RTW实例上又让BTW的`tRPC-Agent-Go` typed `search_fast`与`read_evidence`走相同当前快照/原文/引用HTTP接口，并让一个真实Runner→LLMAgent→`search_fast` FunctionTool→LLMAgent继续执行。首次失败为ToolSession把可信operation ID与序号拼成`operation:1`作为search_id，RTW引用身份合同只允许字母、数字、下划线、点和连字符，真实POST返回400。BTW在ID生成边界改为`search_`加可信operation ID/序号的SHA256，保留同一运行内的确定性和不同序号隔离；没有放宽RTW输入校验。复验时RTW隔离PG/HTTP中，原提供方、BTW普通Delivery、直接typed Tool、Agent驱动Tool合计**4个不同search_id的唯一引用提交**，失败/重放不额外计数，答案历史仍仅接纳对应普通Delivery的一个产品turn。模型是本地固定Tool-call fixture，不是实际生产DC模型；原生Agent/Tool组件已有独立Trace验收，本次跨仓测试证明真实框架Agent调用Tool且模型看到了引用结果，但尚未由Collector回查原生Span。
