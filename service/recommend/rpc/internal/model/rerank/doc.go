// Package rerank 实现自研重排能力，替代完全依赖外部模型。
//
// 包职责:
//
// 该包实现 Reranker interface（定义于 domain 包），提供自研 rerank：
// Cross-encoder（BERT-base 中文 + linear head）、Two-tower（双塔召回）、
// LambdaMART（XGBoost 终排），并保留外部 rerank（DashScope）作为降级。
// 支持独立 inference 服务、模型热加载、版本化与 A/B 分桶，接入 RerankAgent。
// 训练流水线（每日全量 + 每小时增量）位于 internal/rerank/training/。
//
// 核心 interface:
//   - Reranker: 重排 interface（定义于 domain 包），Rerank(ctx, RerankRequest)
//   - Registry: rerank 注册表，按名称注册与获取
//
// 二开扩展点:
//   - 新增 Reranker: 实现 Reranker interface 并通过 Register 注入
//   - 模型二开: 通过 RecommendConfig.RerankModel 切换 self/external 模型版本
//   - A/B 测试: 通过 RecommendConfig 分桶对比自研/外部 rerank
package rerank
