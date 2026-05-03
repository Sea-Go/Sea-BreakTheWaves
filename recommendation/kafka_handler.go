package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"sea/kafka"
	"sea/metrics"
	docingest "sea/skills/doc_ingest"
	"sea/skillsys"
	"sea/storage"
	"sea/zlog"

	"go.uber.org/zap"
)

func createKafkaMessageHandler(reg *skillsys.Registry, articleRepo *storage.ArticleRepo) kafka.MessageHandler {
	return func(ctx context.Context, event kafka.ArticleSyncEvent) error {
		if err := processArticleSyncEvent(ctx, reg, articleRepo, event); err != nil {
			if retryErr := kafka.EnqueueArticleSyncRetry(ctx, event, "primary", 1, err); retryErr != nil {
				zlog.L().Error("enqueue article sync retry failed", zap.Error(retryErr), zap.String("article_id", event.ArticleID), zap.String("op", event.Op))
			}
			return err
		}

		if err := kafka.PublishSyncResult(ctx, kafka.ArticleSyncResult{
			EventScope: kafka.ArticleSyncScope,
			EventID:    event.EventID,
			ArticleID:  event.ArticleID,
			Op:         event.Op,
			VersionMs:  event.VersionMs,
			Success:    true,
		}); err != nil {
			zlog.L().Error("publish article sync result failed", zap.Error(err), zap.String("article_id", event.ArticleID), zap.String("op", event.Op))
			return err
		}

		metrics.ArticleSyncEventsTotal.WithLabelValues(event.Op, "ok", "primary").Inc()
		return nil
	}
}

func createKafkaRetryHandler(reg *skillsys.Registry, articleRepo *storage.ArticleRepo) kafka.RetryHandler {
	return func(ctx context.Context, event kafka.ArticleSyncEvent) error {
		if err := processArticleSyncEvent(ctx, reg, articleRepo, event); err != nil {
			return err
		}

		if err := kafka.PublishSyncResult(ctx, kafka.ArticleSyncResult{
			EventScope: kafka.ArticleSyncScope,
			EventID:    event.EventID,
			ArticleID:  event.ArticleID,
			Op:         event.Op,
			VersionMs:  event.VersionMs,
			Success:    true,
		}); err != nil {
			return err
		}
		metrics.ArticleSyncEventsTotal.WithLabelValues(event.Op, "ok", "retry").Inc()
		return nil
	}
}

func processArticleSyncEvent(ctx context.Context, reg *skillsys.Registry, articleRepo *storage.ArticleRepo, event kafka.ArticleSyncEvent) error {
	switch strings.TrimSpace(event.Op) {
	case kafka.ArticleSyncOpUpsert:
		argsRaw, err := buildIngestArgs(event)
		if err != nil {
			return err
		}
		if _, _, err := reg.Invoke(ctx, "doc_ingest", argsRaw); err != nil {
			return fmt.Errorf("doc ingest failed: %w", err)
		}
		zlog.L().Info("article sync upsert applied", zap.String("article_id", event.ArticleID), zap.Int64("version_ms", event.VersionMs))
		return nil
	case kafka.ArticleSyncOpDelete:
		if err := docingest.DeleteArticleState(ctx, articleRepo, event.ArticleID); err != nil {
			return fmt.Errorf("delete article state failed: %w", err)
		}
		zlog.L().Info("article sync delete applied", zap.String("article_id", event.ArticleID), zap.Int64("version_ms", event.VersionMs))
		return nil
	default:
		return fmt.Errorf("unsupported article sync op: %s", event.Op)
	}
}

func buildIngestArgs(event kafka.ArticleSyncEvent) (json.RawMessage, error) {
	args := map[string]any{
		"article_id": event.ArticleID,
		"score":      0.5,
		"markdown":   event.Markdown,
		"title":      event.Title,
		"cover":      event.CoverURL,
		"tags":       append([]string(nil), event.SecondaryTags...),
	}
	if strings.TrimSpace(event.ManualTypeTag) != "" {
		args["type_tags"] = []string{strings.TrimSpace(event.ManualTypeTag)}
	}

	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal ingest args failed: %w", err)
	}
	return raw, nil
}
