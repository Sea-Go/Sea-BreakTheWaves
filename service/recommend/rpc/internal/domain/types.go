package domain

import (
	"context"
	"time"
)

// ============================================================================
// 该文件定义推荐系统核心领域模型与支撑类型。
// 所有类型仅依赖标准库，确保离线可编译。
// 字段语义参考 spec.md 双路径架构、12 多 Agent、知识图谱、4 类二开点。
// ============================================================================

// ----------------------------------------------------------------------------
// 用户与画像
// ----------------------------------------------------------------------------

// UserKey 用户唯一标识，跨数据源统一。
type UserKey struct {
	// UserID 用户 ID。
	UserID string
	// Channel 用户当前所在频道（可为空表示默认流）。
	Channel string
}

// UserProfile 三层用户画像：静态 / 动态 / 行为。
// 二开：实现 ProfileManager interface 自定义画像存储与更新逻辑。
type UserProfile struct {
	// Key 用户唯一标识（含频道隔离），三层画像的归属主键。
	Key UserKey
	// Static 静态画像（注册信息/人口统计等缓慢变化属性）。
	Static *StaticProfile
	// Dynamic 动态画像（短期兴趣/会话状态）。
	Dynamic *DynamicProfile
	// Behavior 行为画像（点击/点赞/收藏/完成阅读等历史聚合）。
	Behavior *BehaviorProfile
	// UpdatedAt 画像最近一次更新时间（用于增量同步与缓存失效）。
	UpdatedAt time.Time
}

// StaticProfile 静态画像层，注册时填写，缓慢变化。
type StaticProfile struct {
	// Tier 用户分级。
	Tier string
	// RegisterAt 注册时间。
	RegisterAt time.Time
	// Tags 注册标签。
	Tags []string
	// Interests 注册时填写的兴趣领域（如 "科技/旅行/财经"）。
	Interests []string
	// Purpose 使用用途（如 "信息获取/娱乐/学习/职业"）。
	Purpose string
	// PreferredArticleTypes 偏好文章类型（如 "深度长文/快讯/教程/观点"）。
	PreferredArticleTypes []string
	// PreferredLength 偏好文章长度（如 "short/medium/long"）。
	PreferredLength string
	// PreferredStyle 偏好写作风格（如 "严肃/轻松/学术/口语"）。
	PreferredStyle string
	// Background 读者背景（如 "学生/工程师/管理者/投资人"）。
	Background string
	// Difficulty 偏好内容难度（如 "beginner/intermediate/expert"）。
	Difficulty string
	// ExcludedContent 排除内容标签（不希望看到的主题/类型）。
	ExcludedContent []string
	// ReadingPeriods 阅读时段（如 "09-12/20-23"，对应 PeriodicPattern 槽位）。
	ReadingPeriods []string
	// PersonalizationType 个性化推荐类型（如 "explore/exploit/balanced"）。
	PersonalizationType string
}

// DynamicProfile 动态画像层，随行为累积变化。
type DynamicProfile struct {
	// Topics 当前兴趣主题。
	Topics []string
	// LastActiveAt 最后活跃时间。
	LastActiveAt time.Time
	// Activity 活跃度（0-1，按近期行为频次归一化）。
	Activity float64
	// SessionCount 累计会话数。
	SessionCount int
	// TotalReads 累计阅读文章数。
	TotalReads int
	// PreferenceTrail 偏好变化轨迹（按时间顺序记录兴趣迁移）。
	PreferenceTrail []PreferenceChange
}

// BehaviorProfile 行为画像层，近期行为摘要。
type BehaviorProfile struct {
	// RecentClicks 最近点击文章 ID。
	RecentClicks []string
	// LikedTags 点赞过的标签聚合。
	LikedTags map[string]int
	// DislikedTags 不感兴趣的标签聚合。
	DislikedTags map[string]int
	// RecentLikes 最近点赞文章 ID（保留最近 N 条）。
	RecentLikes []string
	// RecentFavorites 最近收藏文章 ID（保留最近 N 条）。
	RecentFavorites []string
	// RecentCompletions 最近完成阅读文章 ID（保留最近 N 条）。
	RecentCompletions []string
	// RecentSearches 最近搜索关键词（保留最近 N 条）。
	RecentSearches []string
}

