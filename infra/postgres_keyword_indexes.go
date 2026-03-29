package infra

import (
	"context"
	"database/sql"

	"sea/zlog"

	"go.uber.org/zap"
)

func ensureKeywordSearchIndexes(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS pg_trgm;`,
		`CREATE INDEX IF NOT EXISTS idx_articles_title_trgm
			ON articles
			USING gin (LOWER(title) gin_trgm_ops);`,
		`CREATE INDEX IF NOT EXISTS idx_articles_type_tags_trgm
			ON articles
			USING gin (LOWER(type_tags) gin_trgm_ops);`,
		`CREATE INDEX IF NOT EXISTS idx_articles_tags_trgm
			ON articles
			USING gin (LOWER(tags) gin_trgm_ops);`,
	}
	return execIndexStatements(ctx, db, stmts)
}

func ensureSourceKeywordSearchIndexes(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS pg_trgm;`,
		`CREATE INDEX IF NOT EXISTS idx_source_article_title_trgm_published
			ON article
			USING gin (LOWER(title) gin_trgm_ops)
			WHERE status = 2 AND deleted_at IS NULL;`,
		`CREATE INDEX IF NOT EXISTS idx_source_article_author_created_published
			ON article(author_id, created_at DESC, id DESC)
			WHERE status = 2 AND deleted_at IS NULL;`,
		`CREATE INDEX IF NOT EXISTS idx_source_users_username_trgm
			ON users
			USING gin (LOWER(username) gin_trgm_ops);`,
	}
	return execIndexStatements(ctx, db, stmts)
}

func execIndexStatements(ctx context.Context, db *sql.DB, stmts []string) error {
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			zlog.L().Warn("ensure postgres index failed", zap.Error(err), zap.String("sql", stmt))
			return err
		}
	}
	return nil
}
