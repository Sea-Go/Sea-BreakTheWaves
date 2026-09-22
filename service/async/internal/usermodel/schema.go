package usermodel

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

type SubjectStateRow struct {
	AuthorityID  string    `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID     string    `gorm:"column:tenant_id;type:text;primaryKey"`
	SubjectID    string    `gorm:"column:subject_id;type:text;primaryKey"`
	StateVersion int64     `gorm:"column:state_version;type:bigint;not null;default:0;check:usermodel_subject_state_state_version_check,state_version >= 0"`
	UpdatedAt    time.Time `gorm:"column:updated_at;type:timestamptz;not null;default:now()"`
}

type EventRow struct {
	AuthorityID        string          `gorm:"column:authority_id;type:text;primaryKey;index:usermodel_events_history_idx,priority:1;uniqueIndex:usermodel_event_position_uq,priority:1,where:source_sequence IS NOT NULL;uniqueIndex:usermodel_successor_uq,priority:1,where:supersedes_event_id IS NOT NULL"`
	TenantID           string          `gorm:"column:tenant_id;type:text;primaryKey;index:usermodel_events_history_idx,priority:2;uniqueIndex:usermodel_event_position_uq,priority:2,where:source_sequence IS NOT NULL;uniqueIndex:usermodel_successor_uq,priority:2,where:supersedes_event_id IS NOT NULL"`
	SubjectID          string          `gorm:"column:subject_id;type:text;primaryKey;index:usermodel_events_history_idx,priority:3;uniqueIndex:usermodel_event_position_uq,priority:3,where:source_sequence IS NOT NULL;uniqueIndex:usermodel_successor_uq,priority:3,where:supersedes_event_id IS NOT NULL"`
	Producer           string          `gorm:"column:producer;type:text;primaryKey;index:usermodel_events_history_idx,priority:5;uniqueIndex:usermodel_event_position_uq,priority:4,where:source_sequence IS NOT NULL;uniqueIndex:usermodel_successor_uq,priority:4,where:supersedes_event_id IS NOT NULL"`
	EventID            string          `gorm:"column:event_id;type:text;primaryKey;index:usermodel_events_history_idx,priority:6"`
	NormalizedHash     string          `gorm:"column:normalized_hash;type:text;not null"`
	EventBody          json.RawMessage `gorm:"column:event_body;type:jsonb;not null"`
	Action             string          `gorm:"column:action;type:text;not null;check:usermodel_events_action_check,action IN ('assert', 'correct', 'retract')"`
	SemanticKind       string          `gorm:"column:semantic_kind;type:text;not null"`
	OccurredAt         time.Time       `gorm:"column:occurred_at;type:timestamptz;not null;index:usermodel_events_history_idx,priority:4"`
	ObservedAt         time.Time       `gorm:"column:observed_at;type:timestamptz;not null"`
	ReceivedAt         time.Time       `gorm:"column:received_at;type:timestamptz;not null;default:now()"`
	SourcePartition    string          `gorm:"column:source_partition;type:text;not null;uniqueIndex:usermodel_event_position_uq,priority:5,where:source_sequence IS NOT NULL"`
	SourceSequence     *int64          `gorm:"column:source_sequence;type:bigint;uniqueIndex:usermodel_event_position_uq,priority:6,where:source_sequence IS NOT NULL;check:usermodel_events_source_sequence_check,source_sequence IS NULL OR source_sequence > 0"`
	SupersedesProducer *string         `gorm:"column:supersedes_producer;type:text;check:usermodel_events_supersedes_pair_check,(supersedes_producer IS NULL) = (supersedes_event_id IS NULL);uniqueIndex:usermodel_successor_uq,priority:5,where:supersedes_event_id IS NOT NULL"`
	SupersedesEventID  *string         `gorm:"column:supersedes_event_id;type:text;check:usermodel_events_action_successor_check,(action = 'assert') = (supersedes_event_id IS NULL);uniqueIndex:usermodel_successor_uq,priority:6,where:supersedes_event_id IS NOT NULL"`
	Status             string          `gorm:"column:status;type:text;not null;check:usermodel_events_status_check,status IN ('accepted', 'pending_dependency')"`
	InitialStatus      string          `gorm:"column:initial_status;type:text;not null;check:usermodel_events_initial_status_check,initial_status IN ('accepted', 'pending_dependency')"`
	InitialVersion     int64           `gorm:"column:initial_version;type:bigint;not null;check:usermodel_events_initial_version_check,initial_version >= 0"`
	AcceptedVersion    *int64          `gorm:"column:accepted_version;type:bigint"`
	Subject            SubjectStateRow `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID;references:AuthorityID,TenantID,SubjectID;constraint:OnDelete:RESTRICT"`
}

