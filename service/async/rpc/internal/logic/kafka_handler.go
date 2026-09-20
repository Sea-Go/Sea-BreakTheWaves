package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	docingest "sea/service/async/rpc/internal/knowledge/doc_ingest"
	"sea/service/async/rpc/internal/model"
	model "sea/service/async/rpc/internal/model"
	"sea/service/async/rpc/internal/mqs/kafka"
	"sea/service/common/infra"
	"sea/service/common/logx"
	"sea/service/common/metricx"
	"sea/service/common/skillsys"

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

// StartWorkers owns article-sync consumption and retry execution. The API
// process calls it during startup; all state below is owned by Async.
func StartWorkers(ctx context.Context) (func() error, error) {
	if err := infra.PostgresInit(); err != nil {
		return nil, fmt.Errorf("async postgres init failed: %w", err)
	}
	if err := infra.MilvusInit(); err != nil {
		zlog.L().Warn("async milvus init failed", zap.Error(err))
	}
	if err := infra.Neo4jInit(); err != nil {
		zlog.L().Warn("async neo4j init failed", zap.Error(err))
	}

	registry := skillsys.NewRegistry()
	articleRepo := model.NewArticleRepo(infra.Postgres())
	registry.Register(docingest.New(articleRepo, infra.NewAIClient()))
	if err := kafka.Init(); err != nil {
		return nil, err
	}
	if err := kafka.Start(ctx, createKafkaMessageHandler(registry, articleRepo)); err != nil {
		_ = kafka.Close()
		return nil, err
	}
	if err := kafka.StartRetry(ctx, createKafkaRetryHandler(registry, articleRepo)); err != nil {
		_ = kafka.Close()
		return nil, err
	}
	return kafka.Close, nil
}