// PreferenceChange 偏好变化轨迹条目，记录兴趣迁移事件。
type PreferenceChange struct {
	// Tag 兴趣标签。
	Tag string
	// Action 变化动作（"add"/"weaken"/"remove"）。
	Action string
	// Delta 权重变化量。
	Delta float64
	// ChangedAt 变化时间。
	ChangedAt time.Time
}

// TemporalProfile 时间画像：长期/短期/会话/周期/趋势/衰减/淘汰。
// 二开：通过 TemporalProfileConfig 配置衰减参数，或实现 DecayEngine 注入自定义公式。
type TemporalProfile struct {
	// LongTerm 长期兴趣（90 天累积）。
	LongTerm []Interest
	// ShortTerm 短期兴趣（7 天累积）。
	ShortTerm []Interest
	// Session 会话内兴趣（30 分钟）。
	Session []Interest
	// Periodic 周期模式（按时间槽位，如 "weekday-9" / "weekend-20"）。
	Periodic map[string][]Interest
	// Trending 趋势兴趣（24h 突增）。
	Trending []Interest
	// DecayState 衰减状态（每兴趣当前的衰减后强度）。
	DecayState map[string]DecayState
	// EliminationPool 淘汰池（被淘汰的兴趣标签，可被复活）。
	EliminationPool []string
	// PeriodicPattern 结构化周期模式（按时间槽位，如 "09-12"，对应 InterestEntry 列表）。
	// 与 Periodic map 并存：Periodic 用于按 weekday/weekend 维度，PeriodicPattern 用于按小时槽位。
	PeriodicPattern PeriodicPattern
	// GlobalDecayState 全局衰减状态（记录上次整体衰减时间与全局 tau 常数）。
	GlobalDecayState GlobalDecayState
	// EliminationEntries 淘汰池（含完整 Interest 条目，可携带 Weight/Strength 等用于复活）。
	// 与 EliminationPool []string 并存：后者仅存 Tag，前者保留完整画像用于复活策略。
	EliminationEntries []Interest
	// UpdatedAt 时间画像最近一次更新时间。
	UpdatedAt time.Time
}

// Interest 兴趣条目，含衰减与强化参数。
type Interest struct {
	// Tag 兴趣标签。
	Tag string
	// Weight 兴趣权重。
	Weight float64
	// Strength 强度 S（用于衰减计算）。
	Strength float64
	// LastReinforcedAt 最后强化时间。
	LastReinforcedAt time.Time
	// DecayTau 衰减时间常数（指数衰减 tau）。
	DecayTau float64
	// Archived 是否归档（淘汰池中）。
	Archived bool
}

// DecayState 衰减状态。
type DecayState struct {
	// Tag 兴趣标签。
	Tag string
	// Score 衰减后分数。
	Score float64
	// Updated 最后更新时间。
	Updated time.Time
	// LastDecayAt 该兴趣最近一次应用衰减的时间（用于增量衰减计算）。
	LastDecayAt time.Time
	// GlobalTau 全局衰减时间常数 tau（秒），用于 Ebbinghaus 公式 R = exp(-t/tau)。
	GlobalTau float64
}

// GlobalDecayState 全局衰减状态，记录时间画像整体衰减元信息。
// 与 DecayState（每兴趣一条）不同，GlobalDecayState 是全局唯一的衰减调度状态。
type GlobalDecayState struct {
	// LastDecayAt 上次整体衰减执行时间。
	LastDecayAt time.Time
	// GlobalTau 全局衰减时间常数 tau（秒），作为未显式设置 DecayTau 的兴趣条目的默认值。
	GlobalTau float64
}

// ----------------------------------------------------------------------------
// 召回 / 排序 / 重排
// ----------------------------------------------------------------------------

// Candidate 候选文章。
type Candidate struct {
	// ArticleID 文章 ID。
	ArticleID string
	// Score 候选分数（召回/排序/重排分数）。
	Score float64
	// Source 来源（recall/cf/graph/search/channel/rule）。
	Source string
	// Scores 特征分数 map，供排序器（WeightedRanker/LRRanker）做特征工程。
	// 键为特征名（如 "cf_score"/"graph_score"/"freshness"），值为特征值。
	// 二开扩展点：业务方可注入自定义特征，排序器按 weights 加权计算。
	Scores map[string]float64
	// Extra 额外元数据（特征/质量分等）。
	Extra map[string]any
}