type UnmappedEventRow struct {
	AuthorityID     string          `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID        string          `gorm:"column:tenant_id;type:text;primaryKey"`
	ExternalSubject string          `gorm:"column:external_subject_id;type:text;primaryKey"`
	Producer        string          `gorm:"column:producer;type:text;primaryKey"`
	EventID         string          `gorm:"column:event_id;type:text;primaryKey"`
	NormalizedHash  string          `gorm:"column:normalized_hash;type:text;not null"`
	EventBody       json.RawMessage `gorm:"column:event_body;type:jsonb;not null"`
	ReceivedAt      time.Time       `gorm:"column:received_at;type:timestamptz;not null;default:now()"`
	BoundSubjectID  *string         `gorm:"column:bound_subject_id;type:text"`
	BoundVersion    *int64          `gorm:"column:bound_version;type:bigint;check:usermodel_unmapped_events_version_binding_check,(bound_subject_id IS NULL) = (bound_version IS NULL)"`
	BoundStatus     *string         `gorm:"column:bound_status;type:text;check:usermodel_unmapped_events_status_binding_check,(bound_subject_id IS NULL) = (bound_status IS NULL)"`
}

type SubjectBindingRow struct {
	AuthorityID     string    `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID        string    `gorm:"column:tenant_id;type:text;primaryKey"`
	ExternalSubject string    `gorm:"column:external_subject_id;type:text;primaryKey"`
	SubjectID       string    `gorm:"column:subject_id;type:text;not null"`
	FirstBoundAt    time.Time `gorm:"column:first_bound_at;type:timestamptz;not null;default:now()"`
}

type ActiveFactRow struct {
	AuthorityID      string   `gorm:"column:authority_id;type:text;primaryKey;uniqueIndex:usermodel_active_impression_uq,priority:1,where:impression_id IS NOT NULL"`
	TenantID         string   `gorm:"column:tenant_id;type:text;primaryKey;uniqueIndex:usermodel_active_impression_uq,priority:2,where:impression_id IS NOT NULL"`
	SubjectID        string   `gorm:"column:subject_id;type:text;primaryKey;uniqueIndex:usermodel_active_impression_uq,priority:3,where:impression_id IS NOT NULL"`
	Producer         string   `gorm:"column:producer;type:text;primaryKey"`
	EventID          string   `gorm:"column:event_id;type:text;primaryKey"`
	ImpressionID     *string  `gorm:"column:impression_id;type:text;uniqueIndex:usermodel_active_impression_uq,priority:4,where:impression_id IS NOT NULL"`
	ActivatedVersion int64    `gorm:"column:activated_version;type:bigint;not null"`
	Event            EventRow `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID,Producer,EventID;references:AuthorityID,TenantID,SubjectID,Producer,EventID;constraint:OnDelete:RESTRICT"`
}

type AttributionRow struct {
	AuthorityID       string    `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID          string    `gorm:"column:tenant_id;type:text;primaryKey"`
	SubjectID         string    `gorm:"column:subject_id;type:text;primaryKey"`
	Producer          string    `gorm:"column:producer;type:text;primaryKey"`
	EventID           string    `gorm:"column:event_id;type:text;primaryKey"`
	ImpressionID      string    `gorm:"column:impression_id;type:text;not null"`
	SourceEvidenceRef string    `gorm:"column:source_evidence_ref;type:text;not null"`
	LinkedVersion     int64     `gorm:"column:linked_version;type:bigint;not null"`
	LinkedAt          time.Time `gorm:"column:linked_at;type:timestamptz;not null;default:now()"`
	RevokedVersion    *int64    `gorm:"column:revoked_version;type:bigint"`
	Event             EventRow  `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID,Producer,EventID;references:AuthorityID,TenantID,SubjectID,Producer,EventID;constraint:OnDelete:RESTRICT"`
}

