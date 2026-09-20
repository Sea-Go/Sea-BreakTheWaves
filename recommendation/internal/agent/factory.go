// Package agent factory.go — AgentFactory 实现。
//
// 该文件实现 domain.AgentFactory interface，统一构建 12 个 Agent。
// Factory 持有各 Agent 的构造函数变量（AgentConstructor），支持延迟绑定：
// 本 sub-agent 实现 IntentAgent 与 RecallPlannerAgent 的构造函数，
// 其他 9 个 Agent 由别的 sub-agent 实现，未注入时调用会 panic 提示。
// NewOrchestratorAgent 暂时返回 nil（Phase 12 实现）。
//
// 二开扩展点：
//   - 通过 WithXxxConstructor 替换任意 Agent 的构造函数
//   - 通过 Configure 应用多个 FactoryOption
package agent

import (
	"sea/internal/domain"
)

// AgentConstructor Agent 构造函数类型，由各 Agent 文件提供。
// 统一签名为 (llm, tools, opts) -> domain.Agent，避免 Factory 引用具体类型导致循环依赖。
type AgentConstructor func(llm LLMClient, tools ToolExecutor, opts domain.AgentOptions) domain.Agent

// FactoryOption Factory 配置选项函数。
type FactoryOption func(*Factory)

// Factory Agent 工厂实现，持有 LLM/工具/记忆与各 Agent 构造函数。
type Factory struct {
	llm    LLMClient
	tools  ToolExecutor
	memory MemoryStore

	// 各 Agent 构造函数（默认用各包内 NewXxxAgent，可被 WithXxx 覆盖用于二开）。
	intentCtor        AgentConstructor
	recallPlannerCtor AgentConstructor
	graphCtor         AgentConstructor
	rerankCtor        AgentConstructor
	qualityCtor       AgentConstructor
	explainCtor       AgentConstructor
	searchCtor        AgentConstructor
	profileCtor       AgentConstructor
	channelCtor       AgentConstructor
	feedbackCtor      AgentConstructor
	evalCtor          AgentConstructor
	orchestratorCtor  AgentConstructor
}

// NewFactory 创建 Agent 工厂，注入 LLM/工具/记忆，并默认绑定本包提供的构造函数。
// IntentAgent 与 RecallPlannerAgent 的构造函数由本包提供；
// 其他 9 个 Agent 构造函数初始为 nil，需通过 WithXxxConstructor 注入或由其他 sub-agent 注册。
func NewFactory(llm LLMClient, tools ToolExecutor, memory MemoryStore) *Factory {
	f := &Factory{
		llm:    llm,
		tools:  tools,
		memory: memory,
	}
	// 默认绑定本 sub-agent 实现的构造函数。
	f.intentCtor = NewIntentAgent
	f.recallPlannerCtor = NewRecallPlannerAgent
	return f
}

// Configure 应用一个或多个 FactoryOption，用于二开替换 Agent 构造函数。
func (f *Factory) Configure(opts ...FactoryOption) {
	for _, opt := range opts {
		opt(f)
	}
}

// WithIntentConstructor 替换 IntentAgent 构造函数。
func WithIntentConstructor(ctor AgentConstructor) FactoryOption {
	return func(f *Factory) { f.intentCtor = ctor }
}

// WithRecallPlannerConstructor 替换 RecallPlannerAgent 构造函数。
func WithRecallPlannerConstructor(ctor AgentConstructor) FactoryOption {
	return func(f *Factory) { f.recallPlannerCtor = ctor }
}

// WithGraphConstructor 替换 GraphAgent 构造函数。
func WithGraphConstructor(ctor AgentConstructor) FactoryOption {
	return func(f *Factory) { f.graphCtor = ctor }
}

// WithRerankConstructor 替换 RerankAgent 构造函数。
func WithRerankConstructor(ctor AgentConstructor) FactoryOption {
	return func(f *Factory) { f.rerankCtor = ctor }
}

// WithQualityConstructor 替换 QualityAgent 构造函数。
func WithQualityConstructor(ctor AgentConstructor) FactoryOption {
	return func(f *Factory) { f.qualityCtor = ctor }
}

// WithExplainConstructor 替换 ExplainAgent 构造函数。
func WithExplainConstructor(ctor AgentConstructor) FactoryOption {
	return func(f *Factory) { f.explainCtor = ctor }
}

// WithSearchConstructor 替换 SearchAgent 构造函数。
func WithSearchConstructor(ctor AgentConstructor) FactoryOption {
	return func(f *Factory) { f.searchCtor = ctor }
}

// WithProfileConstructor 替换 ProfileAgent 构造函数。
func WithProfileConstructor(ctor AgentConstructor) FactoryOption {
	return func(f *Factory) { f.profileCtor = ctor }
}

// WithChannelConstructor 替换 ChannelAgent 构造函数。
func WithChannelConstructor(ctor AgentConstructor) FactoryOption {
	return func(f *Factory) { f.channelCtor = ctor }
}

// WithFeedbackConstructor 替换 FeedbackAgent 构造函数。
func WithFeedbackConstructor(ctor AgentConstructor) FactoryOption {
	return func(f *Factory) { f.feedbackCtor = ctor }
}

// WithEvalConstructor 替换 EvalAgent 构造函数。
func WithEvalConstructor(ctor AgentConstructor) FactoryOption {
	return func(f *Factory) { f.evalCtor = ctor }
}

// WithOrchestratorConstructor 替换 OrchestratorAgent 构造函数。
func WithOrchestratorConstructor(ctor AgentConstructor) FactoryOption {
	return func(f *Factory) { f.orchestratorCtor = ctor }
}

