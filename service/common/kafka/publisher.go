package kafka

import (
	"encoding/json"

	"context"
	"fmt"
	"sync"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"

	"github.com/IBM/sarama"
)

var (
	publisherOnce sync.Once
	publisher     sarama.SyncProducer
	publisherErr  error
)

func ensurePublisher(endpoint config.KafkaEndpointConfig) (sarama.SyncProducer, error) {
	if endpoint.Address == "" {
		return nil, fmt.Errorf("kafka address is empty")
	}
	publisherOnce.Do(func() {
		cfg := sarama.NewConfig()
		cfg.Producer.Return.Successes = true
		cfg.Producer.Retry.Max = 3
		cfg.Producer.RequiredAcks = sarama.WaitForAll
		publisher, publisherErr = sarama.NewSyncProducer([]string{endpoint.Address}, cfg)
	})
	return publisher, publisherErr
}

// PublishJSON publishes a versioned JSON payload using the supplied endpoint.
// It intentionally owns no business envelope; callers provide the contract.
func PublishJSON(ctx context.Context, endpoint config.KafkaEndpointConfig, key string, payload any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	producer, err := ensurePublisher(endpoint)
	if err != nil {
		return err
	}
	value, err := jsonMarshal(payload)
	if err != nil {
		return err
	}
	msg := &sarama.ProducerMessage{Topic: endpoint.Topic, Key: sarama.StringEncoder(key), Value: sarama.ByteEncoder(value)}
	if _, _, err := producer.SendMessage(msg); err != nil {
		return fmt.Errorf("publish kafka message failed: %w", err)
	}
	return nil
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}