type WatermarkRow struct {
	AuthorityID        string `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID           string `gorm:"column:tenant_id;type:text;primaryKey"`
	SubjectID          string `gorm:"column:subject_id;type:text;primaryKey"`
	Producer           string `gorm:"column:producer;type:text;primaryKey"`
	SourcePartition    string `gorm:"column:source_partition;type:text;primaryKey"`
	ContiguousSequence int64  `gorm:"column:contiguous_sequence;type:bigint;not null;default:0"`
	MaxSeenSequence    int64  `gorm:"column:max_seen_sequence;type:bigint;not null;default:0"`
}

type OutboxRow struct {
	OutboxID     int64           `gorm:"column:outbox_id;type:bigint GENERATED ALWAYS AS IDENTITY;primaryKey"`
	AuthorityID  string          `gorm:"column:authority_id;type:text;not null;uniqueIndex:usermodel_outbox_version_uq,priority:1"`
	TenantID     string          `gorm:"column:tenant_id;type:text;not null;uniqueIndex:usermodel_outbox_version_uq,priority:2"`
	SubjectID    string          `gorm:"column:subject_id;type:text;not null;uniqueIndex:usermodel_outbox_version_uq,priority:3"`
	StateVersion int64           `gorm:"column:state_version;type:bigint;not null;uniqueIndex:usermodel_outbox_version_uq,priority:4"`
	EventType    string          `gorm:"column:event_type;type:text;not null"`
	Producer     string          `gorm:"column:producer;type:text;not null"`
	EventID      string          `gorm:"column:event_id;type:text;not null"`
	Payload      json.RawMessage `gorm:"column:payload;type:jsonb;not null"`
	CreatedAt    time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
	DeliveredAt  *time.Time      `gorm:"column:delivered_at;type:timestamptz;index:usermodel_outbox_pending_idx,where:delivered_at IS NULL"`
}

type CoveragePrefixRow struct {
	ManifestSHA256      string          `gorm:"column:manifest_sha256;type:text;primaryKey"`
	Producer            string          `gorm:"column:producer;type:text;not null;index:usermodel_coverage_prefix_scope_idx,priority:1"`
	BindingPolicyID     string          `gorm:"column:binding_policy_id;type:text;not null;index:usermodel_coverage_prefix_scope_idx,priority:2"`
	ThroughOffset       int64           `gorm:"column:through_offset;type:bigint;not null;check:usermodel_coverage_prefix_through_offset_check,through_offset > 0 AND through_offset <= 9007199254740991;index:usermodel_coverage_prefix_scope_idx,priority:3"`
	EventIndexSHA256    string          `gorm:"column:event_index_sha256;type:text;not null"`
	BatchEvidenceSHA256 string          `gorm:"column:batch_evidence_sha256;type:text;not null"`
	RefBody             json.RawMessage `gorm:"column:ref_body;type:jsonb;not null"`
	VerifiedAt          time.Time       `gorm:"column:verified_at;type:timestamptz;not null;default:now()"`
}

type CoverageEventRow struct {
	ManifestSHA256  string            `gorm:"column:manifest_sha256;type:text;primaryKey;index:usermodel_coverage_subject_offset_idx,priority:1"`
	SourceOffset    int64             `gorm:"column:source_offset;type:bigint;not null;primaryKey;index:usermodel_coverage_subject_offset_idx,priority:5;check:usermodel_coverage_event_source_offset_check,source_offset > 0"`
	Producer        string            `gorm:"column:producer;type:text;not null"`
	EventID         string            `gorm:"column:event_id;type:text;not null"`
	InputHash       string            `gorm:"column:input_hash;type:text;not null"`
	AuthorityID     string            `gorm:"column:authority_id;type:text;not null;index:usermodel_coverage_subject_offset_idx,priority:2"`
	TenantID        string            `gorm:"column:tenant_id;type:text;not null;index:usermodel_coverage_subject_offset_idx,priority:3"`
	SubjectID       string            `gorm:"column:subject_id;type:text;not null;index:usermodel_coverage_subject_offset_idx,priority:4"`
	NormalizedHash  string            `gorm:"column:normalized_hash;type:text;not null"`
	AcceptedVersion int64             `gorm:"column:accepted_version;type:bigint;not null;check:usermodel_coverage_event_accepted_version_check,accepted_version > 0"`
	RowBody         json.RawMessage   `gorm:"column:row_body;type:jsonb;not null"`
	Prefix          CoveragePrefixRow `gorm:"belongsTo:true;foreignKey:ManifestSHA256;references:ManifestSHA256;constraint:OnDelete:RESTRICT"`
	Event           EventRow          `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID,Producer,EventID;references:AuthorityID,TenantID,SubjectID,Producer,EventID;constraint:OnDelete:RESTRICT"`
}

type CoverageSubjectRow struct {
	ManifestSHA256    string            `gorm:"column:manifest_sha256;type:text;primaryKey"`
	AuthorityID       string            `gorm:"column:authority_id;type:text;not null;primaryKey"`
	TenantID          string            `gorm:"column:tenant_id;type:text;not null;primaryKey"`
	SubjectID         string            `gorm:"column:subject_id;type:text;not null;primaryKey"`
	ReceiptSHA256     string            `gorm:"column:receipt_sha256;type:text;not null"`
	SparseIndexSHA256 string            `gorm:"column:sparse_index_sha256;type:text;not null"`
	EventCount        int64             `gorm:"column:event_count;type:bigint;not null;check:usermodel_coverage_subject_event_count_check,event_count >= 0"`
	RefBody           json.RawMessage   `gorm:"column:ref_body;type:jsonb;not null"`
	VerifiedAt        time.Time         `gorm:"column:verified_at;type:timestamptz;not null;default:now()"`
	Prefix            CoveragePrefixRow `gorm:"belongsTo:true;foreignKey:ManifestSHA256;references:ManifestSHA256;constraint:OnDelete:RESTRICT"`
}

type OntologyDefinitionRow struct {
	AuthorityID       string          `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID          string          `gorm:"column:tenant_id;type:text;primaryKey"`
	DefinitionVersion int64           `gorm:"column:definition_version;type:bigint;not null;primaryKey;check:usermodel_ontology_definitions_version_check,definition_version > 0"`
	DefinitionHash    string          `gorm:"column:definition_hash;type:text;not null"`
	DefinitionBody    json.RawMessage `gorm:"column:definition_body;type:jsonb;not null"`
	CreatedAt         time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
}

type OntologyHeadRow struct {
	AuthorityID       string                `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID          string                `gorm:"column:tenant_id;type:text;primaryKey"`
	DefinitionVersion int64                 `gorm:"column:definition_version;type:bigint;not null"`
	ActivatedAt       time.Time             `gorm:"column:activated_at;type:timestamptz;not null;default:now()"`
	Definition        OntologyDefinitionRow `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,DefinitionVersion;references:AuthorityID,TenantID,DefinitionVersion;constraint:OnDelete:RESTRICT"`
}

