package infra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"gorm.io/gorm"
)

type Article struct {
	ArticleID string    `gorm:"column:article_id;type:text;primaryKey"`
	Title     string    `gorm:"column:title;type:text;not null"`
	Cover     *string   `gorm:"column:cover;type:text"`
	TypeTags  *string   `gorm:"column:type_tags;type:text"`
	Tags      *string   `gorm:"column:tags;type:text"`
	Score     float32   `gorm:"column:score;type:real;not null;default:0"`
	CreatedAt time.Time `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
}

type ArticleChunk struct {
	ChunkID   string    `gorm:"column:chunk_id;type:text;primaryKey"`
	ArticleID string    `gorm:"column:article_id;type:text;not null;index:idx_article_chunks_article"`
	H2        *string   `gorm:"column:h2;type:text"`
	Content   string    `gorm:"column:content;type:text;not null"`
	CreatedAt time.Time `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
	Article   Article   `gorm:"belongsTo:true;foreignKey:ArticleID;references:ArticleID;constraint:OnDelete:CASCADE"`
}

type UserPoolItem struct {
	UserID       string    `gorm:"column:user_id;type:text;primaryKey;index:idx_user_pool_items_user_type,priority:1"`
	PoolType     string    `gorm:"column:pool_type;type:text;primaryKey;index:idx_user_pool_items_user_type,priority:2"`
	PeriodBucket string    `gorm:"column:period_bucket;type:text;primaryKey;default:'';index:idx_user_pool_items_user_type,priority:3"`
	ArticleID    string    `gorm:"column:article_id;type:text;primaryKey"`
	Score        float32   `gorm:"column:score;type:real;not null;default:0"`
	Similarity   float32   `gorm:"column:similarity;type:real;not null;default:0"`
	RemarkScore  float32   `gorm:"column:remark_score;type:real;not null;default:0"`
	InsertedAt   time.Time `gorm:"column:inserted_at;type:timestamptz;not null;default:now()"`
	Article      Article   `gorm:"belongsTo:true;foreignKey:ArticleID;references:ArticleID;constraint:OnDelete:CASCADE"`
}

type UserRecHistory struct {
	HistoryID  string    `gorm:"column:history_id;type:text;primaryKey"`
	UserID     string    `gorm:"column:user_id;type:text;not null;index:idx_user_rec_history_user_ts,priority:1;index:idx_user_rec_history_user_article,priority:1"`
	ArticleID  string    `gorm:"column:article_id;type:text;not null;index:idx_user_rec_history_user_article,priority:2"`
	Clicked    bool      `gorm:"column:clicked;type:boolean;not null;default:false"`
	Preference float32   `gorm:"column:preference;type:real;not null;default:0"`
	TS         time.Time `gorm:"column:ts;type:timestamptz;not null;default:now();index:idx_user_rec_history_user_ts,priority:2,sort:DESC"`
	Article    Article   `gorm:"belongsTo:true;foreignKey:ArticleID;references:ArticleID;constraint:OnDelete:CASCADE"`
}

type UserMemory struct {
	UserID       string    `gorm:"column:user_id;type:text;primaryKey"`
	MemoryType   string    `gorm:"column:memory_type;type:text;primaryKey"`
	PeriodBucket string    `gorm:"column:period_bucket;type:text;primaryKey;default:''"`
	Content      string    `gorm:"column:content;type:text;not null"`
	UpdatedAt    time.Time `gorm:"column:updated_at;type:timestamptz;not null;default:now()"`
}

type UserMemoryChunk struct {
	UserID       string    `gorm:"column:user_id;type:text;primaryKey"`
	MemoryType   string    `gorm:"column:memory_type;type:text;primaryKey"`
	PeriodBucket string    `gorm:"column:period_bucket;type:text;primaryKey;default:''"`
	ChunkIndex   int       `gorm:"column:chunk_index;type:integer;primaryKey"`
	Content      string    `gorm:"column:content;type:text;not null"`
	UpdatedAt    time.Time `gorm:"column:updated_at;type:timestamptz;not null;default:now()"`
}

