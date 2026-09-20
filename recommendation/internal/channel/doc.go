// Package channel 实现 IP 频道管理与独立召回池。
//
// 包职责:
//
// 该包实现 ChannelManager interface（定义于 domain 包），提供 IP 频道注册
// 体系：业务方通过 Registry 注册自定义 IP 频道，绑定 type_tag + secondary_tags
// + 独立召回池 + 独立画像 filterKey + 独立质量阈值 + 独立 rerank 模型版本。
// 支持频道列表、获取、注册、独立池维护（EnsurePool）、频道路由（Route）。
// 无需改核心代码即可开通"旅游 IP"等独立推荐流。
//
// 核心 interface:
//   - ChannelManager: 频道管理 interface（定义于 domain 包）
//     ListChannels/GetChannel/RegisterChannel/EnsurePool/Route
//   - Registry: 频道注册表
//   - Pool: 频道独立召回池
//
// 二开扩展点:
//   - 注册新频道: 实现 Channel 配置并通过 RegisterChannel 注入，无需改核心代码
//   - 独立池策略: 实现 Pool interface 自定义频道召回池维护逻辑
//   - 路由策略: 实现 Route 方法自定义频道路由规则
package channel