type OntologyProjectionRow struct {
	AuthorityID       string                `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID          string                `gorm:"column:tenant_id;type:text;primaryKey"`
	SubjectID         string                `gorm:"column:subject_id;type:text;primaryKey"`
	DefinitionVersion int64                 `gorm:"column:definition_version;type:bigint;not null"`
	StateVersion      int64                 `gorm:"column:state_version;type:bigint;not null;check:usermodel_ontology_projections_state_version_check,state_version >= 0"`
	AsOf              time.Time             `gorm:"column:as_of;type:timestamptz;not null"`
	NextChangeAt      *time.Time            `gorm:"column:next_change_at;type:timestamptz;index:usermodel_ontology_projection_change_idx,priority:3,where:next_change_at IS NOT NULL"`
	ProjectionBody    json.RawMessage       `gorm:"column:projection_body;type:jsonb;not null"`
	RebuiltAt         time.Time             `gorm:"column:rebuilt_at;type:timestamptz;not null;default:now()"`
	Subject           SubjectStateRow       `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID;references:AuthorityID,TenantID,SubjectID;constraint:OnDelete:RESTRICT"`
	Definition        OntologyDefinitionRow `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,DefinitionVersion;references:AuthorityID,TenantID,DefinitionVersion;constraint:OnDelete:RESTRICT"`
}

type FeatureBaselineRow struct {
	AuthorityID  string          `gorm:"column:authority_id;type:text;primaryKey;uniqueIndex:usermodel_feature_baselines_generation_uq,priority:1"`
	TenantID     string          `gorm:"column:tenant_id;type:text;primaryKey;uniqueIndex:usermodel_feature_baselines_generation_uq,priority:2"`
	SubjectID    string          `gorm:"column:subject_id;type:text;primaryKey;uniqueIndex:usermodel_feature_baselines_generation_uq,priority:3"`
	Revision     int64           `gorm:"column:revision;type:bigint;not null;primaryKey;check:usermodel_feature_baselines_revision_check,revision > 0"`
	Generation   string          `gorm:"column:generation;type:text;not null;uniqueIndex:usermodel_feature_baselines_generation_uq,priority:4"`
	SpecVersion  string          `gorm:"column:spec_version;type:text;not null"`
	SpecHash     string          `gorm:"column:spec_hash;type:text;not null"`
	BaselineHash string          `gorm:"column:baseline_hash;type:text;not null"`
	BaselineBody json.RawMessage `gorm:"column:baseline_body;type:jsonb;not null"`
	AcceptedAt   time.Time       `gorm:"column:accepted_at;type:timestamptz;not null;default:now()"`
	Subject      SubjectStateRow `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID;references:AuthorityID,TenantID,SubjectID;constraint:OnDelete:RESTRICT"`
}

type FeatureHeadRow struct {
	AuthorityID string             `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID    string             `gorm:"column:tenant_id;type:text;primaryKey"`
	SubjectID   string             `gorm:"column:subject_id;type:text;primaryKey"`
	Revision    int64              `gorm:"column:revision;type:bigint;not null"`
	ChangedAt   time.Time          `gorm:"column:changed_at;type:timestamptz;not null;default:now()"`
	Baseline    FeatureBaselineRow `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID,Revision;references:AuthorityID,TenantID,SubjectID,Revision;constraint:OnDelete:RESTRICT"`
}

type FeatureSnapshotRow struct {
	AuthorityID      string          `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID         string          `gorm:"column:tenant_id;type:text;primaryKey"`
	SubjectID        string          `gorm:"column:subject_id;type:text;primaryKey"`
	StateVersion     int64           `gorm:"column:state_version;type:bigint;not null;check:usermodel_feature_snapshots_state_version_check,state_version >= 0"`
	SpecVersion      string          `gorm:"column:spec_version;type:text;not null"`
	SpecHash         string          `gorm:"column:spec_hash;type:text;not null"`
	BaselineRevision *int64          `gorm:"column:baseline_revision;type:bigint"`
	AsOf             time.Time       `gorm:"column:as_of;type:timestamptz;not null"`
	AvailableAt      time.Time       `gorm:"column:available_at;type:timestamptz;not null"`
	SnapshotBody     json.RawMessage `gorm:"column:snapshot_body;type:jsonb;not null"`
	BuiltAt          time.Time       `gorm:"column:built_at;type:timestamptz;not null;default:now()"`
	Subject          SubjectStateRow `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID;references:AuthorityID,TenantID,SubjectID;constraint:OnDelete:RESTRICT"`
}

type FeatureSnapshotVersionRow struct {
	AuthorityID  string          `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID     string          `gorm:"column:tenant_id;type:text;primaryKey"`
	SubjectID    string          `gorm:"column:subject_id;type:text;primaryKey"`
	SnapshotID   string          `gorm:"column:snapshot_id;type:text;not null;primaryKey"`
	SnapshotBody json.RawMessage `gorm:"column:snapshot_body;type:jsonb;not null"`
	CreatedAt    time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
	Subject      SubjectStateRow `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID;references:AuthorityID,TenantID,SubjectID;constraint:OnDelete:RESTRICT"`
}

