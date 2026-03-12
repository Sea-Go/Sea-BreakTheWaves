package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"sea/config"
	"sea/zlog"

	"github.com/IBM/sarama"
	"go.uber.org/zap"
)

var consumerGroup sarama.ConsumerGroup

type ArticleHotEvent struct {
	ArticleID  string `json:"article_id"`
	ArticleTag string `json:"article_tag"`
	Content    string `json:"content,omitempty"`
	CoverUrl   string `json:"cover_url,omitempty"`
}

type MessageHandler func(ctx context.Context, event ArticleHotEvent) error

func Init() error {
	if config.Cfg.Kafka.Address == "" {
		zlog.L().Warn("Kafka 地址未配置，跳过 Consumer 初始化")
		return nil
	}

	cfg := sarama.NewConfig()
	cfg.Consumer.Return.Errors = true
	cfg.Consumer.Group.Rebalance.Strategy = sarama.BalanceStrategyRoundRobin
	cfg.Consumer.Offsets.Initial = sarama.OffsetNewest

	group, err := sarama.NewConsumerGroup(
		[]string{config.Cfg.Kafka.Address},
		config.Cfg.Kafka.Group,
		cfg,
	)
	if err != nil {
		zlog.L().Error("Kafka Consumer 初始化失败", zap.Error(err))
		return err
	}

	consumerGroup = group
	zlog.L().Info("Kafka Consumer 初始化成功",
		zap.String("address", config.Cfg.Kafka.Address),
		zap.String("topic", config.Cfg.Kafka.Topic),
		zap.String("group", config.Cfg.Kafka.Group),
	)
	return nil
}

func Close() error {
	if consumerGroup != nil {
		return consumerGroup.Close()
	}
	return nil
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
			zlog.L().Error("解析消息失败", zap.Error(err), zap.ByteString("value", msg.Value))
			continue
		}
		if err := h.handler(ctx, event); err != nil {
			zlog.L().Error("处理消息失败", zap.Error(err), zap.String("article_id", event.ArticleID))
		} else {
			session.MarkMessage(msg, "")
		}
	}
	return nil
}

func parseMessage(data []byte) (ArticleHotEvent, error) {
	var event ArticleHotEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return event, fmt.Errorf("JSON解析失败: %w", err)
	}
	if event.ArticleID == "" {
		return event, fmt.Errorf("article_id为空")
	}
	return event, nil
}

func Start(ctx context.Context, handler MessageHandler) error {
	if consumerGroup == nil {
		zlog.L().Warn("Kafka Consumer 未初始化，跳过启动")
		return nil
	}

	h := &consumerHandler{handler: handler}
	go func() {
		for {
			select {
			case <-ctx.Done():
				zlog.L().Info("Kafka Consumer 停止")
				return
			default:
				if err := consumerGroup.Consume(ctx, []string{config.Cfg.Kafka.Topic}, h); err != nil {
					zlog.L().Error("Kafka 消费错误", zap.Error(err))
					time.Sleep(time.Second * 5)
				}
			}
		}
	}()

	zlog.L().Info("Kafka Consumer 已启动", zap.String("topic", config.Cfg.Kafka.Topic))
	return nil
}
