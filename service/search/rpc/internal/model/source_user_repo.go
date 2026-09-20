package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type SourceUserRepo struct {
	db *sql.DB
}

type SourceAuthorSearchRecord struct {
	AuthorID           string
	AuthorName         string
	ArticleCount       int
	LatestArticleID    string
	LatestArticleTitle string
	LatestArticleTime  time.Time
}

func NewSourceUserRepo(db *sql.DB) *SourceUserRepo {
	if db == nil {
		return nil
	}
	return &SourceUserRepo{db: db}
}

func (r *SourceUserRepo) SearchAuthorsByName(ctx context.Context, query string, limit int) ([]SourceAuthorSearchRecord, error) {
	if r == nil || r.db == nil {
		return nil, sql.ErrConnDone
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return []SourceAuthorSearchRecord{}, nil
	}
	if limit <= 0 {
		limit = 10
	}

	rows, err := r.db.QueryContext(ctx, `
		WITH matched_authors AS (
			SELECT
				a.author_id,
				u.username,
				a.id AS latest_article_id,
				a.title AS latest_article_title,
				a.created_at AS latest_article_time,
				COUNT(*) OVER (PARTITION BY a.author_id) AS article_count,
				CASE
					WHEN LOWER(u.username) = LOWER($1) THEN 0
					WHEN LOWER(u.username) LIKE LOWER($1) || '%' THEN 1
					ELSE 2
				END AS match_rank,
				ROW_NUMBER() OVER (PARTITION BY a.author_id ORDER BY a.created_at DESC, a.id DESC) AS latest_rank
			FROM article a
			INNER JOIN users u ON a.author_id = u.uid::text
			WHERE a.status = 2
			  AND a.deleted_at IS NULL
			  AND LOWER(u.username) LIKE '%' || LOWER($1) || '%'
		)
		SELECT
			author_id,
			username,
			article_count,
			latest_article_id,
			latest_article_title,
			latest_article_time
		FROM matched_authors
		WHERE latest_rank = 1
		ORDER BY match_rank ASC, article_count DESC, latest_article_time DESC, author_id ASC
		LIMIT $2
	`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]SourceAuthorSearchRecord, 0, limit)
	for rows.Next() {
		var record SourceAuthorSearchRecord
		if err := rows.Scan(
			&record.AuthorID,
			&record.AuthorName,
			&record.ArticleCount,
			&record.LatestArticleID,
			&record.LatestArticleTitle,
			&record.LatestArticleTime,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}

	return records, rows.Err()
}