type ServingBundleRow struct {
	AuthorityID       string                    `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID          string                    `gorm:"column:tenant_id;type:text;primaryKey"`
	SubjectID         string                    `gorm:"column:subject_id;type:text;primaryKey"`
	BundleID          string                    `gorm:"column:bundle_id;type:text;not null;primaryKey"`
	PairID            string                    `gorm:"column:pair_id;type:text;not null"`
	SpaceID           string                    `gorm:"column:space_id;type:text;not null"`
	FeatureSnapshotID string                    `gorm:"column:feature_snapshot_id;type:text;not null"`
	BundleBody        json.RawMessage           `gorm:"column:bundle_body;type:jsonb;not null"`
	CreatedAt         time.Time                 `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
	Snapshot          FeatureSnapshotVersionRow `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID,FeatureSnapshotID;references:AuthorityID,TenantID,SubjectID,SnapshotID;constraint:OnDelete:RESTRICT"`
}

type ServingPointerRow struct {
	AuthorityID      string           `gorm:"column:authority_id;type:text;primaryKey"`
	TenantID         string           `gorm:"column:tenant_id;type:text;primaryKey"`
	SubjectID        string           `gorm:"column:subject_id;type:text;primaryKey"`
	PairID           string           `gorm:"column:pair_id;type:text;not null;primaryKey"`
	BundleID         string           `gorm:"column:bundle_id;type:text;not null"`
	State            string           `gorm:"column:state;type:text;not null;check:usermodel_serving_pointers_state_check,state IN ('active', 'disabled')"`
	ApprovalRef      string           `gorm:"column:approval_ref;type:text;not null"`
	ApprovalRevision int64            `gorm:"column:approval_revision;type:bigint;not null;check:usermodel_serving_pointers_approval_revision_check,approval_revision > 0"`
	PointerVersion   int64            `gorm:"column:pointer_version;type:bigint;not null;check:usermodel_serving_pointers_pointer_version_check,pointer_version > 0"`
	ChangedAt        time.Time        `gorm:"column:changed_at;type:timestamptz;not null;default:now()"`
	Bundle           ServingBundleRow `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID,BundleID;references:AuthorityID,TenantID,SubjectID,BundleID;constraint:OnDelete:RESTRICT"`
}

type CoveredBaselineRow struct {
	AuthorityID          string             `gorm:"column:authority_id;type:text;primaryKey;uniqueIndex:usermodel_covered_baselines_v2_generation_uq,priority:1"`
	TenantID             string             `gorm:"column:tenant_id;type:text;primaryKey;uniqueIndex:usermodel_covered_baselines_v2_generation_uq,priority:2"`
	SubjectID            string             `gorm:"column:subject_id;type:text;primaryKey;uniqueIndex:usermodel_covered_baselines_v2_generation_uq,priority:3"`
	Revision             int64              `gorm:"column:revision;type:bigint;not null;primaryKey;check:usermodel_covered_baselines_v2_revision_check,revision > 0"`
	Generation           string             `gorm:"column:generation;type:text;not null;uniqueIndex:usermodel_covered_baselines_v2_generation_uq,priority:4"`
	ArtifactURL          string             `gorm:"column:artifact_url;type:text;not null"`
	ArtifactSHA256       string             `gorm:"column:artifact_sha256;type:text;not null"`
	SchemaVersion        string             `gorm:"column:schema_version;type:text;not null;check:usermodel_covered_baselines_v2_schema_version_check,schema_version = 'sea.user-feature-baseline.covered.v2'"`
	Status               string             `gorm:"column:status;type:text;not null;check:usermodel_covered_baselines_v2_status_check,status = 'accepted_historical_default_off'"`
	SpecVersion          string             `gorm:"column:spec_version;type:text;not null"`
	SpecHash             string             `gorm:"column:spec_hash;type:text;not null"`
	PrefixManifestSHA256 string             `gorm:"column:prefix_manifest_sha256;type:text;not null"`
	SubjectReceiptSHA256 string             `gorm:"column:subject_receipt_sha256;type:text;not null"`
	ThroughOffset        int64              `gorm:"column:through_offset;type:bigint;not null;check:usermodel_covered_baselines_v2_through_offset_check,through_offset > 0 AND through_offset <= 9007199254740991"`
	InputStateVersion    int64              `gorm:"column:input_state_version;type:bigint;not null;check:usermodel_covered_baselines_v2_input_state_version_check,input_state_version >= 0"`
	AsOf                 time.Time          `gorm:"column:as_of;type:timestamptz;not null"`
	AvailableAt          time.Time          `gorm:"column:available_at;type:timestamptz;not null"`
	CandidateRaw         []byte             `gorm:"column:candidate_raw;type:bytea;not null"`
	CandidateBody        json.RawMessage    `gorm:"column:candidate_body;type:jsonb;not null"`
	AcceptedAt           time.Time          `gorm:"column:accepted_at;type:timestamptz;not null;default:now()"`
	CoverageSubject      CoverageSubjectRow `gorm:"belongsTo:true;foreignKey:PrefixManifestSHA256,AuthorityID,TenantID,SubjectID;references:ManifestSHA256,AuthorityID,TenantID,SubjectID;constraint:OnDelete:RESTRICT"`
}