// RecallRequest 召回请求。
type RecallRequest struct {
	// UserKey 用户标识。
	UserKey UserKey
	// Profile 用户画像。
	Profile *UserProfile
	// Channel 频道名（可空）。
	Channel string
	// TagFilter 标签过滤。
	TagFilter TagFilter
	// TopK 召回数量。
	TopK int
	// Intent 意图（来自 IntentAgent，可空）。
	Intent *Intent
}

// RecallResult 召回结果。
type RecallResult struct {
	// Candidates 候选列表。
	Candidates []Candidate
	// Source 召回源名称。
	Source string
}

// RankContext 排序上下文。
type RankContext struct {
	// Profile 用户画像。
	Profile *UserProfile
	// TemporalProfile 时间画像。
	TemporalProfile *TemporalProfile
	// Channel 频道。
	Channel string
	// Intent 意图。
	Intent *Intent
	// Candidates 待排序候选。
	Candidates []Candidate
}

// RankResult 排序结果。
type RankResult struct {
	// Candidates 排序后候选。
	Candidates []Candidate
}

// RerankRequest 重排请求。
type RerankRequest struct {
	// UserKey 用户标识。
	UserKey UserKey
	// Candidates 待重排候选。
	Candidates []Candidate
	// Query 查询（搜索场景）。
	Query string
	// Model 重排模型（self/external/llm）。
	Model string
	// TopK 重排后保留数量。
	TopK int
}

// RerankResult 重排结果。
type RerankResult struct {
	// Candidates 重排后候选。
	Candidates []Candidate
	// ModelUsed 实际使用的模型。
	ModelUsed string
}

// ----------------------------------------------------------------------------
// 质量
// ----------------------------------------------------------------------------

// QualityRequest 质量评判请求。
type QualityRequest struct {
	// ArticleID 文章 ID。
	ArticleID string
	// Content 文章内容。
	Content string
	// Rubric 评判标准（可空，用默认 rubric）。
	Rubric string
}

// ArticleQuality 文章质量 6 维评分。
// 二开：通过 RecommendConfig.QualityModel 切换评判模型，或实现 QualityJudger 自定义评判逻辑。
type ArticleQuality struct {
	// ArticleID 文章 ID。
	ArticleID string
	// Authority 权威性（0-1）。
	Authority float64
	// Depth 深度（0-1）。
	Depth float64
	// Freshness 新鲜度（0-1）。
	Freshness float64
	// Completeness 完整性（0-1）。
	Completeness float64
	// Readability 可读性（0-1）。
	Readability float64
	// Citation 引用质量（0-1）。
	Citation float64
	// Overall 综合评分（0-1，加权）。
	Overall float64
	// Grade 质量等级（A-T）。
	Grade string
}

// ----------------------------------------------------------------------------
// 搜索
// ----------------------------------------------------------------------------

// SearchQuery 搜索查询。
type SearchQuery struct {
	// Query 原始查询。
	Query string
	// UserKey 用户标识（个性化搜索）。
	UserKey UserKey
	// Filter 搜索过滤。
	Filter SearchFilter
	// TopK 返回数量。
	TopK int
	// Intent 搜索意图（来自查询理解）。
	Intent *SearchIntent
}

// SearchFilter 搜索过滤。
type SearchFilter struct {
	// Tags 标签过滤。
	Tags []string
	// Authors 作者过滤。
	Authors []string
	// Channel 频道过滤。
	Channel string
	// TimeRange 时间范围。
	TimeRange string
	// QualityThreshold 质量阈值。
	QualityThreshold float64
}

// SearchIntent 搜索意图。
type SearchIntent struct {
	// Label 意图标签（informational/navigational/transactional/comparative）。
	Label string
	// Entities 识别实体。
	Entities []Entity
	// TimeIntent 时效意图。
	TimeIntent string
}

// SearchResult 搜索结果。
type SearchResult struct {
	// Hits 命中列表。
	Hits []SearchHit
	// RewrittenQuery 重写后查询。
	RewrittenQuery string
	// GraphKnowledge 图谱知识（实体识别后查图谱获取关联信息）。
	GraphKnowledge *GraphKnowledge
}

