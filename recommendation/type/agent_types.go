package types

// RecommendRequest 推荐请求（非对话式入口）。
type RecommendRequest struct {
	RecRequestID string `json:"rec_request_id"`
	UserID       string `json:"user_id"`
	SessionID    string `json:"session_id"`
	Surface      string `json:"surface"`
	Query        string `json:"query"`

	// Explain: 返回可观测解释信息（调试/评估用）。
	Explain bool `json:"explain,omitempty"`

	// 周期桶：用于周期池/周期记忆（例如 d1 / w1 / weekend 等）
	PeriodBucket string `json:"period_bucket"`
}

// RecommendResponse 推荐接口返回结构
type RecommendResponse struct {
	TraceID      string `json:"trace_id"`
	RecRequestID string `json:"rec_request_id"`
	Status       string `json:"status"`

	// IDs：推荐结果的文章主键列表（对齐你们的 article.id）
	IDs []string `json:"ids"`

	// ArticleIDs：兼容旧字段（不破坏老调用方），可以后续逐步废弃
	ArticleIDs []string `json:"article_ids,omitempty"`

	Explanation string `json:"explanation"`

	// ExplainTrace: 结构化可解释信息（仅在请求 Explain=true 时返回）
	ExplainTrace any `json:"explain_trace,omitempty"`
}

// IntentResult intent.parse 的返回结构。
type IntentResult struct {
	Label      string   `json:"label"`
	Confidence float64  `json:"confidence"`
	Signals    []string `json:"signals"`
}

// RouteDecision policy.route 的返回结构。
type RouteDecision struct {
	Chosen          string   `json:"chosen"`
	ReasonCodes     []string `json:"reason_codes"`
	MustCiteSources bool     `json:"must_cite_sources"`
	MaxToolCalls    int      `json:"max_tool_calls"`
}

// RerankItem ai_rerank_articles 的输出元素。
type RerankItem struct {
	ArticleID string  `json:"article_id"`
	Score     float64 `json:"score"`
	Reason    string  `json:"reason"`
}