type CoveredSnapshotRow struct {
	AuthorityID            string             `gorm:"column:authority_id;type:text;primaryKey;uniqueIndex:usermodel_covered_snapshots_v2_baseline_uq,priority:1"`
	TenantID               string             `gorm:"column:tenant_id;type:text;primaryKey;uniqueIndex:usermodel_covered_snapshots_v2_baseline_uq,priority:2"`
	SubjectID              string             `gorm:"column:subject_id;type:text;primaryKey;uniqueIndex:usermodel_covered_snapshots_v2_baseline_uq,priority:3"`
	SnapshotID             string             `gorm:"column:snapshot_id;type:text;not null;primaryKey"`
	BaselineRevision       int64              `gorm:"column:baseline_revision;type:bigint;not null;check:usermodel_covered_snapshots_v2_baseline_revision_check,baseline_revision > 0;uniqueIndex:usermodel_covered_snapshots_v2_baseline_uq,priority:4"`
	BaselineArtifactSHA256 string             `gorm:"column:baseline_artifact_sha256;type:text;not null"`
	PrefixManifestSHA256   string             `gorm:"column:prefix_manifest_sha256;type:text;not null"`
	SubjectReceiptSHA256   string             `gorm:"column:subject_receipt_sha256;type:text;not null"`
	SpecVersion            string             `gorm:"column:spec_version;type:text;not null"`
	SpecHash               string             `gorm:"column:spec_hash;type:text;not null"`
	InputStateVersion      int64              `gorm:"column:input_state_version;type:bigint;not null;check:usermodel_covered_snapshots_v2_input_state_version_check,input_state_version >= 0"`
	SnapshotRawSHA256      string             `gorm:"column:snapshot_raw_sha256;type:text;not null"`
	SnapshotRaw            []byte             `gorm:"column:snapshot_raw;type:bytea;not null"`
	SnapshotBody           json.RawMessage    `gorm:"column:snapshot_body;type:jsonb;not null"`
	Status                 string             `gorm:"column:status;type:text;not null;check:usermodel_covered_snapshots_v2_status_check,status = 'historical_default_off'"`
	CreatedAt              time.Time          `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
	Baseline               CoveredBaselineRow `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID,BaselineRevision;references:AuthorityID,TenantID,SubjectID,Revision;constraint:OnDelete:RESTRICT"`
	CoverageSubject        CoverageSubjectRow `gorm:"belongsTo:true;foreignKey:PrefixManifestSHA256,AuthorityID,TenantID,SubjectID;references:ManifestSHA256,AuthorityID,TenantID,SubjectID;constraint:OnDelete:RESTRICT"`
}