// SearchHit 搜索命中。
type SearchHit struct {
	// ArticleID 文章 ID。
	ArticleID string
	// Score 命中分数。
	Score float64
	// Source 来源（vector/bm25/graph/image）。
	Source string
}

// ----------------------------------------------------------------------------
// 事件与 Hook
// ----------------------------------------------------------------------------

// BehaviorEvent 强类型行为事件。
// 二开：通过 HookRegistry.Register 注册 Hook 消费事件，扩展业务逻辑。
type BehaviorEvent struct {
	// EventID 事件唯一 ID。
	EventID string
	// EventType 事件类型（fast_path_hit/slow_path_hit/hybrid_merge/
	// graph_query/cypher_generated/skill_invoked/click/like/dislike/...）。
	EventType string
	// UserID 用户 ID。
	UserID string
	// ArticleID 文章 ID（可为空）。
	ArticleID string
	// Channel 频道。
	Channel string
	// PathTaken 路径（fast/slow/hybrid）。
	PathTaken string
	// Timestamp 事件时间。
	Timestamp time.Time
	// Payload 扩展字段。
	Payload map[string]any
}

// Hook 行为事件 Hook 契约。
// 二开：实现该 interface 并通过 HookRegistry.Register 注入。
type Hook interface {
	// Name 返回 Hook 名称。
	Name() string
	// OnEvent 处理事件；错误隔离不影响主流程与其他 Hook。
	OnEvent(ctx context.Context, event BehaviorEvent) error
}

// ----------------------------------------------------------------------------
// 推荐请求 / 响应 / 配置
// ----------------------------------------------------------------------------

// RecommendRequest 推荐请求。
type RecommendRequest struct {
	// UserKey 用户标识。
	UserKey UserKey
	// Channel 频道（可空）。
	Channel string
	// TagFilter 标签过滤。
	TagFilter TagFilter
	// Config 推荐配置。
	Config RecommendConfig
	// PathMode 路径模式（auto/fast/slow/hybrid）。
	PathMode string
	// TopK 返回数量。
	TopK int
	// Explain 是否生成推荐解释。
	Explain bool
}

// RecommendResponse 推荐响应。
type RecommendResponse struct {
	// Candidates 候选列表。
	Candidates []Candidate
	// Cost 成本报告。
	Cost CostReport
	// Config 实际生效配置。
	Config RecommendConfig
	// Channel 频道。
	Channel string
	// QualityScores 质量评分（articleID -> ArticleQuality）。
	QualityScores map[string]ArticleQuality
	// TemporalProfile 时间画像快照。
	TemporalProfile *TemporalProfile
	// CFScores 协同过滤分数（articleID -> score）。
	CFScores map[string]float64
	// RerankScores 重排分数（articleID -> score）。
	RerankScores map[string]float64
	// PathTaken 实际路径（fast/slow/hybrid）。
	PathTaken string
	// GraphTrace 图谱查询链路。
	GraphTrace *GraphTrace
	// Explain 推荐解释文本。
	Explain string
}

// RecommendConfig 推荐配置，所有可二开项通过该结构暴露。
// 二开：通过 config.yaml 配置，或通过依赖注入替换。
type RecommendConfig struct {
	// TopK 返回数量。
	TopK int
	// RecallStrategy 召回策略（rule/content/cf/graph/channel/hybrid）。
	RecallStrategy string
	// RerankModel 重排模型（self/external/llm）。
	RerankModel string
	// RerankWeights 重排权重。
	RerankWeights map[string]float64
	// AgentLoopEnabled 是否启用 Agent 循环。
	AgentLoopEnabled bool
	// FallbackEnabled 是否启用兜底。
	FallbackEnabled bool
	// MaxToolCalls Agent 最大工具调用次数。
	MaxToolCalls int
	// PromptCacheEnabled 是否启用 Prompt Cache。
	PromptCacheEnabled bool
	// SummaryEnabled 是否启用 Session Summary。
	SummaryEnabled bool
	// BestOfN Best-of-N 数量。
	BestOfN int
	// QualityThreshold 质量阈值。
	QualityThreshold float64
	// CFEnabled 是否启用协同过滤。
	CFEnabled bool
	// SearchEnabled 是否启用搜索增强。
	SearchEnabled bool
	// TemporalDecayEnabled 是否启用时间画像衰减。
	TemporalDecayEnabled bool
	// PathMode 路径模式（auto/fast/slow/hybrid）。
	PathMode string
}

