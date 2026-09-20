// Package agent 实现 12 个多 Agent 协作架构，全部基于 trpc-agent-go。
//
// 包职责:
//
// 该包基于 trpc-agent-go 的 GraphAgent/LLMAgent/Runner/Best-of-N/Evaluation
// 等能力构建推荐系统的 12 个 Agent：OrchestratorAgent（编排）、IntentAgent
// （意图理解）、RecallPlannerAgent（召回规划）、GraphAgent（图谱推理）、
// RerankAgent（重排）、QualityAgent（质量评判）、ExplainAgent（解释生成）、
// SearchAgent（搜索子图）、ProfileAgent（画像更新）、ChannelAgent（频道路由）、
// FeedbackAgent（反馈闭环）、EvalAgent（离线评估）。AgentFactory interface
// 统一构建入口，OrchestratorAgent 编排双路径（fast/slow/hybrid）。
//
// 核心 interface:
//   - AgentFactory: Agent 工厂 interface（定义于 domain 包），12 个工厂方法
//   - Agent: 可执行 Agent 契约（定义于 domain 包），Run + Name
//
// 二开扩展点:
//   - 自定义 Agent: 实现该 interface 并通过 AgentFactory 注册即可扩展
//   - 替换 Agent: 通过 AgentFactory 注入自定义实现（如自研 RerankAgent）
//   - 模型分级: 通过 AgentFactory 的模型配置切换小/大模型，支持 A/B 分桶
package agent