type CoveredBundleCandidateRow struct {
	AuthorityID         string             `gorm:"column:authority_id;type:text;primaryKey;uniqueIndex:usermodel_covered_bundles_v2_pair_uq,priority:1"`
	TenantID            string             `gorm:"column:tenant_id;type:text;primaryKey;uniqueIndex:usermodel_covered_bundles_v2_pair_uq,priority:2"`
	SubjectID           string             `gorm:"column:subject_id;type:text;primaryKey;uniqueIndex:usermodel_covered_bundles_v2_pair_uq,priority:3"`
	BundleID            string             `gorm:"column:bundle_id;type:text;not null;primaryKey"`
	SnapshotID          string             `gorm:"column:snapshot_id;type:text;not null;uniqueIndex:usermodel_covered_bundles_v2_pair_uq,priority:4"`
	PairID              string             `gorm:"column:pair_id;type:text;not null;uniqueIndex:usermodel_covered_bundles_v2_pair_uq,priority:5"`
	SpaceID             string             `gorm:"column:space_id;type:text;not null"`
	EncoderID           string             `gorm:"column:encoder_id;type:text;not null"`
	PairFeatureSpecHash string             `gorm:"column:pair_feature_spec_hash;type:text;not null"`
	BundleRawSHA256     string             `gorm:"column:bundle_raw_sha256;type:text;not null"`
	BundleRaw           []byte             `gorm:"column:bundle_raw;type:bytea;not null"`
	BundleBody          json.RawMessage    `gorm:"column:bundle_body;type:jsonb;not null"`
	Status              string             `gorm:"column:status;type:text;not null;check:usermodel_covered_bundles_v2_status_check,status = 'candidate_default_off'"`
	CreatedAt           time.Time          `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
	Snapshot            CoveredSnapshotRow `gorm:"belongsTo:true;foreignKey:AuthorityID,TenantID,SubjectID,SnapshotID;references:AuthorityID,TenantID,SubjectID,SnapshotID;constraint:OnDelete:RESTRICT"`
}

type SubjectRefProjectionRow struct {
	LegacyAuthorityID string          `gorm:"column:legacy_authority_id;type:text;primaryKey"`
	LegacyTenantID    string          `gorm:"column:legacy_tenant_id;type:text;primaryKey"`
	LegacySubjectID   string          `gorm:"column:legacy_subject_id;type:text;primaryKey"`
	Issuer            string          `gorm:"column:issuer;type:text;not null;uniqueIndex:usermodel_sr_v2_canonical_uq,priority:1;check:usermodel_sr_v2_identity_ck,issuer = 'rtw.identity' AND legacy_authority_id = issuer AND legacy_tenant_id = 'platform' AND legacy_subject_id = subject_id"`
	SubjectID         string          `gorm:"column:subject_id;type:text;not null;uniqueIndex:usermodel_sr_v2_canonical_uq,priority:2;check:usermodel_sr_v2_uid_ck,subject_id ~ '^[1-9][0-9]*$' AND (length(subject_id) < 19 OR (length(subject_id) = 19 AND subject_id <= '9223372036854775807'))"`
	ProjectedAt       time.Time       `gorm:"column:projected_at;type:timestamptz;not null;default:now()"`
	Subject           SubjectStateRow `gorm:"belongsTo:true;foreignKey:LegacyAuthorityID,LegacyTenantID,LegacySubjectID;references:AuthorityID,TenantID,SubjectID;constraint:OnDelete:RESTRICT"`
}

func SchemaModels() []any {
	return []any{
		&SubjectStateRow{},
		&EventRow{},
		&UnmappedEventRow{},
		&SubjectBindingRow{},
		&ActiveFactRow{},
		&AttributionRow{},
		&WatermarkRow{},
		&OutboxRow{},
		&CoveragePrefixRow{},
		&CoverageEventRow{},
		&CoverageSubjectRow{},
		&OntologyDefinitionRow{},
		&OntologyHeadRow{},
		&OntologyProjectionRow{},
		&FeatureBaselineRow{},
		&FeatureHeadRow{},
		&FeatureSnapshotRow{},
		&FeatureSnapshotVersionRow{},
		&ServingBundleRow{},
		&ServingPointerRow{},
		&CoveredBaselineRow{},
		&CoveredSnapshotRow{},
		&CoveredBundleCandidateRow{},
		&SubjectRefProjectionRow{},
	}
}

func MigrateSchema(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := database.AutoMigrate(ctx, tx, SchemaModels()...); err != nil {
			return err
		}
		return ensureDatabaseSchemaGuards(ctx, tx)
	})
}

func MigratePool(ctx context.Context, pool *pgxpool.Pool) error {
	return MigratePoolInSchema(ctx, pool, "")
}

// MigratePoolInSchema explicitly selects a possibly mixed-case schema for GORM
// initialization before any table name is resolved.
func MigratePoolInSchema(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	cfg := pool.Config()
	orm, err := database.OpenPgx(ctx, cfg.ConnConfig, database.Config{Schema: schema})
	if err != nil {
		return err
	}
	if sqlDB, dbErr := orm.DB(); dbErr == nil {
		defer sqlDB.Close()
	}
	return MigrateSchema(ctx, orm)
}

func ensureDatabaseSchemaGuards(ctx context.Context, db *gorm.DB) error {
	triggers := []struct{ function, name, table, message string }{
		{"usermodel_coverage_immutable", "usermodel_coverage_prefix_immutable", "usermodel_coverage_prefix", "verified coverage cache is immutable"},
		{"usermodel_coverage_immutable", "usermodel_coverage_event_immutable", "usermodel_coverage_event", "verified coverage cache is immutable"},
		{"usermodel_coverage_immutable", "usermodel_coverage_subject_immutable", "usermodel_coverage_subject", "verified coverage cache is immutable"},
		{"usermodel_reject_bundle_mutation", "usermodel_serving_bundles_immutable", "usermodel_serving_bundles", "serving bundle is immutable"},
		{"usermodel_covered_baseline_immutable", "usermodel_covered_baseline_immutable", "usermodel_covered_baselines_v2", "v2 covered baseline receipt is immutable"},
		{"usermodel_covered_snapshot_bundle_immutable", "usermodel_covered_snapshot_immutable", "usermodel_covered_snapshots_v2", "v2 covered snapshot and bundle candidates are immutable"},
		{"usermodel_covered_snapshot_bundle_immutable", "usermodel_covered_bundle_immutable", "usermodel_covered_bundle_candidates_v2", "v2 covered snapshot and bundle candidates are immutable"},
		{"usermodel_subjectref_v2_projection_immutable", "usermodel_subjectref_v2_projection_immutable", "usermodel_subjectref_v2_projection", "SubjectRef v2 projection is immutable"},
	}
	functions := map[string]string{}
	for _, trigger := range triggers {
		functions[trigger.function] = `CREATE OR REPLACE FUNCTION ` + trigger.function +
			`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION '` + trigger.message +
			`' USING ERRCODE='23514'; END $$`
	}
	for _, function := range []string{
		"usermodel_coverage_immutable", "usermodel_reject_bundle_mutation", "usermodel_covered_baseline_immutable",
		"usermodel_covered_snapshot_bundle_immutable", "usermodel_subjectref_v2_projection_immutable",
	} {
		if err := db.WithContext(ctx).Exec(functions[function]).Error; err != nil {
			return err
		}
	}
	for _, trigger := range triggers {
		if err := db.WithContext(ctx).Exec(`DROP TRIGGER IF EXISTS ` + trigger.name + ` ON ` + trigger.table).Error; err != nil {
			return err
		}
		if err := db.WithContext(ctx).Exec(`CREATE TRIGGER ` + trigger.name + ` BEFORE UPDATE OR DELETE ON ` + trigger.table +
			` FOR EACH ROW EXECUTE FUNCTION ` + trigger.function + `()`).Error; err != nil {
			return err
		}
	}
	return createSubjectRefViews(ctx, db)
}

