package main

import (
	"context"
	"encoding/json"
	"fmt"

	"sea/kafka"
	"sea/skillsys"
	"sea/zlog"

	"go.uber.org/zap"
)

func createKafkaMessageHandler(reg *skillsys.Registry) kafka.MessageHandler {
	return func(ctx context.Context, event kafka.ArticleHotEvent) error {
		zlog.L().Info("收到文章事件",
			zap.String("article_id", event.ArticleID),
			zap.String("article_tag", event.ArticleTag),
			zap.String("cover_url", event.CoverUrl),
		)

		ingestArgs := map[string]any{
			"article_id": event.ArticleID,
			"score":      0.5,
		}

		articleData := map[string]any{
			"article_id": event.ArticleID,
			"title":      event.ArticleID,
		}

		if event.Content != "" {
			articleData["sections"] = []map[string]any{
				{
					"h2": "正文",
					"blocks": []map[string]any{
						{"type": "text", "text": event.Content},
					},
				},
			}
		}

		if event.ArticleTag != "" {
			articleData["type_tags"] = []string{event.ArticleTag}
		}

		if event.CoverUrl != "" {
			articleData["cover"] = event.CoverUrl
		}

		articleJSON, err := json.Marshal(articleData)
		if err != nil {
			return fmt.Errorf("序列化文章数据失败: %w", err)
		}
		ingestArgs["article_json"] = string(articleJSON)

		argsRaw, err := json.Marshal(ingestArgs)
		if err != nil {
			return fmt.Errorf("序列化参数失败: %w", err)
		}

		_, _, err = reg.Invoke(ctx, "doc_ingest", argsRaw)
		if err != nil {
			return fmt.Errorf("文档入库失败: %w", err)
		}

		zlog.L().Info("文章入库成功", zap.String("article_id", event.ArticleID))
		return nil
	}
}
