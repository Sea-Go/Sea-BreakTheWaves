package storage

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

type SourceLikeRepo struct {
	db *sql.DB
}

func NewSourceLikeRepo(db *sql.DB) *SourceLikeRepo {
	if db == nil {
		return nil
	}
	return &SourceLikeRepo{db: db}
}

func (r *SourceLikeRepo) ListDislikedArticleIDs(ctx context.Context, userID string, limit int) ([]string, error) {
	if r == nil || r.db == nil {
		return nil, sql.ErrConnDone
	}

	parsedUserID, err := strconv.ParseInt(strings.TrimSpace(userID), 10, 64)
	if err != nil || parsedUserID <= 0 {
		return []string{}, nil
	}
	if limit <= 0 {
		limit = 1000
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT target_id
		FROM like_record
		WHERE user_id = $1
		  AND target_type = 'article'
		  AND state = 2
		ORDER BY updated_at DESC, id DESC
		LIMIT $2
	`, parsedUserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	for rows.Next() {
		var articleID string
		if err := rows.Scan(&articleID); err != nil {
			return nil, err
		}
		articleID = strings.TrimSpace(articleID)
		if articleID == "" {
			continue
		}
		if _, ok := seen[articleID]; ok {
			continue
		}
		seen[articleID] = struct{}{}
		out = append(out, articleID)
	}

	return out, rows.Err()
}
