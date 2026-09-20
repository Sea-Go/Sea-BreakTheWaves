// Package rank 实现传统推荐快路径的排序能力。
//
// 包职责:
//
// 该包实现 Ranker interface（定义于 domain 包），提供传统推荐快路径的
// 排序器：WeightedRanker（加权）、LRRanker（逻辑回归）、GBDTRanker
// （XGBoost），并配合 Fallback 兜底策略。排序零 LLM token，是 fast 路径
// 与 hybrid 路径的基础。特征工程可扩展，支持业务方注入自定义特征。
//
// 核心 interface:
//   - Ranker: 排序 interface（定义于 domain 包），Rank(ctx, RankContext)
//   - Registry: 排序器注册表，按名称注册与获取
//
// 二开扩展点:
//   - 注册新 Ranker: 实现 Ranker interface 并通过 Register(name, r) 注入
//   - 业务规则注入: 实现 RuleRanker 注入活动期间等业务规则
//   - 特征工程: 通过 RankContext 扩展特征字段
package rank
