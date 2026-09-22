// Package content keeps the content build ledger and its GORM schema owner.
package content

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

type ChunkProfile struct {
	ProfileID      string `gorm:"column:profile_id;type:text;primaryKey"`
	DefinitionHash string `gorm:"column:definition_hash;type:text;not null"`
}

type BuildRow struct {
	BuildID       string           `gorm:"column:build_id;type:text;primaryKey"`
	ModuleID      string           `gorm:"column:module_id;type:text;not null;index:content_builds_module,priority:1"`
	ReleaseID     string           `gorm:"column:release_id;type:text;not null"`
	Generation    int64            `gorm:"column:generation;type:bigint;not null;check:content_builds_generation_check,generation > 0"`
	InputHash     string           `gorm:"column:input_hash;type:text;not null"`
	OperationID   string           `gorm:"column:operation_id;type:text;not null"`
	Revisions     json.RawMessage  `gorm:"column:revisions;type:jsonb;not null"`
	AttemptID     string           `gorm:"column:attempt_id;type:text;not null"`
	LeaseEpoch    int64            `gorm:"column:lease_epoch;type:bigint;not null;check:content_builds_lease_epoch_check,lease_epoch > 0"`
	LeaseExpires  time.Time        `gorm:"column:lease_expires_at;type:timestamptz;not null"`
	CancelVersion int64            `gorm:"column:cancel_version;type:bigint;not null;check:content_builds_cancel_version_check,cancel_version >= 0"`
	State         string           `gorm:"column:state;type:text;not null;check:content_builds_state_check,state IN ('BUILDING', 'READY', 'FAILED', 'CANCELLED', 'SUPERSEDED')"`
	Chunks        *json.RawMessage `gorm:"column:chunks;type:jsonb"`
	Result        *json.RawMessage `gorm:"column:result;type:jsonb"`
	ErrorCode     string           `gorm:"column:error_code;type:text;not null;default:''"`
	CreatedAt     time.Time        `gorm:"column:created_at;type:timestamptz;not null;default:clock_timestamp()"`
	UpdatedAt     time.Time        `gorm:"column:updated_at;type:timestamptz;not null;default:clock_timestamp()"`
}

type BuildLane struct {
	BuildID   string          `gorm:"column:build_id;type:text;primaryKey"`
	Lane      string          `gorm:"column:lane;type:text;primaryKey;check:content_build_lanes_lane_check,lane IN ('dense', 'sparse', 'multivector');"`
	Artifact  json.RawMessage `gorm:"column:artifact;type:jsonb;not null"`
	CreatedAt time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:clock_timestamp()"`
	Build     BuildRow        `gorm:"belongsTo:true;foreignKey:BuildID;references:BuildID;constraint:OnDelete:RESTRICT"`
}

type Tombstone struct {
	ModuleID         string `gorm:"column:module_id;type:text;primaryKey"`
	RevisionID       string `gorm:"column:revision_id;type:text;primaryKey"`
	AggregateVersion int64  `gorm:"column:aggregate_version;type:bigint;not null;check:content_tombstones_aggregate_version_check,aggregate_version > 0"`
	InputHash        string `gorm:"column:input_hash;type:text;not null"`
}

type Outbox struct {
	EventID     string          `gorm:"column:event_id;type:text;primaryKey"`
	BuildID     string          `gorm:"column:build_id;type:text;not null"`
	Payload     json.RawMessage `gorm:"column:payload;type:jsonb;not null"`
	CreatedAt   time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:clock_timestamp()"`
	DeliveredAt *time.Time      `gorm:"column:delivered_at;type:timestamptz"`
	Build       BuildRow        `gorm:"belongsTo:true;foreignKey:BuildID;references:BuildID;constraint:OnDelete:RESTRICT"`
}

type IndexDispatchRow struct {
	BuildID          string     `gorm:"column:build_id;type:text;primaryKey"`
	JobID            string     `gorm:"column:job_id;type:text;not null"`
	WorkerID         string     `gorm:"column:worker_id;type:text;not null"`
	AttemptID        string     `gorm:"column:attempt_id;type:text;not null"`
	LeaseEpoch       int64      `gorm:"column:lease_epoch;type:bigint;not null;check:content_index_dispatch_lease_epoch_check,lease_epoch > 0"`
	CancelVersion    int64      `gorm:"column:cancel_version;type:bigint;not null;check:content_index_dispatch_cancel_version_check,cancel_version >= 0"`
	LeaseExpiresAt   time.Time  `gorm:"column:lease_expires_at;type:timestamptz;not null"`
	Stage            string     `gorm:"column:stage;type:text;not null;default:'pending';check:content_index_dispatch_stage_check,stage IN ('pending', 'needs_new_attempt', 'manual', 'complete');index:content_index_dispatch_pending,priority:1,where:stage = 'pending'"`
	ClaimEpoch       int64      `gorm:"column:claim_epoch;type:bigint;not null;default:0"`
	ClaimUntil       *time.Time `gorm:"column:claim_until;type:timestamptz;index:content_index_dispatch_pending,priority:2,where:stage = 'pending'"`
	RTWAcceptedAt    *time.Time `gorm:"column:rtw_accepted_at;type:timestamptz"`
	LastError        string     `gorm:"column:last_error;type:text;not null;default:''"`
	UpdatedAt        time.Time  `gorm:"column:updated_at;type:timestamptz;not null;default:clock_timestamp();index:content_index_dispatch_pending,priority:3,where:stage = 'pending'"`
	DCAttemptID      string     `gorm:"column:dc_attempt_id;type:text;not null;check:content_index_dispatch_dc_fence_valid,dc_attempt_id <> '' AND dc_lease_epoch > 0 AND dc_cancel_version >= 0"`
	DCLeaseEpoch     int64      `gorm:"column:dc_lease_epoch;type:bigint;not null"`
	DCCancelVersion  int64      `gorm:"column:dc_cancel_version;type:bigint;not null"`
	DCLeaseExpiresAt time.Time  `gorm:"column:dc_lease_expires_at;type:timestamptz;not null"`
	Build            BuildRow   `gorm:"belongsTo:true;foreignKey:BuildID;references:BuildID;constraint:OnDelete:RESTRICT"`
}

func Models() []any {
	return []any{
		&ChunkProfile{},
		&BuildRow{},
		&BuildLane{},
		&Tombstone{},
		&Outbox{},
		&IndexDispatchRow{},
	}
}

func Migrate(ctx context.Context, db *gorm.DB) error {
	return database.AutoMigrate(ctx, db, Models()...)
}

// MigratePool lets isolated pgx acceptance tests initialize the same GORM-owned
// schema without falling back to executable SQL migrations.
func MigratePool(ctx context.Context, pool *pgxpool.Pool) error {
	cfg := pool.Config()
	orm, err := database.OpenPgx(ctx, cfg.ConnConfig, database.Config{})
	if err != nil {
		return err
	}
	if sqlDB, dbErr := orm.DB(); dbErr == nil {
		defer sqlDB.Close()
	}
	return Migrate(ctx, orm)
}

func (ChunkProfile) TableName() string     { return "content_chunk_profiles" }
func (BuildRow) TableName() string         { return "content_builds" }
func (BuildLane) TableName() string        { return "content_build_lanes" }
func (Tombstone) TableName() string        { return "content_tombstones" }
func (Outbox) TableName() string           { return "content_outbox" }
func (IndexDispatchRow) TableName() string { return "content_index_dispatch" }
