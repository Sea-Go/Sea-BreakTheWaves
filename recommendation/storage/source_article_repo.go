package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

type SourceArticleRepo struct {
	db *sql.DB
}

type SourceArticleSearchRecord struct {
	ArticleID     string
	Title         string
	Brief         string
	Cover         string
	ManualTypeTag string
	SecondaryTags []string
	AuthorID      string
	AuthorName    string
	CreatedAt     time.Time
}

func NewSourceArticleRepo(db *sql.DB) *SourceArticleRepo {
	if db == nil {
		return nil
	}
	return &SourceArticleRepo{db: db}
}

func (r *SourceArticleRepo) SearchPublishedByTitle(ctx context.Context, query string, limit int) ([]SourceArticleSearchRecord, error) {
	if r == nil || r.db == nil {
		return nil, sql.ErrConnDone
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return []SourceArticleSearchRecord{}, nil
	}
	if limit <= 0 {
		limit = 10
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT
			a.id,
			a.title,
			COALESCE(a.brief, ''),
			COALESCE(a.cover_image_url, ''),
			COALESCE(a.manual_type_tag, ''),
			COALESCE(a.secondary_tags::text, '[]'),
			COALESCE(a.author_id, ''),
			COALESCE(u.username, ''),
			a.created_at
		FROM article a
		LEFT JOIN users u ON a.author_id = u.uid::text
		WHERE a.status = 2
		  AND a.deleted_at IS NULL
		  AND LOWER(a.title) LIKE '%' || LOWER($1) || '%'
		ORDER BY
			CASE
				WHEN LOWER(a.title) = LOWER($1) THEN 0
				WHEN LOWER(a.title) LIKE LOWER($1) || '%' THEN 1
				ELSE 2
			END,
			a.created_at DESC,
			a.id DESC
		LIMIT $2
	`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]SourceArticleSearchRecord, 0, limit)
	for rows.Next() {
		var record SourceArticleSearchRecord
		var secondaryTagsJSON string
		if err := rows.Scan(
			&record.ArticleID,
			&record.Title,
			&record.Brief,
			&record.Cover,
			&record.ManualTypeTag,
			&secondaryTagsJSON,
			&record.AuthorID,
			&record.AuthorName,
			&record.CreatedAt,
		); err != nil {
			return nil, err
		}
		record.SecondaryTags = parseJSONTextArray(secondaryTagsJSON)
		records = append(records, record)
	}

	return records, rows.Err()
}

func parseJSONTextArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}

	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return []string{}
	}

	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}
