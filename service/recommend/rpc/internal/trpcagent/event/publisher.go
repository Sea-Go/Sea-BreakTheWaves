package event

import (
	"context"
	"fmt"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"
	commonkafka "github.com/Sea-Go/Sea-BreakTheWaves/service/common/kafka"
)

const Scope = "recommendation_v2"

type Message struct {
	EventScope string         `json:"event_scope"`
	EventID    string         `json:"event_id"`
	EventType  string         `json:"event_type"`
	RequestID  string         `json:"request_id,omitempty"`
	TraceID    string         `json:"trace_id,omitempty"`
	TenantID   string         `json:"tenant_id,omitempty"`
	UserID     string         `json:"user_id,omitempty"`
	ArticleID  string         `json:"article_id,omitempty"`
	Rank       int            `json:"rank,omitempty"`
	Channel    string         `json:"channel,omitempty"`
	PathTaken  string         `json:"path_taken,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
	Payload    map[string]any `json:"payload,omitempty"`
}

// PublishRecommendationEvent keeps the legacy topic payload while removing the
// direct dependency from recommendation code on the async service internals.
func PublishRecommendationEvent(ctx context.Context, msg Message) error {
	if msg.EventScope == "" {
		msg.EventScope = Scope
	}
	key := msg.EventID
	if key == "" {
		key = msg.RequestID
	}
	err := commonkafka.PublishJSON(ctx, config.Cfg.RecommendationEventKafka, key, msg)
	if err != nil {
		return fmt.Errorf("publish recommendation event failed: %w", err)
	}
	return nil
}
