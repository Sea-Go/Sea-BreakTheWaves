package domain

import "context"

// ============================================================================
// 该文件集中定义推荐系统的 14 个核心 interface（中央契约）。
// 所有 interface 仅依赖标准库与 domain 包内类型，确保离线可编译。
// 每个 interface 含中文注释说明职责与二开扩展点。
// 实现分布于 internal/{recall,rank,rerank,graph,search,profile,channel,
// skill,agent,event,obs,...} 等包，通过依赖注入装配。
// ============================================================================

// Recaller 召回 interface，传统推荐快路径核心契约。
//
// 职责：从多数据源（规则/内容/CF/图谱/频道）召回候选文章，零 LLM token。
// 实现：RuleRecaller/ContentRecaller/CFRecaller/GraphRecaller/ChannelRecaller/
// HybridRecaller（位于 internal/recall 与 internal/cf）。
//
// 二开扩展点：实现该 interface 并通过 recall.Register(name, r) 注入，
// OrchestratorAgent 即可在召回规划中调用该召回器，无需修改核心代码。
type Recaller interface {
	// Recall 执行召回。
	// ctx 上下文；req 召回请求（用户/画像/频道/标签/TopK/意图）。
	// 返回召回结果与 error。
	Recall(ctx context.Context, req RecallRequest) (RecallResult, error)
	// Name 返回召回器名称（用于注册与规划）。
	Name() string
}

// Ranker 排序 interface，传统推荐快路径核心契约。
//
// 职责：对召回候选按特征与模型排序，零 LLM token。
// 实现：WeightedRanker/LRRanker/GBDTRanker（位于 internal/rank）。
//
// 二开扩展点：实现该 interface 并通过 rank.Register(name, r) 注入，
// 支持业务方注入业务规则（如"活动期间提升活动文章权重"）。
type Ranker interface {
	// Rank 执行排序。
	// ctx 上下文；ctx2 排序上下文（画像/时间画像/频道/意图/候选）。
	// 返回排序结果与 error。
	Rank(ctx context.Context, rankCtx RankContext) (RankResult, error)
	// Name 返回排序器名称。
	Name() string
}

// Reranker 重排 interface，自研/外部 rerank 统一契约。
//
// 职责：对候选精排，支持自研（Cross-encoder/Two-tower/LambdaMART）与
// 外部（DashScope）rerank，A/B 分桶切换。
// 实现：SelfReranker/ExternalReranker/LLMReranker（位于 internal/rerank）。
//
// 二开扩展点：实现该 interface 并通过 rerank.Register 注入；
// 或通过 RecommendConfig.RerankModel 切换 self/external 模型版本。
type Reranker interface {
	// Rerank 执行重排。
	// ctx 上下文；req 重排请求（用户/候选/查询/模型/TopK）。
	// 返回重排结果与 error。
	Rerank(ctx context.Context, req RerankRequest) (RerankResult, error)
	// Name 返回重排器名称。
	Name() string
}

// QualityJudger 文章质量评判 interface，6 维评分 + Best-of-N。
//
// 职责：对文章做多维质量评分（Authority/Depth/Freshness/Completeness/
// Readability/Citation），配合 Best-of-N 与 LLM Verifier 选择最高质量。
// 实现：JudgeAgent（位于 internal/quality）。
//
// 二开扩展点：实现该 interface 接入自研评判模型；
// 或通过 Rubric interface 自定义评判标准；
// 或通过 RecommendConfig.QualityModel 切换模型版本。
type QualityJudger interface {
	// Judge 评判文章质量。
	// ctx 上下文；req 质量请求（文章 ID/内容/rubric）。
	// 返回 6 维质量评分与 error。
	Judge(ctx context.Context, req QualityRequest) (ArticleQuality, error)
}

