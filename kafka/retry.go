package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"sea/metrics"
	"sea/zlog"

	"github.com/IBM/sarama"
	"go.uber.org/zap"
)

const maxRetryAttempts = 5

type RetryArticleSyncMessage struct {
	Payload         json.RawMessage `json:"payload"`
	Source          string          `json:"source"`
	RetryCount      int             `json:"retry_count"`
	LastError       string          `json:"last_error,omitempty"`
	NextAttemptUnix int64           `json:"next_attempt_unix"`
}

type RetryHandler func(ctx context.Context, event ArticleSyncEvent) error

func EnqueueArticleSyncRetry(ctx context.Context, event ArticleSyncEvent, source string, retryCount int, err error) error {
	payload, marshalErr := json.Marshal(event)
	if marshalErr != nil {
		return fmt.Errorf("marshal retry event failed: %w", marshalErr)
	}
	if producer == nil {
		return fmt.Errorf("kafka producer not initialized")
	}

	message := RetryArticleSyncMessage{
		Payload:         payload,
		Source:          source,
		RetryCount:      retryCount,
		NextAttemptUnix: time.Now().Add(retryBackoff(retryCount)).Unix(),
	}
	if err != nil {
		message.LastError = err.Error()
	}

	data, marshalErr := json.Marshal(message)
	if marshalErr != nil {
		return fmt.Errorf("marshal retry message failed: %w", marshalErr)
	}

	metrics.ArticleSyncRetryTotal.WithLabelValues(event.Op, source).Inc()
	return sendMessage(retryEndpoint().Topic, event.ArticleID, data)
}

type retryConsumerHandler struct {
	handler RetryHandler
}

func (h *retryConsumerHandler) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (h *retryConsumerHandler) Cleanup(sarama.ConsumerGroupSession) error { return nil }

func (h *retryConsumerHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for msg := range claim.Messages() {
		retryMsg, err := parseRetryMessage(msg.Value)
		if err != nil {
			zlog.L().Error("parse article sync retry message failed", zap.Error(err), zap.ByteString("value", msg.Value))
			session.MarkMessage(msg, "")
			continue
		}

		if err := waitUntilRetryReady(session.Context(), retryMsg.NextAttemptUnix); err != nil {
			return err
		}

		var event ArticleSyncEvent
		if err := json.Unmarshal(retryMsg.Payload, &event); err != nil {
			zlog.L().Error("unmarshal article sync retry payload failed", zap.Error(err), zap.ByteString("value", retryMsg.Payload))
			session.MarkMessage(msg, "")
			continue
		}

		if err := h.handler(session.Context(), event); err != nil {
			zlog.L().Error(
				"retry article sync failed",
				zap.Error(err),
				zap.String("article_id", event.ArticleID),
				zap.String("op", event.Op),
				zap.String("source", retryMsg.Source),
				zap.Int("retry_count", retryMsg.RetryCount),
			)
			if retryMsg.RetryCount+1 <= maxRetryAttempts {
				if enqueueErr := EnqueueArticleSyncRetry(session.Context(), event, retryMsg.Source, retryMsg.RetryCount+1, err); enqueueErr != nil {
					zlog.L().Error("requeue article sync retry failed", zap.Error(enqueueErr))
				}
			} else {
				metrics.ArticleSyncEventsTotal.WithLabelValues(event.Op, "error", "retry_exhausted").Inc()
				if resultErr := PublishSyncResult(session.Context(), ArticleSyncResult{
					EventScope:   ArticleSyncScope,
					EventID:      event.EventID,
					ArticleID:    event.ArticleID,
					Op:           event.Op,
					VersionMs:    event.VersionMs,
					Success:      false,
					ErrorMessage: err.Error(),
				}); resultErr != nil {
					zlog.L().Error("publish final article sync failure result failed", zap.Error(resultErr), zap.String("article_id", event.ArticleID))
				}
			}
		}

		session.MarkMessage(msg, "")
	}
	return nil
}

func StartRetry(ctx context.Context, handler RetryHandler) error {
	if retryConsumerGroup == nil {
		zlog.L().Warn("article sync retry consumer not initialized, skip start")
		return nil
	}

	h := &retryConsumerHandler{handler: handler}
	go func() {
		topic := retryEndpoint().Topic
		for {
			select {
			case <-ctx.Done():
				zlog.L().Info("article sync retry consumer stopped")
				return
			default:
				if err := retryConsumerGroup.Consume(ctx, []string{topic}, h); err != nil {
					zlog.L().Error("article sync retry consume failed", zap.Error(err))
					time.Sleep(5 * time.Second)
				}
			}
		}
	}()

	zlog.L().Info("article sync retry consumer started", zap.String("topic", retryEndpoint().Topic))
	return nil
}

func parseRetryMessage(data []byte) (RetryArticleSyncMessage, error) {
	var message RetryArticleSyncMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return message, fmt.Errorf("unmarshal retry message failed: %w", err)
	}
	if len(message.Payload) == 0 {
		return message, fmt.Errorf("retry payload is empty")
	}
	return message, nil
}

func waitUntilRetryReady(ctx context.Context, nextAttemptUnix int64) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		nowUnix := time.Now().Unix()
		if nowUnix < nextAttemptUnix {
			time.Sleep(time.Duration(nextAttemptUnix-nowUnix) * time.Second)
			continue
		}
		if activePrimaryJobs.Load() == 0 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func retryBackoff(retryCount int) time.Duration {
	if retryCount < 1 {
		retryCount = 1
	}
	backoff := time.Duration(retryCount*retryCount) * 5 * time.Second
	if backoff > 2*time.Minute {
		return 2 * time.Minute
	}
	return backoff
}
