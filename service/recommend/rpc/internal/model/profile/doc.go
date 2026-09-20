// Package profile 实现用户画像与时间画像（含衰减淘汰引擎）。
//
// 包职责:
//
// 该包实现 ProfileManager interface（定义于 domain 包），管理三层
// UserProfile（静态/动态/行为）与时间画像 TemporalProfile（LongTerm/
// ShortTerm/Session/Periodic/Trending/DecayState/EliminationPool）。
// 衰减与淘汰引擎：指数衰减 + Ebbinghaus 遗忘 + 强化/弱化 + 淘汰/复活，
// 每兴趣条目含 Tag/Weight/Strength S/LastReinforcedAt/DecayTau/Archived。
// 周期模式按槽位 TopK，趋势检测 24h vs 7d 基线 2σ 突增。ProfileAgent
// 异步消费事件更新画像。
//
// 核心 interface:
//   - ProfileManager: 画像管理 interface（定义于 domain 包）
//     GetProfile/UpdateProfile/GetTemporalProfile/UpdateTemporalProfile
//   - DecayEngine: 衰减引擎 interface，Decay/Reinforce/Weaken/TickElimination
//   - TrendingDetector: 趋势检测 interface
//
// 二开扩展点:
//   - 自定义衰减参数: 通过 TemporalProfileConfig 配置 DecayTau/强化系数
//   - 自定义衰减公式: 实现 DecayEngine interface 注入业务衰减模型
//   - 趋势检测策略: 实现 TrendingDetector interface 自定义基线与突增判定
package profile