// GraphQuerier 知识图谱查询 interface，Cypher/召回/实体链接/图片搜索。
//
// 职责：基于 Neo4j 大图谱（12 节点类型）提供查询能力，供图谱召回、
// LLM 图谱查询、搜索知识、图片关联使用。
// 实现：Client（位于 internal/graph，基于 neo4j-go-driver/v5）。
//
// 二开扩展点：实现该 interface 注入自定义图谱后端；
// 或在 internal/graph/cypher.go 新增 Cypher 模板（参数化防注入）。
type GraphQuerier interface {
	// QueryByCypher 执行 Cypher 查询。
	// ctx 上下文；cypher Cypher 语句；params 参数（参数化防注入）。
	// 返回节点列表与 error。
	QueryByCypher(ctx context.Context, cypher string, params map[string]any) ([]GraphNode, error)
	// RecallByGraph 图谱多跳召回。
	// ctx 上下文；req 图谱召回请求（用户/跳数/TopK/模式）。
	// 返回候选列表与 error。
	RecallByGraph(ctx context.Context, req GraphRecallRequest) ([]Candidate, error)
	// EntityLink 实体链接。
	// ctx 上下文；query 查询文本。
	// 返回识别实体列表与 error。
	EntityLink(ctx context.Context, query string) ([]Entity, error)
	// ImageSearch 以图搜文（图片节点 → 视觉相似 → 文章）。
	// ctx 上下文；imageURL 图片 URL。
	// 返回候选文章列表与 error。
	ImageSearch(ctx context.Context, imageURL string) ([]Candidate, error)
}

// Searcher 搜索 interface，语义混合检索统一契约。
//
// 职责：提供完整搜索增强栈（语义/混合/查询理解/重写/个性化/过滤/补全/
// 历史/图谱知识/图片）。
// 实现：HybridSearcher（位于 internal/search）。
//
// 二开扩展点：实现该 interface 并通过 search.Register 注入；
// 或实现 QueryUnderstander/QueryRewriter interface 扩展查询理解与重写。
type Searcher interface {
	// Search 执行搜索。
	// ctx 上下文；query 搜索查询（查询/用户/过滤/TopK/意图）。
	// 返回搜索结果与 error。
	Search(ctx context.Context, query SearchQuery) (SearchResult, error)
}

// ProfileManager 用户画像管理 interface，三层画像 + 时间画像。
//
// 职责：管理 UserProfile（静态/动态/行为）与 TemporalProfile（长期/短期/
// 会话/周期/趋势/衰减/淘汰），含衰减与淘汰引擎。
// 实现：Manager（位于 internal/profile）。
//
// 二开扩展点：实现该 interface 自定义画像存储与更新逻辑；
// 或通过 TemporalProfileConfig 配置衰减参数；
// 或实现 DecayEngine interface 注入自定义衰减公式。
type ProfileManager interface {
	// GetProfile 获取用户画像。
	// ctx 上下文；key 用户标识。
	// 返回用户画像与 error。
	GetProfile(ctx context.Context, key UserKey) (UserProfile, error)
	// UpdateProfile 更新用户画像。
	// ctx 上下文；key 用户标识；profile 待更新画像。
	// 返回 error。
	UpdateProfile(ctx context.Context, key UserKey, profile UserProfile) error
	// GetTemporalProfile 获取时间画像。
	// ctx 上下文；key 用户标识。
	// 返回时间画像与 error。
	GetTemporalProfile(ctx context.Context, key UserKey) (TemporalProfile, error)
	// UpdateTemporalProfile 更新时间画像。
	// ctx 上下文；key 用户标识；profile 待更新时间画像。
	// 返回 error。
	UpdateTemporalProfile(ctx context.Context, key UserKey, profile TemporalProfile) error
}

