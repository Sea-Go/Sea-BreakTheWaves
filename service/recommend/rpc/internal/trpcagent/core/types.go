package recommendationv2

import (
	"encoding/json"
	"time"
)

const (
	PathFast   = "fast"
	PathSlow   = "slow"
	PathHybrid = "hybrid"
	PathAuto   = "auto"

	EventImpression        = "impression"
	EventClick             = "click"
	EventLike              = "like"
	EventDislike           = "dislike"
	EventFavorite          = "favorite"
	EventReadComplete      = "read_complete"
	EventSearch            = "search"
	EventRequestStarted    = "request_started"
	EventPathRouted        = "path_routed"
	EventRecallDone        = "recall_done"
	EventRankDone          = "rank_done"
	EventRerankDone        = "rerank_done"
	EventQualityDone       = "quality_done"
	EventRequestFinished   = "request_finished"
	EventAgentStarted      = "agent_started"
	EventLLMCall           = "llm_call"
	EventToolCall          = "tool_call"
	EventSkillInvoked      = "skill_invoked"
	EventAgentFinished     = "agent_finished"
	EventCostUpdated       = "cost_updated"
	EventTraceEmitted      = "trace_emitted"
	EventFallbackTriggered = "fallback_triggered"
	EventError             = "error"
)

type UserIdentity struct {
	TenantID    string         `json:"tenant_id"`
	UserID      string         `json:"user_id"`
	AnonymousID string         `json:"anonymous_id,omitempty"`
	SessionID   string         `json:"session_id,omitempty"`
	DeviceID    string         `json:"device_id,omitempty"`
	Channel     string         `json:"channel,omitempty"`
	Locale      string         `json:"locale,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Extension   map[string]any `json:"extension,omitempty"`
}

type UserProfile struct {
	Static       map[string]any `json:"static,omitempty"`
	Dynamic      map[string]any `json:"dynamic,omitempty"`
	Behavior     map[string]any `json:"behavior,omitempty"`
	Temporal     map[string]any `json:"temporal,omitempty"`
	BusinessTags []string       `json:"business_tags,omitempty"`
	Extension    map[string]any `json:"extension,omitempty"`
	UpdatedAt    time.Time      `json:"updated_at,omitempty"`
}

type ProfileSnapshot struct {
	User       UserIdentity `json:"user"`
	Profile    UserProfile  `json:"profile"`
	SnapshotID string       `json:"snapshot_id"`
	CreatedAt  time.Time    `json:"created_at"`
}

type RecommendRequest struct {
	TenantID  string           `json:"tenant_id"`
	RequestID string           `json:"request_id"`
	Scenario  string           `json:"scenario"`
	Channel   string           `json:"channel"`
	User      UserIdentity     `json:"user"`
	Query     string           `json:"query,omitempty"`
	Context   map[string]any   `json:"context,omitempty"`
	TopK      int              `json:"top_k,omitempty"`
	PathMode  string           `json:"path_mode,omitempty"`
	Config    *RecommendConfig `json:"config,omitempty"`
	Debug     bool             `json:"debug,omitempty"`
	Extension map[string]any   `json:"extension,omitempty"`
}

type RecommendResponse struct {
	RequestID    string          `json:"request_id"`
	TraceID      string          `json:"trace_id"`
	PathTaken    string          `json:"path_taken"`
	Items        []RecommendItem `json:"items"`
	Explanations []Explanation   `json:"explanations,omitempty"`
	Cost         CostReport      `json:"cost"`
	Metrics      MetricReport    `json:"metrics"`
	Steps        []TraceStep     `json:"steps,omitempty"`
	Fallback     *FallbackReport `json:"fallback,omitempty"`
	Extension    map[string]any  `json:"extension,omitempty"`
}

type RecommendItem struct {
	ID        string         `json:"id"`
	ArticleID string         `json:"article_id"`
	Score     float64        `json:"score"`
	Source    string         `json:"source"`
	Rank      int            `json:"rank"`
	Reason    string         `json:"reason,omitempty"`
	Features  map[string]any `json:"features,omitempty"`
	Extension map[string]any `json:"extension,omitempty"`
}

type Explanation struct {
	ItemID string `json:"item_id,omitempty"`
	Text   string `json:"text"`
	Source string `json:"source"`
}

type RecommendConfig struct {
	PathMode *string             `json:"path_mode,omitempty"`
	Recall   RecallConfig        `json:"recall"`
	Rank     RankConfig          `json:"rank"`
	Rerank   RerankConfig        `json:"rerank"`
	Cost     CostConfig          `json:"cost"`
	Obs      ObservabilityConfig `json:"obs"`
}

type RecallConfig struct {
	Sources        []string           `json:"sources,omitempty"`
	TopK           *int               `json:"top_k,omitempty"`
	Weights        map[string]float64 `json:"weights,omitempty"`
	TimeoutMillis  *int               `json:"timeout_millis,omitempty"`
	FallbackSource string             `json:"fallback_source,omitempty"`
}

type RankConfig struct {
	FeatureWeights map[string]float64 `json:"feature_weights,omitempty"`
	ModelVersion   string             `json:"model_version,omitempty"`
	ABBucket       string             `json:"ab_bucket,omitempty"`
}

type RerankConfig struct {
	Model         string   `json:"model,omitempty"`
	TopN          *int     `json:"top_n,omitempty"`
	Budget        *float64 `json:"budget,omitempty"`
	TimeoutMillis *int     `json:"timeout_millis,omitempty"`
}

type CostConfig struct {
	MaxTokens     *int     `json:"max_tokens,omitempty"`
	MaxAmount     *float64 `json:"max_amount,omitempty"`
	ModelTier     string   `json:"model_tier,omitempty"`
	CacheStrategy string   `json:"cache_strategy,omitempty"`
}

type ObservabilityConfig struct {
	TraceSampleRate *float64 `json:"trace_sample_rate,omitempty"`
	ReturnSteps     *bool    `json:"return_steps,omitempty"`
	ReturnExplain   *bool    `json:"return_explain,omitempty"`
}

type EffectiveRecommendConfig struct {
	PathMode string
	Recall   EffectiveRecallConfig
	Rank     RankConfig
	Rerank   EffectiveRerankConfig
	Cost     EffectiveCostConfig
	Obs      EffectiveObservabilityConfig
}

type EffectiveRecallConfig struct {
	Sources        []string
	TopK           int
	Weights        map[string]float64
	TimeoutMillis  int
	FallbackSource string
}

type EffectiveRerankConfig struct {
	Model         string
	TopN          int
	Budget        float64
	TimeoutMillis int
}

type EffectiveCostConfig struct {
	MaxTokens     int
	MaxAmount     float64
	ModelTier     string
	CacheStrategy string
}

type EffectiveObservabilityConfig struct {
	TraceSampleRate float64
	ReturnSteps     bool
	ReturnExplain   bool
}

type ConfigScope struct {
	TenantID string
	Channel  string
	Scenario string
}

type StreamEvent struct {
	Type      string             `json:"type"`
	TraceID   string             `json:"trace_id,omitempty"`
	RequestID string             `json:"request_id,omitempty"`
	Step      *TraceStep         `json:"step,omitempty"`
	Response  *RecommendResponse `json:"response,omitempty"`
	Event     *BehaviorEvent     `json:"event,omitempty"`
	Error     string             `json:"error,omitempty"`
	Metadata  map[string]any     `json:"metadata,omitempty"`
}

type TraceQueryRequest struct {
	TraceID   string `form:"trace_id" json:"trace_id,omitempty"`
	RequestID string `form:"request_id" json:"request_id,omitempty"`
	UserID    string `form:"user_id" json:"user_id,omitempty"`
	TenantID  string `form:"tenant_id" json:"tenant_id,omitempty"`
	Channel   string `form:"channel" json:"channel,omitempty"`
	Path      string `form:"path" json:"path,omitempty"`
	Skill     string `form:"skill" json:"skill,omitempty"`
	Limit     int    `form:"limit" json:"limit,omitempty"`
}

type TraceQueryResponse struct {
	Traces []TraceRecord `json:"traces"`
	Total  int           `json:"total"`
}

type TraceRecord struct {
	TraceID     string      `json:"trace_id"`
	RequestID   string      `json:"request_id"`
	TenantID    string      `json:"tenant_id"`
	UserID      string      `json:"user_id,omitempty"`
	AnonymousID string      `json:"anonymous_id,omitempty"`
	Channel     string      `json:"channel"`
	Scenario    string      `json:"scenario"`
	Path        string      `json:"path"`
	StartedAt   time.Time   `json:"started_at"`
	FinishedAt  time.Time   `json:"finished_at"`
	LatencyMS   int64       `json:"latency_ms"`
	Steps       []TraceStep `json:"steps"`
	Cost        CostReport  `json:"cost"`
	Fallback    bool        `json:"fallback"`
	Items       int         `json:"items"`
	Skills      []string    `json:"skills,omitempty"`
}

type CostReport struct {
	TokensIn         int     `json:"tokens_in"`
	TokensOut        int     `json:"tokens_out"`
	CachedTokens     int     `json:"cached_tokens"`
	LLMCalls         int     `json:"llm_calls"`
	ToolCalls        int     `json:"tool_calls"`
	GraphQueries     int     `json:"graph_queries"`
	VectorQueries    int     `json:"vector_queries"`
	RerankCost       float64 `json:"rerank_cost"`
	EstimatedAmount  float64 `json:"estimated_amount"`
	CacheSavedAmount float64 `json:"cache_saved_amount"`
}

type MetricReport struct {
	LatencyMillis int64             `json:"latency_millis"`
	Path          string            `json:"path"`
	ItemCount     int               `json:"item_count"`
	Fallback      bool              `json:"fallback"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type TraceStep struct {
	Name           string         `json:"name"`
	Type           string         `json:"type"`
	Status         string         `json:"status"`
	StartedAt      time.Time      `json:"started_at"`
	FinishedAt     time.Time      `json:"finished_at"`
	DurationMillis int64          `json:"duration_millis"`
	Cost           CostReport     `json:"cost,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Error          string         `json:"error,omitempty"`
}

type FallbackReport struct {
	Triggered bool   `json:"triggered"`
	Reason    string `json:"reason,omitempty"`
	Source    string `json:"source,omitempty"`
}

type BehaviorEvent struct {
	EventID         string          `json:"event_id"`
	EventType       string          `json:"event_type"`
	RequestID       string          `json:"request_id,omitempty"`
	TraceID         string          `json:"trace_id,omitempty"`
	TenantID        string          `json:"tenant_id,omitempty"`
	User            UserIdentity    `json:"user"`
	ArticleID       string          `json:"article_id,omitempty"`
	Rank            int             `json:"rank,omitempty"`
	Channel         string          `json:"channel,omitempty"`
	PathTaken       string          `json:"path_taken,omitempty"`
	Timestamp       time.Time       `json:"timestamp"`
	Metadata        map[string]any  `json:"metadata,omitempty"`
	RawEvent        json.RawMessage `json:"raw_event,omitempty"`
	ActivityContext map[string]any  `json:"activity_context,omitempty"`
}

type EventBatchRequest struct {
	Events []BehaviorEvent `json:"events"`
}

type EventBatchResponse struct {
	Accepted         int                      `json:"accepted"`
	Rejected         []string                 `json:"rejected,omitempty"`
	ActivityAnalysis []ActivityAnalysisResult `json:"activity_analysis,omitempty"`
}
