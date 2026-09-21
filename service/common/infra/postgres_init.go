package infra

import (
	"context"
	"database/sql"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/logx"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

var pgDB *sql.DB
var pgORM *gorm.DB

func Postgres() *sql.DB {
	return pgDB
}

func PostgresORM() *gorm.DB {
	return pgORM
}

func PostgresInit() error {
	orm, err := database.Open(context.Background(), database.Config{
		DSN:                    config.Cfg.Postgres.DSN,
		MaxOpenConns:           config.Cfg.Postgres.MaxOpenConns,
		MaxIdleConns:           config.Cfg.Postgres.MaxIdleConns,
		ConnMaxLifetimeSeconds: config.Cfg.Postgres.ConnMaxLifetimeSeconds,
	})
	if err != nil {
		zlog.L().Error("postgres connect failed", zap.Error(err))
		return err
	}
	sqlDB, err := orm.DB()
	if err != nil {
		zlog.L().Error("postgres sql handle failed", zap.Error(err))
		return err
	}
	ctx := context.Background()
	pgDB = sqlDB
	pgORM = orm

	if err := ensurePGSchema(ctx, orm); err != nil {
		return err
	}

	indexCtx, indexCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer indexCancel()
	if err := ensureKeywordSearchIndexes(indexCtx, orm); err != nil {
		zlog.L().Warn("ensure local keyword search indexes failed", zap.Error(err))
	}

	zlog.L().Info("postgres initialized")
	return nil
}

func ensurePGSchema(ctx context.Context, db *gorm.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS articles (
			article_id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			cover TEXT,
			type_tags TEXT,
			tags TEXT,
			score REAL NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
		`CREATE TABLE IF NOT EXISTS article_chunks (
			chunk_id TEXT PRIMARY KEY,
			article_id TEXT NOT NULL REFERENCES articles(article_id) ON DELETE CASCADE,
			h2 TEXT,
			content TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
		`CREATE TABLE IF NOT EXISTS user_pool_items (
			user_id TEXT NOT NULL,
			pool_type TEXT NOT NULL,
			period_bucket TEXT NOT NULL DEFAULT '',
			article_id TEXT NOT NULL REFERENCES articles(article_id) ON DELETE CASCADE,
			score REAL NOT NULL DEFAULT 0,
			similarity REAL NOT NULL DEFAULT 0,
			remark_score REAL NOT NULL DEFAULT 0,
			inserted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (user_id, pool_type, period_bucket, article_id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_user_pool_items_user_type ON user_pool_items(user_id, pool_type, period_bucket);`,
		`CREATE TABLE IF NOT EXISTS user_rec_history (
			history_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			article_id TEXT NOT NULL REFERENCES articles(article_id) ON DELETE CASCADE,
			clicked BOOLEAN NOT NULL DEFAULT false,
			preference REAL NOT NULL DEFAULT 0,
			ts TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
		`ALTER TABLE user_rec_history ADD COLUMN IF NOT EXISTS history_id TEXT;`,
		`UPDATE user_rec_history
			SET history_id = user_id || '|' || (EXTRACT(EPOCH FROM ts) * 1000000000)::bigint || '|' || article_id
			WHERE history_id IS NULL OR history_id = '';`,
		`CREATE INDEX IF NOT EXISTS idx_user_rec_history_user_ts ON user_rec_history(user_id, ts DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_user_rec_history_history_id ON user_rec_history(history_id);`,
		`CREATE INDEX IF NOT EXISTS idx_user_rec_history_user_article ON user_rec_history(user_id, article_id);`,
		`CREATE TABLE IF NOT EXISTS user_memory (
			user_id TEXT NOT NULL,
			memory_type TEXT NOT NULL,
			period_bucket TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (user_id, memory_type, period_bucket)
		);`,
		`CREATE TABLE IF NOT EXISTS user_memory_chunks (
			user_id TEXT NOT NULL,
			memory_type TEXT NOT NULL,
			period_bucket TEXT NOT NULL DEFAULT '',
			chunk_index INT NOT NULL,
			content TEXT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (user_id, memory_type, period_bucket, chunk_index)
		);`,
		`CREATE TABLE IF NOT EXISTS reco_request_logs (
			rec_request_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			surface TEXT NOT NULL DEFAULT 'home_feed',
			query TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			returned_count INT NOT NULL DEFAULT 0,
			candidate_count INT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_reco_request_logs_surface_created
			ON reco_request_logs(surface, created_at DESC);`,
		`CREATE TABLE IF NOT EXISTS reco_event_logs (
			id BIGSERIAL PRIMARY KEY,
			rec_request_id TEXT NOT NULL DEFAULT '',
			user_id TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			surface TEXT NOT NULL DEFAULT 'home_feed',
			article_id TEXT NOT NULL DEFAULT '',
			rank INT NOT NULL DEFAULT 0,
			event_type TEXT NOT NULL,
			event_ts TIMESTAMPTZ NOT NULL DEFAULT now(),
			metadata JSONB NOT NULL DEFAULT '{}'::jsonb
		);`,
		`CREATE INDEX IF NOT EXISTS idx_reco_event_logs_surface_ts
			ON reco_event_logs(surface, event_ts DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_reco_event_logs_request_article
			ON reco_event_logs(rec_request_id, article_id);`,
		`CREATE TABLE IF NOT EXISTS reco_eval_cases (
			case_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL DEFAULT '',
			query TEXT NOT NULL DEFAULT '',
			surface TEXT NOT NULL DEFAULT 'dashboard_recommend',
			period_bucket TEXT NOT NULL DEFAULT 'd1',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
		`CREATE TABLE IF NOT EXISTS reco_eval_labels (
			case_id TEXT NOT NULL REFERENCES reco_eval_cases(case_id) ON DELETE CASCADE,
			article_id TEXT NOT NULL,
			relevance REAL NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY(case_id, article_id)
		);`,
		`CREATE TABLE IF NOT EXISTS reco_eval_runs (
			run_id TEXT PRIMARY KEY,
			case_count INT NOT NULL DEFAULT 0,
			metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
	}

	for _, s := range stmts {
		if err := db.WithContext(ctx).Exec(s).Error; err != nil {
			zlog.L().Error("ensure postgres schema failed", zap.Error(err), zap.String("sql", s))
			return err
		}
	}
	return nil
}
