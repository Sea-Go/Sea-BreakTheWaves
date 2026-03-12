package infra

import (
	"context"
	"database/sql"
	"time"

	"sea/config"
	"sea/zlog"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"
)

var pgDB *sql.DB

// Postgres 返回全局 Postgres 连接池（在 PostgresInit 成功后可用）。
func Postgres() *sql.DB {
	return pgDB
}

// PostgresInit 初始化 Postgres：连接池 + 必要表结构（用于 demo / 本地快速启动）。
// 生产环境建议把建表迁移交给 migration 工具，这里保留“开箱即用”能力。
func PostgresInit() error {
	db, err := sql.Open("pgx", config.Cfg.Postgres.DSN)
	if err != nil {
		zlog.L().Error("连接 Postgres 失败", zap.Error(err))
		return err
	}

	if config.Cfg.Postgres.MaxOpenConns > 0 {
		db.SetMaxOpenConns(config.Cfg.Postgres.MaxOpenConns)
	}
	if config.Cfg.Postgres.MaxIdleConns > 0 {
		db.SetMaxIdleConns(config.Cfg.Postgres.MaxIdleConns)
	}
	if config.Cfg.Postgres.ConnMaxLifetimeSeconds > 0 {
		db.SetConnMaxLifetime(time.Duration(config.Cfg.Postgres.ConnMaxLifetimeSeconds) * time.Second)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		zlog.L().Error("Postgres Ping 失败", zap.Error(err))
		return err
	}

	pgDB = db

	if err := ensurePGSchema(ctx, db); err != nil {
		return err
	}

	zlog.L().Info("Postgres 初始化完成")
	return nil
}

func ensurePGSchema(ctx context.Context, db *sql.DB) error {
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
		// 兼容：若旧表没有 history_id，则补列并回填，避免 Scan NULL。
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
			-- 向量索引统一由 Milvus 负责；PG 仅保留内容与可审计字段
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (user_id, memory_type, period_bucket, chunk_index)
		);`,
	}

	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			zlog.L().Error("初始化表结构失败", zap.Error(err), zap.String("sql", s))
			return err
		}
	}
	return nil
}