type RecoRequestLog struct {
	RecRequestID   string    `gorm:"column:rec_request_id;type:text;primaryKey"`
	UserID         string    `gorm:"column:user_id;type:text;not null;default:''"`
	SessionID      string    `gorm:"column:session_id;type:text;not null;default:''"`
	Surface        string    `gorm:"column:surface;type:text;not null;default:'home_feed';index:idx_reco_request_logs_surface_created,priority:1"`
	Query          string    `gorm:"column:query;type:text;not null;default:''"`
	Status         string    `gorm:"column:status;type:text;not null;default:''"`
	ReturnedCount  int       `gorm:"column:returned_count;type:integer;not null;default:0"`
	CandidateCount int       `gorm:"column:candidate_count;type:integer;not null;default:0"`
	CreatedAt      time.Time `gorm:"column:created_at;type:timestamptz;not null;default:now();index:idx_reco_request_logs_surface_created,priority:2,sort:DESC"`
}

type RecoEventLog struct {
	ID           int64           `gorm:"column:id;type:bigint GENERATED ALWAYS AS IDENTITY;primaryKey"`
	RecRequestID string          `gorm:"column:rec_request_id;type:text;not null;default:'';index:idx_reco_event_logs_request_article,priority:1"`
	UserID       string          `gorm:"column:user_id;type:text;not null;default:''"`
	SessionID    string          `gorm:"column:session_id;type:text;not null;default:''"`
	Surface      string          `gorm:"column:surface;type:text;not null;default:'home_feed';index:idx_reco_event_logs_surface_ts,priority:1"`
	ArticleID    string          `gorm:"column:article_id;type:text;not null;default:'';index:idx_reco_event_logs_request_article,priority:2"`
	Rank         int             `gorm:"column:rank;type:integer;not null;default:0"`
	EventType    string          `gorm:"column:event_type;type:text;not null"`
	EventTS      time.Time       `gorm:"column:event_ts;type:timestamptz;not null;default:now();index:idx_reco_event_logs_surface_ts,priority:2,sort:DESC"`
	Metadata     json.RawMessage `gorm:"column:metadata;type:jsonb;not null;default:'{}'::jsonb"`
}

type RecoEvalCase struct {
	CaseID       string    `gorm:"column:case_id;type:text;primaryKey"`
	UserID       string    `gorm:"column:user_id;type:text;not null;default:''"`
	Query        string    `gorm:"column:query;type:text;not null;default:''"`
	Surface      string    `gorm:"column:surface;type:text;not null;default:'dashboard_recommend'"`
	PeriodBucket string    `gorm:"column:period_bucket;type:text;not null;default:'d1'"`
	CreatedAt    time.Time `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
}

type RecoEvalLabel struct {
	CaseID    string       `gorm:"column:case_id;type:text;primaryKey"`
	ArticleID string       `gorm:"column:article_id;type:text;primaryKey"`
	Relevance float32      `gorm:"column:relevance;type:real;not null;default:0"`
	CreatedAt time.Time    `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
	Case      RecoEvalCase `gorm:"belongsTo:true;foreignKey:CaseID;references:CaseID;constraint:OnDelete:CASCADE"`
}

type RecoEvalRun struct {
	RunID     string          `gorm:"column:run_id;type:text;primaryKey"`
	CaseCount int             `gorm:"column:case_count;type:integer;not null;default:0"`
	Metrics   json.RawMessage `gorm:"column:metrics;type:jsonb;not null;default:'{}'::jsonb"`
	CreatedAt time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
}

func ApplicationModels() []any {
	return []any{
		&Article{},
		&ArticleChunk{},
		&UserPoolItem{},
		&UserRecHistory{},
		&UserMemory{},
		&UserMemoryChunk{},
		&RecoRequestLog{},
		&RecoEventLog{},
		&RecoEvalCase{},
		&RecoEvalLabel{},
		&RecoEvalRun{},
	}
}

func ensurePGSchema(ctx context.Context, db *gorm.DB) error {
	return database.AutoMigrate(ctx, db, ApplicationModels()...)
}

func (Article) TableName() string         { return "articles" }
func (ArticleChunk) TableName() string    { return "article_chunks" }
func (UserPoolItem) TableName() string    { return "user_pool_items" }
func (UserRecHistory) TableName() string  { return "user_rec_history" }
func (UserMemory) TableName() string      { return "user_memory" }
func (UserMemoryChunk) TableName() string { return "user_memory_chunks" }
func (RecoRequestLog) TableName() string  { return "reco_request_logs" }
func (RecoEventLog) TableName() string    { return "reco_event_logs" }
func (RecoEvalCase) TableName() string    { return "reco_eval_cases" }
func (RecoEvalLabel) TableName() string   { return "reco_eval_labels" }
func (RecoEvalRun) TableName() string     { return "reco_eval_runs" }
