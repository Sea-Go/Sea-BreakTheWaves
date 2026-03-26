package storage

import (
	"context"
	"database/sql"
	"time"
)

// PoolType 候选池类型（用于推荐系统）。
type PoolType string

const (
	PoolLongTerm  PoolType = "long_term"
	PoolShortTerm PoolType = "short_term"
	PoolPeriodic  PoolType = "periodic"
)

// PoolItem 是候选池里的元素（文章级）。
// remark_score：把“文章分数 + 向量相似度”等信号融合后的最终分数，用于池内排序。
type PoolItem struct {
	UserID       string
	PoolType     PoolType
	PeriodBucket string
	ArticleID    string
	Score        float32
	Similarity   float32
	RemarkScore  float32
	InsertedAt   time.Time
}

type PoolRepo struct {
	db *sql.DB
}

func NewPoolRepo(db *sql.DB) *PoolRepo {
	return &PoolRepo{db: db}
}

func (r *PoolRepo) GetPoolSize(ctx context.Context, userID string, poolType PoolType, periodBucket string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM user_pool_items
		WHERE user_id=$1 AND pool_type=$2 AND period_bucket=$3
	`, userID, string(poolType), periodBucket).Scan(&n)
	return n, err
}

// AddItems 批量把候选放入池子（重复的 article_id 自动忽略）。
func (r *PoolRepo) AddItems(ctx context.Context, items []PoolItem) error {
	for _, it := range items {
		_, err := r.db.ExecContext(ctx, `
			INSERT INTO user_pool_items(user_id, pool_type, period_bucket, article_id, score, similarity, remark_score, inserted_at)
			VALUES($1,$2,$3,$4,$5,$6,$7, now())
			ON CONFLICT(user_id, pool_type, period_bucket, article_id) DO UPDATE SET
				score=EXCLUDED.score,
				similarity=EXCLUDED.similarity,
				remark_score=EXCLUDED.remark_score,
				inserted_at=now()
		`, it.UserID, string(it.PoolType), it.PeriodBucket, it.ArticleID, it.Score, it.Similarity, it.RemarkScore)
		if err != nil {
			return err
		}
	}
	return nil
}

// PopTopK 从池子里取出 topK（按 remark_score 降序），并可选删除。
func (r *PoolRepo) PopTopK(ctx context.Context, userID string, poolType PoolType, periodBucket string, topK int, remove bool) ([]PoolItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT user_id, pool_type, period_bucket, article_id, score, similarity, remark_score, inserted_at
		FROM user_pool_items
		WHERE user_id=$1 AND pool_type=$2 AND period_bucket=$3
		ORDER BY remark_score DESC, inserted_at ASC
		LIMIT $4
	`, userID, string(poolType), periodBucket, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []PoolItem
	for rows.Next() {
		var it PoolItem
		var pt string
		if err := rows.Scan(&it.UserID, &pt, &it.PeriodBucket, &it.ArticleID, &it.Score, &it.Similarity, &it.RemarkScore, &it.InsertedAt); err != nil {
			return nil, err
		}
		it.PoolType = PoolType(pt)
		res = append(res, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if remove && len(res) > 0 {
		// 批量删除
		for _, it := range res {
			_, err := r.db.ExecContext(ctx, `
				DELETE FROM user_pool_items
				WHERE user_id=$1 AND pool_type=$2 AND period_bucket=$3 AND article_id=$4
			`, it.UserID, string(it.PoolType), it.PeriodBucket, it.ArticleID)
			if err != nil {
				return nil, err
			}
		}
	}

	return res, nil
}

// RemoveItems 从池子中移除指定 article_id 列表（推荐后出池）。
func (r *PoolRepo) RemoveItems(ctx context.Context, userID string, poolType PoolType, periodBucket string, articleIDs []string) error {
	for _, id := range articleIDs {
		_, err := r.db.ExecContext(ctx, `
			DELETE FROM user_pool_items
			WHERE user_id=$1 AND pool_type=$2 AND period_bucket=$3 AND article_id=$4
		`, userID, string(poolType), periodBucket, id)
		if err != nil {
			return err
		}
	}
	return nil
}