func createSubjectRefViews(ctx context.Context, db *gorm.DB) error {
	views := []struct {
		name  string
		query *gorm.DB
	}{
		{"usermodel_subject_state_subjectref_v2", db.Table("usermodel_subjectref_v2_projection p").
			Select("p.issuer,p.subject_id,s.state_version,s.updated_at").
			Joins("JOIN usermodel_subject_state s ON (s.authority_id,s.tenant_id,s.subject_id)=(p.legacy_authority_id,p.legacy_tenant_id,p.legacy_subject_id)")},
		{"usermodel_events_subjectref_v2", db.Table("usermodel_subjectref_v2_projection p").
			Select("p.issuer,p.subject_id,e.producer,e.event_id,e.normalized_hash,e.event_body,e.action,e.semantic_kind,e.occurred_at,e.observed_at,e.source_partition,e.source_sequence,e.status,e.initial_version,e.accepted_version").
			Joins("JOIN usermodel_events e ON (e.authority_id,e.tenant_id,e.subject_id)=(p.legacy_authority_id,p.legacy_tenant_id,p.legacy_subject_id)")},
		{"usermodel_active_facts_subjectref_v2", db.Table("usermodel_subjectref_v2_projection p").
			Select("p.issuer,p.subject_id,a.producer,a.event_id,a.impression_id,a.activated_version").
			Joins("JOIN usermodel_active_facts a ON (a.authority_id,a.tenant_id,a.subject_id)=(p.legacy_authority_id,p.legacy_tenant_id,p.legacy_subject_id)")},
		{"usermodel_outbox_subjectref_v2", db.Table("usermodel_subjectref_v2_projection p").
			Select("p.issuer,p.subject_id,o.outbox_id,o.state_version,o.event_type,o.producer,o.event_id,o.payload,o.created_at,o.delivered_at").
			Joins("JOIN usermodel_outbox o ON (o.authority_id,o.tenant_id,o.subject_id)=(p.legacy_authority_id,p.legacy_tenant_id,p.legacy_subject_id)")},
	}
	for _, view := range views {
		if err := db.WithContext(ctx).Migrator().CreateView(view.name, gorm.ViewOption{Query: view.query, Replace: true}); err != nil {
			return err
		}
	}
	return nil
}

func (SubjectStateRow) TableName() string           { return "usermodel_subject_state" }
func (EventRow) TableName() string                  { return "usermodel_events" }
func (UnmappedEventRow) TableName() string          { return "usermodel_unmapped_events" }
func (SubjectBindingRow) TableName() string         { return "usermodel_subject_bindings" }
func (ActiveFactRow) TableName() string             { return "usermodel_active_facts" }
func (AttributionRow) TableName() string            { return "usermodel_attributions" }
func (WatermarkRow) TableName() string              { return "usermodel_watermarks" }
func (OutboxRow) TableName() string                 { return "usermodel_outbox" }
func (CoveragePrefixRow) TableName() string         { return "usermodel_coverage_prefix" }
func (CoverageEventRow) TableName() string          { return "usermodel_coverage_event" }
func (CoverageSubjectRow) TableName() string        { return "usermodel_coverage_subject" }
func (OntologyDefinitionRow) TableName() string     { return "usermodel_ontology_definitions" }
func (OntologyHeadRow) TableName() string           { return "usermodel_ontology_heads" }
func (OntologyProjectionRow) TableName() string     { return "usermodel_ontology_projections" }
func (FeatureBaselineRow) TableName() string        { return "usermodel_feature_baselines" }
func (FeatureHeadRow) TableName() string            { return "usermodel_feature_heads" }
func (FeatureSnapshotRow) TableName() string        { return "usermodel_feature_snapshots" }
func (FeatureSnapshotVersionRow) TableName() string { return "usermodel_feature_snapshot_versions" }
func (ServingBundleRow) TableName() string          { return "usermodel_serving_bundles" }
func (ServingPointerRow) TableName() string         { return "usermodel_serving_pointers" }
func (CoveredBaselineRow) TableName() string        { return "usermodel_covered_baselines_v2" }
func (CoveredSnapshotRow) TableName() string        { return "usermodel_covered_snapshots_v2" }
func (CoveredBundleCandidateRow) TableName() string { return "usermodel_covered_bundle_candidates_v2" }
func (SubjectRefProjectionRow) TableName() string   { return "usermodel_subjectref_v2_projection" }
