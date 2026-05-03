package storage

import (
	"context"
	"database/sql"
	"time"
)

// MemoryType 记忆类型。
type MemoryType string

const (
	MemoryLongTerm  MemoryType = "long_term"
	MemoryShortTerm MemoryType = "short_term"
	MemoryPeriodic  MemoryType = "periodic"
)

type UserMemory struct {
	UserID       string
	MemoryType   MemoryType
	PeriodBucket string
	Content      string
	UpdatedAt    time.Time
}

type MemoryRepo struct {
	db *sql.DB
}

func NewMemoryRepo(db *sql.DB) *MemoryRepo {
	return &MemoryRepo{db: db}
}

func (r *MemoryRepo) Upsert(ctx context.Context, m UserMemory) error {
	// 允许上层显式传入更新时间，便于与其他存储（如 Milvus 记忆分块）保持一致的版本号。
	// 若未传入，则退化为当前时间。
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = time.Now()
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_memory(user_id, memory_type, period_bucket, content, updated_at)
		VALUES($1,$2,$3,$4, $5)
		ON CONFLICT(user_id, memory_type, period_bucket) DO UPDATE SET
			content=EXCLUDED.content,
			updated_at=EXCLUDED.updated_at
	`, m.UserID, string(m.MemoryType), m.PeriodBucket, m.Content, m.UpdatedAt)
	return err
}

func (r *MemoryRepo) Get(ctx context.Context, userID string, memType MemoryType, periodBucket string) (UserMemory, bool, error) {
	var m UserMemory
	m.UserID = userID
	m.MemoryType = memType
	m.PeriodBucket = periodBucket

	err := r.db.QueryRowContext(ctx, `
		SELECT content, updated_at
		FROM user_memory
		WHERE user_id=$1 AND memory_type=$2 AND period_bucket=$3
	`, userID, string(memType), periodBucket).Scan(&m.Content, &m.UpdatedAt)
	if err == sql.ErrNoRows {
		return UserMemory{}, false, nil
	}
	if err != nil {
		return UserMemory{}, false, err
	}
	return m, true, nil
}
