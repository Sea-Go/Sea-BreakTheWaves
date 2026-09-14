# WS08-E 内容候选池与 ItemCF 接纳边界

本包在 BTW 根模块拥有推荐内容基线，不复用旧 `recommendation/` 独立模块的未版本化池状态。进程装配使用 `app.NewRTWItemSource(ridethewind.Client)`：仅调用 RTW Worker 当前人工发布快照与其中逐个修订，核对模块、release、generation、发布指针、三路内容索引 ref、有效修订集合、item 身份/对象 hash，前后两次读取发布；只保存标题、类型、媒体、时间和内容 hash，原文不进入 item 特征。一次最多 4096 个修订，超出拒收。`FeatureVersion=rtw-item-metadata.v1` 是显式规则基线，不是 H10.c 已批准数仓特征或训练 item encoder。

部署者显式执行 `migrations/recommend/001_pools.sql`；`NewStore` 不迁移数据库。`Store.Rebuild` 将固定发布和 item 特征序列化为 SHA-256 不可变 `PoolRelease`，同一指针同一内容重试幂等；PG advisory transaction lock、单调发布版本和事务内 RTW 再核对阻止旧 worker 回写新指针。旧代保留供按 ID 审计，当前查询只读活动头。`Store.Candidates` 限制 1–100 条，按修订创建时间与 item ID 稳定排序，每项标 `new_content`，并在返回前后核对 RTW 当前发布；撤回/指针变更/RTW 不可用时不公开旧池。结果 `behavior_state=not_connected` 明确表示尚未接入成熟行为，**不是**无成熟数据、零点击或负例；空内容单独返回 `empty_content`。

`ComputeItemCF` 是**未激活的确定性数值候选**。输入契约要求 DWS generation/manifest hash、窗口水位、完整主体、请求/展示归因、`qualification=eligible`、窗口成熟和 `POSITIVE|OBSERVED_NEGATIVE`；标签可用时点及窗口终点都不得晚于固定水位。任何 PENDING、未展示、孤儿、同主体重复 impression 或超水位行整体拒收。只有当前 release 修订上的成熟 `POSITIVE` 进入 user-item 集合；成熟观测负例只作状态计数、不连边；同主体同 item 的多次合格展示只贡献一条边，每人最多 100 个正 item。两两共现按 `1/(n-1)` 限制高活跃用户贡献，再除以 `sqrt(freq(i)*freq(j))`，每 item TopK≤100。`no_mature_rows`、`mature_no_positive` 与 `positive` 独立表示。当前没有可信 WS07 DWS/CH source adapter 与发布准入，所以这个计算结果不写入活动候选、不能宣称有真实 ItemCF 召回或效果。将来适配器必须从 DWS generation 的实际完成/可用收据映射 `AvailableAt`，不得复用请求特征的 `feature_available_at`。

ItemCF 候选工件 ID 同时绑定 TopK 与固定 DWS 水位；同一邻居数值不能跨不同 cutoff 复用工件标识。取消在读取 DWS 前及遍历用户共现时生效。

WS08-D `ServingBundle`/PairRef 和 item embedding index、H11.b 配对生效属于 WS08-F；本包的三路搜索索引仅作为同代内容签名，不冒充推荐 item 向量空间。WS07-C 需要交付真实成熟 DWS/候选统计、完整水位和冻结 manifest；之后才能接入热门池、ItemCF 耐久邻居、配对 item 特征、H09.c 阶段事实与用户请求消费。

验收见 [ACCEPTANCE.md](ACCEPTANCE.md)。
