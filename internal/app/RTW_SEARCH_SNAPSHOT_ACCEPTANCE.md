# BTW 消费 RTW 当前人工发布快照：局部验收

日期：2026-09-14。固定 RTW 提供者 `7519ecc`，BTW 从其 goctl 类型重生 `internal/clients/ridethewind/wire.gen.go/source.lock.json`。`GetCurrentSearchSnapshot(module_id)` 只向 RTW Worker 发送模块 ID；release/generation/发布版本、三路索引和有效修订都由 RTW 当前人工发布指针签发。HTTP 成功后客户端核对模块身份及三路数量，`RTWSearchSnapshotProvider.Current` 进一步拒绝非正整数/非规范十进制指针、缺失或多余 lane、错误内容寻址键及重复/空修订，再复制为 `search.Snapshot`。身份、会话和搜索/答案 ID 仍属于可信产品入口，不从此快照接口推断。

隔离组件 race 测试覆盖错模块、`0`/`07` 指针、缺 lane、伪造对象 key 和重复修订。两仓进程联验设置 `SEA_BTW_CITATION_CONSUMER_ROOT=<BTW集成工作树>` 运行 RTW `service/knowledge/scripts/acceptance.sh`：真实 RTW go-zero HTTP/PG16 从当前发布返回含 source 与 wiki 两个有效修订的三路快照，BTW生成客户端取得该快照并与RTW发布状态逐字段核对，再以**该真实快照**读取原文、提交同search_id引用、提交已验证答案并读回。首次联验发现旧手工fixture只列 source、漏了已发布wiki修订；修正fixture为完整有效集合后，完整RTW race/vet脚本及BTW子进程通过。此反例说明“候选所属有效修订子集”和“权威当前完整有效集合”不可混为一谈。

状态：H03/H07**当前快照到引用/答案子链**在隔离真实RTW进程/PG/HTTP达到`INTEGRATED`。仍使用结构性三路IndexManifest；BTW正式可信HTTP ScopeResolver/公开流、实际BGE-M3三路索引、真实用户/服务间身份、Collector→DC下钻未验。不能把这一子合同写成H07整体验收。
