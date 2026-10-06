package content

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/IBM/sarama"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/analytics"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/google/uuid"
	"time"
)

type contentConsumer struct{ s *Service }

func (contentConsumer) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (contentConsumer) Cleanup(sarama.ConsumerGroupSession) error { return nil }
func (h contentConsumer) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for message := range claim.Messages() {
		var input item.ContentEvent
		err := json.Unmarshal(message.Value, &input)
		if err == nil {
			_, err = h.s.Accept(session.Context(), input, nil)
		}
		if err != nil {
			if errors.Is(err, item.ErrInvalid) || errors.Is(err, item.ErrConflict) || !json.Valid(message.Value) {
				if e := h.s.reject(session.Context(), message, err); e != nil {
					return e
				}
			} else {
				return err
			}
		}
		session.MarkMessage(message, "")
	}
	return nil
}
func (s *Service) Consume(ctx context.Context, brokers []string, topic, group string) error {
	cfg := sarama.NewConfig()
	cfg.Consumer.Offsets.Initial = sarama.OffsetOldest
	cfg.Consumer.Return.Errors = false
	g, e := sarama.NewConsumerGroup(brokers, group, cfg)
	if e != nil {
		return e
	}
	defer g.Close()
	for ctx.Err() == nil {
		if e = g.Consume(ctx, []string{topic}, contentConsumer{s}); e != nil {
			return e
		}
	}
	return ctx.Err()
}
func (s *Service) ExportBehavior(ctx context.Context, brokers []string, topic string) (int, error) {
	if len(brokers) == 0 || topic == "" {
		return 0, item.ErrInvalid
	}
	cfg := sarama.NewConfig()
	cfg.Producer.Return.Successes = true
	cfg.Producer.RequiredAcks = sarama.WaitForAll
	cfg.Net.DialTimeout = 5 * time.Second
	producer, e := sarama.NewSyncProducer(brokers, cfg)
	if e != nil {
		return 0, e
	}
	defer producer.Close()
	rows, e := s.store.PendingBehavior(ctx, 500)
	if e != nil {
		return 0, e
	}
	ids := []string{}
	for _, r := range rows {
		if e = ctx.Err(); e != nil {
			return len(ids), e
		}
		_, _, e = producer.SendMessage(&sarama.ProducerMessage{Topic: topic, Key: sarama.StringEncoder(r.RequestID), Value: sarama.StringEncoder(r.Payload)})
		if e != nil {
			return len(ids), e
		}
		ids = append(ids, r.EventID)
	}
	return len(ids), s.store.Exported(ctx, ids)
}

type behaviorConsumer struct{ sink *analytics.ClickHouse }

func (behaviorConsumer) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (behaviorConsumer) Cleanup(sarama.ConsumerGroupSession) error { return nil }
func (h behaviorConsumer) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	events := []item.Behavior{}
	messages := []*sarama.ConsumerMessage{}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	flush := func() error {
		if len(events) == 0 {
			return nil
		}
		if e := h.sink.Write(session.Context(), events); e != nil {
			return e
		}
		for _, m := range messages {
			session.MarkMessage(m, "")
		}
		events = nil
		messages = nil
		return nil
	}
	for {
		select {
		case <-session.Context().Done():
			return session.Context().Err()
		case <-ticker.C:
			if e := flush(); e != nil {
				return e
			}
		case message, ok := <-claim.Messages():
			if !ok {
				return flush()
			}
			var event item.Behavior
			if e := json.Unmarshal(message.Value, &event); e != nil {
				return e
			}
			events = append(events, event)
			messages = append(messages, message)
			if len(events) >= 500 {
				if e := flush(); e != nil {
					return e
				}
			}
		}
	}
}
func ConsumeBehavior(ctx context.Context, brokers []string, topic, group string, sink *analytics.ClickHouse) error {
	if sink == nil {
		return item.ErrInvalid
	}
	if e := sink.Migrate(ctx); e != nil {
		return e
	}
	cfg := sarama.NewConfig()
	cfg.Consumer.Offsets.Initial = sarama.OffsetOldest
	g, e := sarama.NewConsumerGroup(brokers, group, cfg)
	if e != nil {
		return e
	}
	defer g.Close()
	for ctx.Err() == nil {
		if e = g.Consume(ctx, []string{topic}, behaviorConsumer{sink}); e != nil && !errors.Is(e, context.Canceled) {
			return e
		}
	}
	return ctx.Err()
}

func (s *Service) reject(ctx context.Context, message *sarama.ConsumerMessage, cause error) error {
	key := "rejected/" + uuid.NewString()
	if _, e := s.objects.Put(ctx, key, bytes.NewReader(message.Value), int64(len(message.Value)), "application/json"); e != nil {
		return e
	}
	id := fmt.Sprintf("%s/%d/%d", message.Topic, message.Partition, message.Offset)
	return s.store.Reject(ctx, id, key, cause.Error())
}
