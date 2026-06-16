package types

import "time"

const (
	RecoEventImpression   = "impression"
	RecoEventClick        = "click"
	RecoEventLike         = "like"
	RecoEventDislike      = "dislike"
	RecoEventFavorite     = "favorite"
	RecoEventReadComplete = "read_complete"
)

type RecoRequestLog struct {
	RecRequestID   string    `json:"rec_request_id"`
	UserID         string    `json:"user_id"`
	SessionID      string    `json:"session_id"`
	Surface        string    `json:"surface"`
	Query          string    `json:"query"`
	Status         string    `json:"status"`
	ReturnedCount  int       `json:"returned_count"`
	CandidateCount int       `json:"candidate_count"`
	CreatedAt      time.Time `json:"created_at"`
}

type RecoEventLog struct {
	RecRequestID string         `json:"rec_request_id"`
	UserID       string         `json:"user_id"`
	SessionID    string         `json:"session_id"`
	Surface      string         `json:"surface"`
	ArticleID    string         `json:"article_id"`
	Rank         int            `json:"rank"`
	EventType    string         `json:"event_type"`
	EventTS      time.Time      `json:"event_ts"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type RecoEventRequest struct {
	Events []RecoEventLog `json:"events"`
}

type RecoEventResponse struct {
	Accepted int `json:"accepted"`
}

type RecoMetricValue struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Category    string  `json:"category"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit"`
	Description string  `json:"description"`
	Source      string  `json:"source"`
}

type RecoEvaluationSummary struct {
	Surface         string            `json:"surface"`
	Window          string            `json:"window"`
	WindowSeconds   int64             `json:"window_seconds"`
	GeneratedAt     time.Time         `json:"generated_at"`
	RequestCount    int64             `json:"request_count"`
	ImpressionCount int64             `json:"impression_count"`
	ClickCount      int64             `json:"click_count"`
	ConversionCount int64             `json:"conversion_count"`
	MetricValues    []RecoMetricValue `json:"metric_values"`
}