// ChannelManager IP 频道管理 interface，频道注册/独立池/路由。
//
// 职责：管理 IP 频道注册体系，每频道绑定 type_tag + secondary_tags + 独立
// 召回池 + 独立画像 filterKey + 独立质量阈值 + 独立 rerank 模型版本。
// 实现：Registry（位于 internal/channel）。
//
// 二开扩展点：通过 RegisterChannel 注册自定义 IP 频道，无需改核心代码；
// 或实现 Pool interface 自定义频道召回池维护逻辑。
type ChannelManager interface {
	// ListChannels 列出所有频道。
	// ctx 上下文。
	// 返回频道列表与 error。
	ListChannels(ctx context.Context) ([]Channel, error)
	// GetChannel 获取单个频道。
	// ctx 上下文；name 频道名。
	// 返回频道与 error。
	GetChannel(ctx context.Context, name string) (Channel, error)
	// RegisterChannel 注册频道。
	// ctx 上下文；channel 频道配置。
	// 返回 error。
	RegisterChannel(ctx context.Context, channel Channel) error
	// EnsurePool 确保频道独立召回池存在并维护水位。
	// ctx 上下文；name 频道名。
	// 返回 error。
	EnsurePool(ctx context.Context, name string) error
	// Route 频道路由。
	// ctx 上下文；req 路由请求（用户/频道/意图）。
	// 返回路由结果与 error。
	Route(ctx context.Context, req ChannelRouteRequest) (ChannelRouteResult, error)
}

// FeedbackConsumer 反馈消费 interface，行为事件闭环。
//
// 职责：消费行为事件，触发画像/质量/CF/rerank 反馈闭环。
// 实现：FeedbackAgent（位于 internal/agent，Function Agent）。
//
// 二开扩展点：实现该 interface 自定义反馈处理逻辑；
// 或通过 HookRegistry.Register 注册 Hook 消费事件。
type FeedbackConsumer interface {
	// Consume 消费行为事件。
	// ctx 上下文；event 行为事件。
	// 返回 error。
	Consume(ctx context.Context, event BehaviorEvent) error
}

// SkillRegistry Skill 注册体系 interface，50+ Skill 声明式二开。
//
// 职责：管理 Skill 清单（12 类 50+ Skill），遵循 Anthropic Agent Skills
// 规范，加载 SKILL.md + 工具 + 资源，暴露给 Agent 调用。
// 实现：Registry（位于 internal/skill）。
//
// 二开扩展点：业务方放置技能目录（SKILL.md + 工具 + 资源），
// 框架自动加载，无需改核心代码；或通过 Register 注入自定义实现。
type SkillRegistry interface {
	// Register 注册 Skill。
	// skill Skill 描述。
	// 返回 error。
	Register(skill Skill) error
	// Get 获取 Skill。
	// name Skill 名称。
	// 返回 Skill 与 error。
	Get(name string) (Skill, error)
	// List 列出所有 Skill。
	// 返回 Skill 列表。
	List() []Skill
	// Invoke 调用 Skill。
	// ctx 上下文；name Skill 名称；input 调用输入。
	// 返回调用输出与 error。
	Invoke(ctx context.Context, name string, input SkillInput) (SkillOutput, error)
}

