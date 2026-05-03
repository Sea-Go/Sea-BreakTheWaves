package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"sea/config"
	"sea/metrics"
	"sea/zlog"

	"github.com/IBM/sarama"
	"go.uber.org/zap"
)

var (
	consumerGroup      sarama.ConsumerGroup
	retryConsumerGroup sarama.ConsumerGroup
	producer           sarama.SyncProducer
	activePrimaryJobs  atomic.Int64
)

const (
	ArticleSyncScope = "article_sync"

	ArticleSyncOpUpsert = "upsert"
	ArticleSyncOpDelete = "delete"
)

type ArticleSyncEvent struct {
	EventScope    string   `json:"event_scope"`
	EventID       string   `json:"event_id"`
	ArticleID     string   `json:"article_id"`
	Op            string   `json:"op"`
	Reason        string   `json:"reason"`
	AuthorID      string   `json:"author_id"`
	Status        string   `json:"status"`
	VersionMs     int64    `json:"version_ms"`
	Title         string   `json:"title,omitempty"`
	Brief         string   `json:"brief,omitempty"`
	CoverURL      string   `json:"cover_url,omitempty"`
	ManualTypeTag string   `json:"manual_type_tag,omitempty"`
	SecondaryTags []string `json:"secondary_tags,omitempty"`
	Markdown      string   `json:"markdown,omitempty"`
}

