// Package recall 实现传统推荐快路径的召回能力。
//
// 包职责:
//
// 该包实现 Recaller interface（定义于 domain 包），提供传统推荐快路径的
// 召回源：RuleRecaller（热点/最新/编辑）、ContentRecaller（向量 + BM25）、
// CFRecaller（User-CF/Item-CF/MF）、GraphRecaller（Neo4j 多跳）、
// ChannelRecaller（频道独立池）、HybridRecaller（并行多源融合去重）。
// 召回零 LLM token，P99 < 100ms，是 fast 路径与 hybrid 路径的基础。
//
// 核心 interface:
//   - Recaller: 召回 interface（定义于 domain 包），Recall(ctx, RecallRequest)
//   - Registry: 召回源注册表，按名称注册与获取
//
// 二开扩展点:
//   - 注册新 Recaller: 实现 Recaller interface 并通过 Register(name, r) 注入
//   - 业务规则注入: 实现 RuleRecaller 注入活动期间等业务规则
//   - 混合召回编排: 通过 HybridRecaller 配置召回源组合与权重
package recall