// AgentFactory Agent 工厂 interface，12 个 Agent 统一构建。
//
// 职责：统一构建 12 个 Agent（Orchestrator/Intent/RecallPlanner/Graph/
// Rerank/Quality/Explain/Search/Profile/Channel/Feedback/Eval），
// 封装模型分级（小/大模型）与 Prompt Cache 配置。
// 实现：Factory（位于 internal/agent）。
//
// 二开扩展点：实现该 interface 自定义 Agent 构建（如替换模型/工具/Skill）；
// 或通过 AgentOptions 配置模型分级与 A/B 分桶。
type AgentFactory interface {
	// NewOrchestratorAgent 创建编排 Agent（GraphAgent），路由决策 + 编排 + 融合。
	NewOrchestratorAgent(opts AgentOptions) Agent
	// NewIntentAgent 创建意图理解 Agent（小模型，结构化输出）。
	NewIntentAgent(opts AgentOptions) Agent
	// NewRecallPlannerAgent 创建召回规划 Agent（小模型，决定召回源组合）。
	NewRecallPlannerAgent(opts AgentOptions) Agent
	// NewGraphAgent 创建图谱推理 Agent（大模型，Cypher 生成 + 图谱推理）。
	NewGraphAgent(opts AgentOptions) Agent
	// NewRerankAgent 创建重排 Agent（大模型 + 自研工具）。
	NewRerankAgent(opts AgentOptions) Agent
	// NewQualityAgent 创建质量评判 Agent（Best-of-N + LLM Verifier）。
	NewQualityAgent(opts AgentOptions) Agent
	// NewExplainAgent 创建解释生成 Agent（大模型）。
	NewExplainAgent(opts AgentOptions) Agent
	// NewSearchAgent 创建搜索 Agent（GraphAgent 子图，慢路径搜索）。
	NewSearchAgent(opts AgentOptions) Agent
	// NewProfileAgent 创建画像更新 Agent（小模型 + Memory，异步）。
	NewProfileAgent(opts AgentOptions) Agent
	// NewChannelAgent 创建频道路由 Agent（小模型，IP 频道路由）。
	NewChannelAgent(opts AgentOptions) Agent
	// NewFeedbackAgent 创建反馈闭环 Agent（Function，无 LLM）。
	NewFeedbackAgent(opts AgentOptions) Agent
	// NewEvalAgent 创建离线评估 Agent（Evaluation）。
	NewEvalAgent(opts AgentOptions) Agent
}

// Orchestrator 编排 interface，双路径路由与融合。
//
// 职责：基于意图复杂度 + RecommendConfig.PathMode + 用户画像决策路径
// （fast/slow/hybrid），编排多 Agent 协作，融合快慢路径候选。
// 实现：OrchestratorAgent（位于 internal/agent，GraphAgent）。
//
// 二开扩展点：实现该 interface 自定义编排逻辑；
// 或通过 RecommendConfig.PathMode 配置路径策略。
type Orchestrator interface {
	// Recommend 执行推荐主流程。
	// ctx 上下文；req 推荐请求（用户/频道/标签/配置/路径模式/TopK/解释开关）。
	// 返回推荐响应与 error。
	Recommend(ctx context.Context, req RecommendRequest) (RecommendResponse, error)
}

// EventEmitter 事件发射 interface，行为事件统一出口。
//
// 职责：统一发射行为事件，供 HookRegistry 顺序调用 Hook 消费。
// 实现：Emitter（位于 internal/event）。
//
// 二开扩展点：实现该 interface 自定义事件出口（如接入 Kafka/ES）；
// 或通过 HookRegistry.Register 注册 Hook 消费事件。
type EventEmitter interface {
	// Emit 发射行为事件。
	// ctx 上下文；event 行为事件。
	// 返回 error。
	Emit(ctx context.Context, event BehaviorEvent) error
}

// HookRegistry Hook 注册 interface，二开扩展点核心入口。
//
// 职责：管理 Hook 注册表，行为事件触发时顺序调用 Hook，错误隔离不影响
// 主流程与其他 Hook。
// 实现：Registry（位于 internal/event）。
//
// 二开扩展点：实现 Hook interface 并通过 Register 注入，
// 错误隔离不影响主流程；内置 Hook：Log/Metrics/Eval/OnlineFeedback/
// QualityFeedback/CFFeedback/RerankFeedback/GraphFeedback。
type HookRegistry interface {
	// Register 注册 Hook。
	// hook Hook 实现。
	// 返回 error。
	Register(hook Hook) error
	// Invoke 顺序调用所有 Hook 处理事件（错误隔离）。
	// ctx 上下文；event 行为事件。
	// 返回 error（聚合错误，不影响主流程）。
	Invoke(ctx context.Context, event BehaviorEvent) error
}