// CostReport 成本报告。
type CostReport struct {
	// TokensIn 输入 token 数。
	TokensIn int
	// TokensOut 输出 token 数。
	TokensOut int
	// CachedTokens 命中 Prompt Cache 的 token 数。
	CachedTokens int
	// LLMCalls LLM 调用次数。
	LLMCalls int
	// EstimatedCost 估算费用（元）。
	EstimatedCost float64
}

// ----------------------------------------------------------------------------
// 频道
// ----------------------------------------------------------------------------

// Channel IP 频道定义。
// 二开：通过 ChannelManager.RegisterChannel 注册自定义频道，无需改核心代码。
type Channel struct {
	// Name 频道名。
	Name string
	// TypeTag 主类型标签。
	TypeTag string
	// SecondaryTags 次级标签。
	SecondaryTags []string
	// FilterKey 画像 filterKey（频道隔离）。
	FilterKey string
	// QualityThreshold 独立质量阈值。
	QualityThreshold float64
	// RerankModel 独立 rerank 模型版本。
	RerankModel string
	// PoolSize 独立召回池大小。
	PoolSize int
}

// ChannelRouteRequest 频道路由请求。
type ChannelRouteRequest struct {
	// UserKey 用户标识。
	UserKey UserKey
	// Channel 频道名。
	Channel string
	// Intent 意图。
	Intent *Intent
}

// ChannelRouteResult 频道路由结果。
type ChannelRouteResult struct {
	// Channel 频道。
	Channel Channel
	// PoolKey 独立池键。
	PoolKey string
	// FilterKey 画像 filterKey。
	FilterKey string
}

// ----------------------------------------------------------------------------
// 意图与实体
// ----------------------------------------------------------------------------

// Intent 意图理解结果（来自 IntentAgent）。
// Complexity 字段驱动 OrchestratorAgent 路由（simple→fast, medium→hybrid, complex→slow）。
type Intent struct {
	// Label 意图标签。
	Label string
	// Confidence 置信度（0-1）。
	Confidence float64
	// Signals 意图信号。
	Signals map[string]string
	// Complexity 复杂度（simple/medium/complex），驱动路径决策。
	Complexity string
	// Entities 识别实体。
	Entities []Entity
	// TimeIntent 时效意图。
	TimeIntent string
}

// Entity 实体。
type Entity struct {
	// Name 实体名。
	Name string
	// Type 实体类型（Author/IP/Tag/Title/Topic）。
	Type string
}

// TagFilter 标签过滤。
type TagFilter struct {
	// Include 包含标签。
	Include []string
	// Exclude 排除标签。
	Exclude []string
}

// ----------------------------------------------------------------------------
// 知识图谱
// ----------------------------------------------------------------------------

// GraphNode 图谱节点。
type GraphNode struct {
	// ID 节点 ID。
	ID string
	// Type 节点类型（Article/Tag/TypeTag/Author/IP/Channel/Topic/Entity/
	// Image/User/Behavior/QualityReport）。
	Type string
	// Props 节点属性。
	Props map[string]any
}

// GraphEdge 图谱边。
type GraphEdge struct {
	// From 起点节点 ID。
	From string
	// To 终点节点 ID。
	To string
	// Type 关系类型（HAS_TAG/WROTE_BY/BELONGS_IP/LIKED/CO_OCCURRED_WITH/
	// VISUALLY_SIMILAR 等）。
	Type string
	// Props 边属性。
	Props map[string]any
}

// GraphRecallRequest 图谱召回请求。
type GraphRecallRequest struct {
	// UserKey 用户标识。
	UserKey UserKey
	// Hop 多跳跳数。
	Hop int
	// TopK 召回数量。
	TopK int
	// Pattern 召回模式（user_like_similar/user_similar_like/co_occurrence/
	// entity/article_author/ip）。
	Pattern string
}

