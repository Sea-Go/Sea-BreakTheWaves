package storage

import (
	"context"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"gorm.io/gorm"
)

type PoolRelease struct {
	PoolReleaseID       string    `gorm:"column:pool_release_id;type:character(64);primaryKey"`
	ModuleID            string    `gorm:"column:module_id;type:text;not null;index:recommend_pool_releases_module,priority:1"`
	PublicationRevision int64     `gorm:"column:publication_revision;type:bigint;not null;check:recommend_pool_releases_publication_revision_check,publication_revision > 0;index:recommend_pool_releases_module,priority:2"`
	FeatureVersion      string    `gorm:"column:feature_version;type:text;not null"`
	FeatureHash         string    `gorm:"column:feature_hash;type:character(64);not null"`
	ReleaseBody         []byte    `gorm:"column:release_body;type:bytea;not null"`
	CreatedAt           time.Time `gorm:"column:created_at;type:timestamptz;not null;default:clock_timestamp()"`
}

type PoolHead struct {
	ModuleID            string      `gorm:"column:module_id;type:text;primaryKey"`
	PoolReleaseID       string      `gorm:"column:pool_release_id;type:character(64);not null"`
	PublicationRevision int64       `gorm:"column:publication_revision;type:bigint;not null;check:recommend_pool_heads_publication_revision_check,publication_revision > 0"`
	PointerVersion      int64       `gorm:"column:pointer_version;type:bigint;not null;check:recommend_pool_heads_pointer_version_check,pointer_version > 0"`
	ChangedAt           time.Time   `gorm:"column:changed_at;type:timestamptz;not null;default:clock_timestamp()"`
	PoolRelease         PoolRelease `gorm:"belongsTo:true;foreignKey:PoolReleaseID;references:PoolReleaseID;constraint:OnDelete:RESTRICT"`
}

type PairProposal struct {
	ProposalID    string      `gorm:"column:proposal_id;type:character(64);primaryKey"`
	ModuleID      string      `gorm:"column:module_id;type:text;not null"`
	PoolReleaseID string      `gorm:"column:pool_release_id;type:character(64);not null"`
	PairID        string      `gorm:"column:pair_id;type:text;not null"`
	ProposalBody  []byte      `gorm:"column:proposal_body;type:bytea;not null"`
	CreatedAt     time.Time   `gorm:"column:created_at;type:timestamptz;not null;default:clock_timestamp()"`
	PoolRelease   PoolRelease `gorm:"belongsTo:true;foreignKey:PoolReleaseID;references:PoolReleaseID;constraint:OnDelete:RESTRICT"`
}

type ItemIndexGeneration struct {
	GenerationID  string       `gorm:"column:item_index_generation_id;type:character(64);primaryKey"`
	ProposalID    string       `gorm:"column:proposal_id;type:character(64);not null"`
	PoolReleaseID string       `gorm:"column:pool_release_id;type:character(64);not null"`
	IndexBody     []byte       `gorm:"column:index_body;type:bytea;not null"`
	CreatedAt     time.Time    `gorm:"column:created_at;type:timestamptz;not null;default:clock_timestamp()"`
	Proposal      PairProposal `gorm:"belongsTo:true;foreignKey:ProposalID;references:ProposalID;constraint:OnDelete:RESTRICT"`
	PoolRelease   PoolRelease  `gorm:"belongsTo:true;foreignKey:PoolReleaseID;references:PoolReleaseID;constraint:OnDelete:RESTRICT"`
}

type PairRelease struct {
	PairReleaseID    string              `gorm:"column:encoder_pair_release_id;type:character(64);primaryKey"`
	ProposalID       string              `gorm:"column:proposal_id;type:character(64);not null;uniqueIndex:recommend_pair_releases_approval_uq,priority:1"`
	GenerationID     string              `gorm:"column:item_index_generation_id;type:character(64);not null;uniqueIndex:recommend_pair_releases_approval_uq,priority:2"`
	ModuleID         string              `gorm:"column:module_id;type:text;not null"`
	PoolReleaseID    string              `gorm:"column:pool_release_id;type:character(64);not null"`
	PairID           string              `gorm:"column:pair_id;type:text;not null"`
	ApprovalRef      string              `gorm:"column:approval_ref;type:text;not null;uniqueIndex:recommend_pair_releases_approval_uq,priority:3"`
	ApprovalRevision int64               `gorm:"column:approval_revision;type:bigint;not null;check:recommend_pair_releases_approval_revision_check,approval_revision > 0;uniqueIndex:recommend_pair_releases_approval_uq,priority:4"`
	ReleaseBody      []byte              `gorm:"column:release_body;type:bytea;not null"`
	CreatedAt        time.Time           `gorm:"column:created_at;type:timestamptz;not null;default:clock_timestamp()"`
	Proposal         PairProposal        `gorm:"belongsTo:true;foreignKey:ProposalID;references:ProposalID;constraint:OnDelete:RESTRICT"`
	Generation       ItemIndexGeneration `gorm:"belongsTo:true;foreignKey:GenerationID;references:GenerationID;constraint:OnDelete:RESTRICT"`
	PoolRelease      PoolRelease         `gorm:"belongsTo:true;foreignKey:PoolReleaseID;references:PoolReleaseID;constraint:OnDelete:RESTRICT"`
}

