package types

// 这些类型用于链路日志/观测 schema，供 zlog 输出 zap.Any(...)。

// Status：请求/step 的结果状态（对应日志 status 字段）。
type Status int

const (
	StatusOK       Status = 200
	StatusDegraded Status = 520
	StatusFallback Status = 521
	StatusError    Status = 500
)

type ExpID struct {
	Name    string `json:"name"`
	Variant string `json:"variant"`
}

// BaseContext：贯穿整条链路的低基数字段 + 脱敏字段。
type BaseContext struct {
	TraceID          string  `json:"trace_id"`
	SpanID           string  `json:"span_id"`
	RecRequestID     string  `json:"rec_request_id"`
	UserIDHash       string  `json:"user_id_hash"`
	SessionID        string  `json:"session_id"`
	Surface          string  `json:"surface"`
	ExpIDs           []ExpID `json:"exp_ids"`
	AgentName        string  `json:"agent_name"`
	Locale           string  `json:"locale"`
	UserTier         string  `json:"user_tier"`
	TimeSkewDetected *bool   `json:"time_skew_detected,omitempty"`
}

type ModelInfo struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type Intent struct {
	Label            string   `json:"label"`
	Confidence       float64  `json:"confidence,omitempty"`
	ConfidenceBucket string   `json:"confidence_bucket,omitempty"`
	Signals          []string `json:"signals,omitempty"`
}

type Decision struct {
	ID           string            `json:"id,omitempty"`
	Type         string            `json:"type"`
	Status       Status            `json:"status,omitempty"`
	Chosen       string            `json:"chosen"`
	Confidence   float64           `json:"confidence,omitempty"`
	ReasonCodes  []string          `json:"reason_codes,omitempty"`
	Signals      map[string]any    `json:"signals,omitempty"`
	Constraints  map[string]any    `json:"constraints,omitempty"`
	Alternatives []map[string]any  `json:"alternatives,omitempty"`
	ArtifactsRef map[string]string `json:"artifacts_ref,omitempty"`
}

type Retrieval struct {
	Source           string   `json:"source"`
	QueryCount       int      `json:"query_count,omitempty"`
	RequestedTopK    int      `json:"requested_topk,omitempty"`
	ReturnedDocCount int      `json:"returned_doc_count"`
	Empty            bool     `json:"empty,omitempty"`
	CoverageScore    float64  `json:"coverage_score,omitempty"`
	DocIDHashesTop5  []string `json:"doc_id_hashes_top5,omitempty"`
}

type ToolCall struct {
	Name            string         `json:"name"`
	IOSchemaVersion string         `json:"io_schema_version,omitempty"`
	ArgsSummary     map[string]any `json:"args_summary,omitempty"`
	ArgsRef         string         `json:"args_ref,omitempty"`
	Outcome         string         `json:"outcome,omitempty"`
	ErrorClass      string         `json:"error_class,omitempty"`
	ResultSummary   map[string]any `json:"result_summary,omitempty"`
	ResultRef       string         `json:"result_ref,omitempty"`
}

type Gen struct {
	TokensIn    int    `json:"tokens_in,omitempty"`
	TokensOut   int    `json:"tokens_out,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
	ResponseRef string `json:"response_ref,omitempty"`
}

type Quality struct {
	SchemaValid         bool     `json:"schema_valid"`
	CitationValid       bool     `json:"citation_valid"`
	ClaimGroundingCheck string   `json:"claim_grounding_check"`
	Violations          []string `json:"violations,omitempty"`
}
