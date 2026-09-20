// Package event 实现统一行为事件与二开 Hook 体系。
//
// 包职责:
//
// 该包定义强类型 BehaviorEvent（事件类型含 fast_path_hit/slow_path_hit/
// hybrid_merge/graph_query/cypher_generated/skill_invoked 等）与 Hook
// interface（OnEvent + 扩展 OnRecommend/OnToolCall/OnLLMCall/OnQualityJudge/
// OnSearch/OnRerank/OnGraphQuery/OnSkillInvoke）。Registry 顺序调用 Hook
// 并做错误隔离。内置 Hook：Log/Metrics/Eval/OnlineFeedback/QualityFeedback/
// CFFeedback/RerankFeedback/GraphFeedback。通过 trpc-agent-go Callbacks
// 接入 Agent 循环。
//
// 核心 interface:
//   - Hook: 行为事件 Hook interface，OnEvent(ctx, BehaviorEvent) + Name()
//   - HookRegistry: Hook 注册表（定义于 domain 包），Register/Invoke
//   - EventEmitter: 事件发射 interface（定义于 domain 包），Emit
//
// 二开扩展点:
//   - 注册 Hook: 实现 Hook interface 并通过 HookRegistry.Register 注入
//   - 错误隔离: Hook 错误不影响主流程与其他 Hook
//   - 反馈闭环: 通过 FeedbackHook 写入画像/质量/CF/rerank 反馈
package event