// GraphKnowledge 图谱知识（搜索场景）。
type GraphKnowledge struct {
	// Entities 关联实体。
	Entities []Entity
	// Edges 关联边。
	Edges []GraphEdge
	// Articles 关联文章 ID。
	Articles []string
	// Authors 关联作者 ID。
	Authors []string
	// IPs 关联 IP 名称。
	IPs []string
	// Cypher 生成的 Cypher 语句（调试用）。
	Cypher string
}

// RewrittenQuery 查询重写结果。
//
// 由 QueryRewriter 输出，包含原始查询、重写查询、同义词扩展、相关词与拼写纠正。
// 二开：实现 QueryRewriter interface 注入业务同义词与领域词典。
type RewrittenQuery struct {
	// Original 原始 query。
	Original string
	// Rewritten 重写后 query。
	Rewritten string
	// Synonyms 同义词扩展。
	Synonyms []string
	// Related 相关词。
	Related []string
	// Correction 拼写纠正（空表示无需纠正）。
	Correction string
}

// SearchSuggestion 搜索补全。
type SearchSuggestion struct {
	// Text 补全文本。
	Text string
	// Score 补全分。
	Score float64
	// Source 来源（prefix/hot/personalize）。
	Source string
}

// SearchHistoryEntry 搜索历史条目。
type SearchHistoryEntry struct {
	// Query 搜索词。
	Query string
	// UserID 用户 ID。
	UserID string
	// SearchedAt 搜索时间（毫秒）。
	SearchedAt int64
	// ClickedArticleID 点击的文章 ID（空表示未点击）。
	ClickedArticleID string
	// ClickedAt 点击时间（毫秒）。
	ClickedAt int64
}

// GraphTrace 图谱查询链路。
type GraphTrace struct {
	// Cypher 执行的 Cypher 语句。
	Cypher string
	// Results 结果数。
	Results int
	// Duration 耗时。
	Duration time.Duration
}

// ----------------------------------------------------------------------------
// Skill
// ----------------------------------------------------------------------------

// Skill Skill 描述契约（Anthropic Agent Skills 规范）。
// 二开：业务方放置技能目录（SKILL.md + 工具 + 资源），框架自动加载。
type Skill struct {
	// Name Skill 名称（如 recall.content）。
	Name string
	// Description Skill 描述。
	Description string
	// Tools 绑定工具名。
	Tools []string
	// Resources 资源路径。
	Resources []string
}

// SkillInput Skill 调用输入。
type SkillInput struct {
	// Args 调用参数。
	Args map[string]any
}

// SkillOutput Skill 调用输出。
type SkillOutput struct {
	// Result 调用结果。
	Result any
}

// ----------------------------------------------------------------------------
// Agent
// ----------------------------------------------------------------------------

// Agent 可执行 Agent 契约。
// 真实实现基于 trpc-agent-go 的 GraphAgent/LLMAgent/Runner/Best-of-N/Evaluation。
// 二开：实现该 interface 并通过 AgentFactory 注册自定义 Agent。
type Agent interface {
	// Run 执行 Agent 主循环。
	// ctx 上下文；input 输入（用户/会话/消息/状态）。
	// 返回 AgentOutput 与 error。
	Run(ctx context.Context, input AgentInput) (AgentOutput, error)
	// Name 返回 Agent 名称。
	Name() string
}

// AgentInput Agent 输入。
type AgentInput struct {
	// UserID 用户 ID。
	UserID string
	// SessionID 会话 ID。
	SessionID string
	// Message 输入消息。
	Message string
	// State 状态 schema（candidates/rerank_items/quality_scores/...）。
	State map[string]any
}

// AgentOutput Agent 输出。
type AgentOutput struct {
	// State 输出状态。
	State map[string]any
	// Result 输出结果。
	Result any
	// Trace 执行轨迹。
	Trace []string
}

// AgentOptions Agent 构造选项。
type AgentOptions struct {
	// Model 模型名。
	Model string
	// Tools 绑定工具名。
	Tools []string
	// Skills 绑定 Skill 名。
	Skills []string
	// MaxToolCalls 最大工具调用次数。
	MaxToolCalls int
	// PromptCacheEnabled 是否启用 Prompt Cache。
	PromptCacheEnabled bool
}