type ArticleSyncResult struct {
	EventScope   string `json:"event_scope"`
	EventID      string `json:"event_id"`
	ArticleID    string `json:"article_id"`
	Op           string `json:"op"`
	VersionMs    int64  `json:"version_ms"`
	Success      bool   `json:"success"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type MessageHandler func(ctx context.Context, event ArticleSyncEvent) error

type kafkaEndpoint struct {
	Address string
	Topic   string
	Group   string
}

func Init() error {
	primary := primaryEndpoint()
	if primary.Address == "" || primary.Topic == "" || primary.Group == "" {
		zlog.L().Warn("article sync kafka config incomplete, skip kafka init")
		return nil
	}

	consumerCfg := sarama.NewConfig()
	consumerCfg.Consumer.Return.Errors = true
	consumerCfg.Consumer.Group.Rebalance.Strategy = sarama.BalanceStrategyRoundRobin
	consumerCfg.Consumer.Offsets.Initial = sarama.OffsetNewest

	group, err := sarama.NewConsumerGroup([]string{primary.Address}, primary.Group, consumerCfg)
	if err != nil {
		zlog.L().Error("init article sync consumer failed", zap.Error(err))
		return err
	}
	consumerGroup = group

	retryCfg := retryEndpoint()
	retryGroup, err := sarama.NewConsumerGroup([]string{retryCfg.Address}, retryCfg.Group, consumerCfg)
	if err != nil {
		_ = consumerGroup.Close()
		consumerGroup = nil
		zlog.L().Error("init article sync retry consumer failed", zap.Error(err))
		return err
	}
	retryConsumerGroup = retryGroup

	producerCfg := sarama.NewConfig()
	producerCfg.Producer.Return.Successes = true
	producerCfg.Producer.Retry.Max = 3
	producerCfg.Producer.RequiredAcks = sarama.WaitForAll

	producerAddr := resultEndpoint().Address
	if producerAddr == "" {
		producerAddr = primary.Address
	}
	syncProducer, err := sarama.NewSyncProducer([]string{producerAddr}, producerCfg)
	if err != nil {
		_ = retryConsumerGroup.Close()
		_ = consumerGroup.Close()
		retryConsumerGroup = nil
		consumerGroup = nil
		zlog.L().Error("init article sync producer failed", zap.Error(err))
		return err
	}
	producer = syncProducer

	zlog.L().Info(
		"article sync kafka initialized",
		zap.String("address", primary.Address),
		zap.String("topic", primary.Topic),
		zap.String("group", primary.Group),
		zap.String("result_topic", resultEndpoint().Topic),
		zap.String("retry_topic", retryCfg.Topic),
		zap.String("retry_group", retryCfg.Group),
	)
	return nil
}

func Close() error {
	var firstErr error
	if consumerGroup != nil {
		if err := consumerGroup.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if retryConsumerGroup != nil {
		if err := retryConsumerGroup.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if producer != nil {
		if err := producer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type consumerHandler struct {
	handler MessageHandler
}

func (h *consumerHandler) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (h *consumerHandler) Cleanup(sarama.ConsumerGroupSession) error { return nil }

func (h *consumerHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for msg := range claim.Messages() {
		ctx := session.Context()
		event, err := parseMessage(msg.Value)
		if err != nil {
			metrics.ArticleSyncEventsTotal.WithLabelValues("invalid", "error", "primary").Inc()
			zlog.L().Error("parse article sync event failed", zap.Error(err), zap.ByteString("value", msg.Value))
			session.MarkMessage(msg, "")
			continue
		}

		activePrimaryJobs.Add(1)
		err = h.handler(ctx, event)
		activePrimaryJobs.Add(-1)

		if err != nil {
			metrics.ArticleSyncEventsTotal.WithLabelValues(event.Op, "error", "primary").Inc()
			zlog.L().Error("handle article sync event failed", zap.Error(err), zap.String("article_id", event.ArticleID), zap.String("op", event.Op))
		}
		session.MarkMessage(msg, "")
	}
	return nil
}

func parseMessage(data []byte) (ArticleSyncEvent, error) {
	var event ArticleSyncEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return event, fmt.Errorf("unmarshal article sync event failed: %w", err)
	}
	if strings.TrimSpace(event.ArticleID) == "" {
		return event, fmt.Errorf("article_id is empty")
	}
	if strings.TrimSpace(event.Op) == "" {
		return event, fmt.Errorf("op is empty")
	}
	if strings.TrimSpace(event.EventScope) == "" {
		event.EventScope = ArticleSyncScope
	}
	return event, nil
}

func Start(ctx context.Context, handler MessageHandler) error {
	if consumerGroup == nil {
		zlog.L().Warn("article sync consumer not initialized, skip start")
		return nil
	}

	h := &consumerHandler{handler: handler}
	go func() {
		topic := primaryEndpoint().Topic
		for {
			select {
			case <-ctx.Done():
				zlog.L().Info("article sync consumer stopped")
				return
			default:
				if err := consumerGroup.Consume(ctx, []string{topic}, h); err != nil {
					zlog.L().Error("article sync consume failed", zap.Error(err))
					time.Sleep(5 * time.Second)
				}
			}
		}
	}()

	zlog.L().Info("article sync consumer started", zap.String("topic", primaryEndpoint().Topic))
	return nil
}

func PublishSyncResult(ctx context.Context, result ArticleSyncResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return sendMessage(resultEndpoint().Topic, result.ArticleID, data)
}

func sendMessage(topic, key string, payload []byte) error {
	if producer == nil {
		return fmt.Errorf("kafka producer not initialized")
	}
	if strings.TrimSpace(topic) == "" {
		return fmt.Errorf("kafka topic is empty")
	}

	msg := &sarama.ProducerMessage{
		Topic: topic,
		Value: sarama.ByteEncoder(payload),
	}
	if strings.TrimSpace(key) != "" {
		msg.Key = sarama.StringEncoder(key)
	}

	_, _, err := producer.SendMessage(msg)
	return err
}

func primaryEndpoint() kafkaEndpoint {
	address := strings.TrimSpace(config.Cfg.ArticleSyncKafka.Address)
	topic := strings.TrimSpace(config.Cfg.ArticleSyncKafka.Topic)
	group := strings.TrimSpace(config.Cfg.ArticleSyncKafka.Group)
	if address == "" {
		address = strings.TrimSpace(config.Cfg.Kafka.Address)
	}
	if topic == "" {
		topic = strings.TrimSpace(config.Cfg.Kafka.Topic)
		if topic == "" {
			topic = "article-sync-events"
		}
	}
	if group == "" {
		group = strings.TrimSpace(config.Cfg.Kafka.Group)
		if group == "" {
			group = "sea-breakthewaves-sync"
		}
	}
	return kafkaEndpoint{Address: address, Topic: topic, Group: group}
}

func resultEndpoint() kafkaEndpoint {
	address := strings.TrimSpace(config.Cfg.ArticleSyncResultKafka.Address)
	topic := strings.TrimSpace(config.Cfg.ArticleSyncResultKafka.Topic)
	if address == "" {
		address = primaryEndpoint().Address
	}
	if topic == "" {
		topic = "article-sync-results"
	}
	return kafkaEndpoint{Address: address, Topic: topic}
}

func retryEndpoint() kafkaEndpoint {
	address := strings.TrimSpace(config.Cfg.ArticleSyncRetryKafka.Address)
	topic := strings.TrimSpace(config.Cfg.ArticleSyncRetryKafka.Topic)
	group := strings.TrimSpace(config.Cfg.ArticleSyncRetryKafka.Group)
	if address == "" {
		address = primaryEndpoint().Address
	}
	if topic == "" {
		topic = strings.TrimSpace(config.Cfg.Kafka.RetryTopic)
		if topic == "" {
			topic = "article-sync-retry"
		}
	}
	if group == "" {
		group = strings.TrimSpace(config.Cfg.Kafka.RetryGroup)
		if group == "" {
			group = "sea-breakthewaves-sync-retry"
		}
	}
	return kafkaEndpoint{Address: address, Topic: topic, Group: group}
}
