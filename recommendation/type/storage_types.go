package types

import "time"

// PoolItem 是候选池里的元素（文章级）。
// 注意：types 包必须保持“纯数据、无业务包依赖”，这里用 string 表示 pool_type。
type PoolItem struct {
	UserID       string
	PoolType     string
	PeriodBucket string
	ArticleID    string
	Score        float32
	Similarity   float32
	RemarkScore  float32
	InsertedAt   time.Time
}

// UserMemory 是用户记忆内容（长期/短期/周期）。
// 注意：types 包保持无 storage 依赖，这里用 string 表示 memory_type。
type UserMemory struct {
	UserID       string
	MemoryType   string
	PeriodBucket string
	Content      string
	UpdatedAt    time.Time
}

// UserHistoryItem 表示一条用户推荐历史记录。
type UserHistoryItem struct {
	HistoryID  string    `json:"history_id"`
	UserID     string    `json:"user_id"`
	ArticleID  string    `json:"article_id"`
	Clicked    bool      `json:"clicked"`
	Preference float32   `json:"preference"`
	TS         time.Time `json:"ts"`

	Similarity float32   `json:"similarity,omitempty"`
	Embed      []float32 `json:"-"`
}
