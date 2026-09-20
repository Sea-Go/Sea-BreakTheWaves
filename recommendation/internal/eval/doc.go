// Package eval 实现完整验收体系：离线评估 + A/B + 回归 + 漂移 + 标注闭环。
//
// 包职责:
//
// 该包基于 trpc-agent-go Evaluation 模块构建 EvalAgent，提供完整验收闭环：
// 离线评估集（每频道/IP/意图至少 50 条 golden cases）、评估指标（确定性
// Recall@K/NDCG@K/CTR/CVR + LLM Judge + rubric）、回归测试（版本迭代前
// 自动运行对比基线）、行为漂移检测（在线指标分布对比历史基线）、人工标注
// 闭环（管理员标注 → 入评估集 → 校准 LLM Judge rubrics）、在线 A/B
// （RecommendConfig 分桶 + CTR/CVR/延迟/成本对比）、双路径验收（对比
// fast/slow/hybrid 质量与成本）。
//
// 核心 interface:
//   - EvalAgent: 评估 Agent（基于 trpc-agent-go Evaluation）
//   - CaseManager: 评估集管理 interface，CRUD
//   - Metric: 评估指标 interface（确定性 + LLM Judge + rubric）
//   - DriftDetector: 行为漂移检测 interface
//
// 二开扩展点:
//   - 新增评估指标: 实现 Metric interface 注册自定义指标
//   - 自定义 rubric: 实现 Rubric interface 注入业务评判标准
//   - 漂移检测策略: 实现 DriftDetector interface 自定义基线与判定阈值
package eval