type PairHead struct {
	ModuleID         string      `gorm:"column:module_id;type:text;primaryKey"`
	PairReleaseID    string      `gorm:"column:encoder_pair_release_id;type:character(64);not null"`
	PairID           string      `gorm:"column:pair_id;type:text;not null;index:recommend_pair_heads_pair"`
	PointerVersion   int64       `gorm:"column:pointer_version;type:bigint;not null;check:recommend_pair_heads_pointer_version_check,pointer_version > 0"`
	ApprovalRef      string      `gorm:"column:approval_ref;type:text;not null"`
	ApprovalRevision int64       `gorm:"column:approval_revision;type:bigint;not null;check:recommend_pair_heads_approval_revision_check,approval_revision > 0"`
	ChangedAt        time.Time   `gorm:"column:changed_at;type:timestamptz;not null;default:clock_timestamp()"`
	PairRelease      PairRelease `gorm:"belongsTo:true;foreignKey:PairReleaseID;references:PairReleaseID;constraint:OnDelete:RESTRICT"`
}

func SchemaModels() []any {
	return []any{
		&PoolRelease{},
		&PoolHead{},
		&PairProposal{},
		&ItemIndexGeneration{},
		&PairRelease{},
		&PairHead{},
	}
}

func MigrateSchema(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := database.AutoMigrate(ctx, tx, SchemaModels()...); err != nil {
			return err
		}
		return ensureImmutableSchemaGuards(ctx, tx)
	})
}

func ensureImmutableSchemaGuards(ctx context.Context, db *gorm.DB) error {
	statements := []string{
		`CREATE OR REPLACE FUNCTION recommend_reject_release_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'recommend pool release is immutable'; END $$`,
		`DROP TRIGGER IF EXISTS recommend_pool_releases_immutable ON recommend_pool_releases`,
		`CREATE TRIGGER recommend_pool_releases_immutable BEFORE UPDATE OR DELETE ON recommend_pool_releases FOR EACH ROW EXECUTE FUNCTION recommend_reject_release_mutation()`,
		`CREATE OR REPLACE FUNCTION recommend_reject_pair_artifact_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'recommend pair artifact is immutable'; END $$`,
		`DROP TRIGGER IF EXISTS recommend_pair_proposals_immutable ON recommend_pair_proposals`,
		`CREATE TRIGGER recommend_pair_proposals_immutable BEFORE UPDATE OR DELETE ON recommend_pair_proposals FOR EACH ROW EXECUTE FUNCTION recommend_reject_pair_artifact_mutation()`,
		`DROP TRIGGER IF EXISTS recommend_item_index_generations_immutable ON recommend_item_index_generations`,
		`CREATE TRIGGER recommend_item_index_generations_immutable BEFORE UPDATE OR DELETE ON recommend_item_index_generations FOR EACH ROW EXECUTE FUNCTION recommend_reject_pair_artifact_mutation()`,
		`DROP TRIGGER IF EXISTS recommend_pair_releases_immutable ON recommend_pair_releases`,
		`CREATE TRIGGER recommend_pair_releases_immutable BEFORE UPDATE OR DELETE ON recommend_pair_releases FOR EACH ROW EXECUTE FUNCTION recommend_reject_pair_artifact_mutation()`,
	}
	for _, statement := range statements {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func (PoolRelease) TableName() string         { return "recommend_pool_releases" }
func (PoolHead) TableName() string            { return "recommend_pool_heads" }
func (PairProposal) TableName() string        { return "recommend_pair_proposals" }
func (ItemIndexGeneration) TableName() string { return "recommend_item_index_generations" }
func (PairRelease) TableName() string         { return "recommend_pair_releases" }
func (PairHead) TableName() string            { return "recommend_pair_heads" }