// 编译期断言：Factory 实现 domain.AgentFactory interface。
var _ domain.AgentFactory = (*Factory)(nil)

// NewOrchestratorAgent 创建编排 Agent。
// 暂时返回 nil，Phase 12 实现 OrchestratorAgent（双路径路由与融合）。
func (f *Factory) NewOrchestratorAgent(_ domain.AgentOptions) domain.Agent {
	// Phase 12 实现，当前返回 nil。
	return nil
}

// NewIntentAgent 创建意图理解 Agent（小模型，结构化输出）。
func (f *Factory) NewIntentAgent(opts domain.AgentOptions) domain.Agent {
	return f.intentCtor(f.llm, f.tools, opts)
}

// NewRecallPlannerAgent 创建召回规划 Agent（小模型，决定召回源组合）。
func (f *Factory) NewRecallPlannerAgent(opts domain.AgentOptions) domain.Agent {
	return f.recallPlannerCtor(f.llm, f.tools, opts)
}

// NewGraphAgent 创建图谱推理 Agent（大模型，Cypher 生成 + 图谱推理）。
// 该 Agent 尚未由本 sub-agent 实现，需通过 WithGraphConstructor 注入构造函数。
func (f *Factory) NewGraphAgent(opts domain.AgentOptions) domain.Agent {
	if f.graphCtor == nil {
		panic("agent.Factory: GraphAgent 构造函数未注入，该 Agent 尚未实现")
	}
	return f.graphCtor(f.llm, f.tools, opts)
}

// NewRerankAgent 创建重排 Agent（大模型 + 自研工具）。
// 该 Agent 尚未由本 sub-agent 实现，需通过 WithRerankConstructor 注入构造函数。
func (f *Factory) NewRerankAgent(opts domain.AgentOptions) domain.Agent {
	if f.rerankCtor == nil {
		panic("agent.Factory: RerankAgent 构造函数未注入，该 Agent 尚未实现")
	}
	return f.rerankCtor(f.llm, f.tools, opts)
}

// NewQualityAgent 创建质量评判 Agent（Best-of-N + LLM Verifier）。
// 该 Agent 尚未由本 sub-agent 实现，需通过 WithQualityConstructor 注入构造函数。
func (f *Factory) NewQualityAgent(opts domain.AgentOptions) domain.Agent {
	if f.qualityCtor == nil {
		panic("agent.Factory: QualityAgent 构造函数未注入，该 Agent 尚未实现")
	}
	return f.qualityCtor(f.llm, f.tools, opts)
}

// NewExplainAgent 创建解释生成 Agent（大模型）。
// 该 Agent 尚未由本 sub-agent 实现，需通过 WithExplainConstructor 注入构造函数。
func (f *Factory) NewExplainAgent(opts domain.AgentOptions) domain.Agent {
	if f.explainCtor == nil {
		panic("agent.Factory: ExplainAgent 构造函数未注入，该 Agent 尚未实现")
	}
	return f.explainCtor(f.llm, f.tools, opts)
}

// NewSearchAgent 创建搜索 Agent（GraphAgent 子图，慢路径搜索）。
// 该 Agent 尚未由本 sub-agent 实现，需通过 WithSearchConstructor 注入构造函数。
func (f *Factory) NewSearchAgent(opts domain.AgentOptions) domain.Agent {
	if f.searchCtor == nil {
		panic("agent.Factory: SearchAgent 构造函数未注入，该 Agent 尚未实现")
	}
	return f.searchCtor(f.llm, f.tools, opts)
}

// NewProfileAgent 创建画像更新 Agent（小模型 + Memory，异步）。
// 该 Agent 尚未由本 sub-agent 实现，需通过 WithProfileConstructor 注入构造函数。
func (f *Factory) NewProfileAgent(opts domain.AgentOptions) domain.Agent {
	if f.profileCtor == nil {
		panic("agent.Factory: ProfileAgent 构造函数未注入，该 Agent 尚未实现")
	}
	return f.profileCtor(f.llm, f.tools, opts)
}

// NewChannelAgent 创建频道路由 Agent（小模型，IP 频道路由）。
// 该 Agent 尚未由本 sub-agent 实现，需通过 WithChannelConstructor 注入构造函数。
func (f *Factory) NewChannelAgent(opts domain.AgentOptions) domain.Agent {
	if f.channelCtor == nil {
		panic("agent.Factory: ChannelAgent 构造函数未注入，该 Agent 尚未实现")
	}
	return f.channelCtor(f.llm, f.tools, opts)
}

// NewFeedbackAgent 创建反馈闭环 Agent（Function，无 LLM）。
// 该 Agent 尚未由本 sub-agent 实现，需通过 WithFeedbackConstructor 注入构造函数。
func (f *Factory) NewFeedbackAgent(opts domain.AgentOptions) domain.Agent {
	if f.feedbackCtor == nil {
		panic("agent.Factory: FeedbackAgent 构造函数未注入，该 Agent 尚未实现")
	}
	return f.feedbackCtor(f.llm, f.tools, opts)
}

// NewEvalAgent 创建离线评估 Agent（Evaluation）。
// 该 Agent 尚未由本 sub-agent 实现，需通过 WithEvalConstructor 注入构造函数。
func (f *Factory) NewEvalAgent(opts domain.AgentOptions) domain.Agent {
	if f.evalCtor == nil {
		panic("agent.Factory: EvalAgent 构造函数未注入，该 Agent 尚未实现")
	}
	return f.evalCtor(f.llm, f.tools, opts)
}
