package wikiqualitysource

import (
	"context"
	_ "embed"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrate-fact-set-ods.sql
var factSetMigrationSQL string

var ErrFactSetSchema = errors.New("Wiki FactSet ODS v2 schema is missing, weak or partially applied")

const odsEventTable = "warehouse_wiki_quality.ods_event"
const odsFactSetTable = "warehouse_wiki_quality.ods_fact_set"

func schemaCondition(ctx context.Context, db *pgxpool.Pool, query string, args ...any) bool {
	var ok bool
	return db.QueryRow(ctx, query, args...).Scan(&ok) == nil && ok
}

func schemaConstraint(ctx context.Context, db *pgxpool.Pool,
	table, name, kind string, words ...string) bool {
	var def string
	if db.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint
        WHERE conrelid=to_regclass($1) AND conname=$2 AND contype=$3 AND convalidated`,
		table, name, kind).Scan(&def) != nil {
		return false
	}
	for _, word := range words {
		if !strings.Contains(def, word) {
			return false
		}
	}
	return true
}

func schemaTrigger(ctx context.Context, db *pgxpool.Pool,
	table, name string, deferred bool, words ...string) bool {
	var def string
	var isDeferred, initiallyDeferred bool
	if db.QueryRow(ctx, `SELECT pg_get_triggerdef(oid),tgdeferrable,tginitdeferred
        FROM pg_trigger WHERE tgrelid=to_regclass($1) AND tgname=$2
        AND NOT tgisinternal AND tgenabled='O'`, table, name).
		Scan(&def, &isDeferred, &initiallyDeferred) != nil ||
		deferred && (!isDeferred || !initiallyDeferred) ||
		!deferred && (isDeferred || initiallyDeferred) {
		return false
	}
	for _, word := range words {
		if !strings.Contains(def, word) {
			return false
		}
	}
	return true
}

func schemaColumn(ctx context.Context, db *pgxpool.Pool,
	table, name, typ string, notNull bool) bool {
	return schemaCondition(ctx, db, `SELECT EXISTS (
        SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass($1)
        AND attname=$2 AND attnum>0 AND NOT attisdropped
        AND format_type(atttypid,atttypmod)=$3 AND attnotnull=$4)`,
		table, name, typ, notNull)
}

// CheckFactSetSchema is read-only and must run before an enabled FactSet
// consumer reads DC or ACKs any position. It validates the explicit v2 column,
// named three-state evidence constraints, sidecar shape/FK/unique keys, and
// both parent/sidecar append-only and payload-link triggers.
func CheckFactSetSchema(ctx context.Context, db *pgxpool.Pool) error {
	if ctx == nil || db == nil || ctx.Err() != nil ||
		!schemaCondition(ctx, db, `SELECT to_regclass($1) IS NOT NULL
            AND to_regclass($2) IS NOT NULL AND to_regclass($3) IS NOT NULL`,
			odsEventTable, odsFactSetTable, "warehouse_wiki_quality.consumer_cursor") ||
		!schemaColumn(ctx, db, odsEventTable, "fact_set_payload_jcs_sha256", "text", false) ||
		!schemaConstraint(ctx, db, odsEventTable, "ods_event_status_v2_check", "c",
			"quality_verified", "technical_skip", "fact_set_verified") ||
		!schemaConstraint(ctx, db, odsEventTable, "ods_event_evidence_v2_check", "c",
			"technical_skip", "quality_verified", "fact_set_verified",
			"authority_event_json", "authority_event_sha256",
			"fact_set_payload_jcs_sha256", "IS NULL", "IS NOT NULL", "^[a-f0-9]{64}$") ||
		schemaCondition(ctx, db, `SELECT EXISTS (SELECT 1 FROM pg_constraint
            WHERE conrelid=to_regclass($1) AND conname IN
            ('ods_event_status_check','ods_event_check'))`, odsEventTable) ||
		!schemaTrigger(ctx, db, odsEventTable, "wiki_quality_ods_immutable", false,
			"BEFORE", "UPDATE", "DELETE", "reject_ods_rewrite") ||
		!schemaTrigger(ctx, db, odsEventTable, "ods_event_fact_set_sidecar_required", true,
			"AFTER INSERT", "require_fact_set_sidecar") {
		return ErrFactSetSchema
	}
	columns := []struct{ name, typ string }{
		{"producer", "text"}, {"source_offset", "bigint"},
		{"fact_set_id", "text"}, {"revision_id", "text"},
		{"revision", "bigint"}, {"base_revision_id", "text"},
		{"wiki_revision_id", "text"}, {"source_scope_revision", "text"},
		{"payload_jcs", "bytea"}, {"payload_jcs_sha256", "text"},
	}
	for _, col := range columns {
		if !schemaColumn(ctx, db, odsFactSetTable, col.name, col.typ, true) {
			return ErrFactSetSchema
		}
	}
	if !schemaCondition(ctx, db, `SELECT (
        SELECT count(*) FROM pg_attribute WHERE attrelid=to_regclass($1)
        AND attnum>0 AND NOT attisdropped)=10`, odsFactSetTable) ||
		!schemaConstraint(ctx, db, odsFactSetTable, "ods_fact_set_pkey", "p",
			"PRIMARY KEY (producer, source_offset)") ||
		!schemaConstraint(ctx, db, odsFactSetTable, "ods_fact_set_event_fk", "f",
			"FOREIGN KEY (producer, source_offset)", "ods_event", "producer", "source_offset") ||
		!schemaCondition(ctx, db, `SELECT EXISTS (SELECT 1 FROM pg_constraint
        WHERE conrelid=to_regclass($1) AND conname='ods_fact_set_event_fk'
        AND contype='f' AND confrelid=to_regclass($2) AND convalidated)`,
			odsFactSetTable, odsEventTable) ||
		!schemaCondition(ctx, db, `SELECT count(*)=2 AND
        count(*) FILTER (WHERE pg_get_constraintdef(oid) LIKE '%UNIQUE (producer, revision_id)%')=1 AND
        count(*) FILTER (WHERE pg_get_constraintdef(oid) LIKE '%UNIQUE (producer, fact_set_id, revision)%')=1
        FROM pg_constraint WHERE conrelid=to_regclass($1) AND contype='u' AND convalidated`,
			odsFactSetTable) ||
		!schemaConstraint(ctx, db, odsFactSetTable, "ods_fact_set_offset_check", "c", "source_offset", "> 0") ||
		!schemaConstraint(ctx, db, odsFactSetTable, "ods_fact_set_revision_check", "c", "revision", "> 0") ||
		!schemaConstraint(ctx, db, odsFactSetTable, "ods_fact_set_scope_check", "c", "scope_", "[a-f0-9]{64}") ||
		!schemaConstraint(ctx, db, odsFactSetTable, "ods_fact_set_payload_nonempty_check", "c",
			"octet_length", "payload_jcs", "> 0") ||
		!schemaConstraint(ctx, db, odsFactSetTable, "ods_fact_set_payload_sha_check", "c",
			"payload_jcs_sha256", "^[a-f0-9]{64}$") ||
		!schemaConstraint(ctx, db, odsFactSetTable, "ods_fact_set_identity_check", "c",
			"fact_set_id", "revision_id", "wiki_revision_id") ||
		!schemaTrigger(ctx, db, odsFactSetTable, "wiki_fact_set_ods_immutable", false,
			"BEFORE", "UPDATE", "DELETE", "reject_ods_rewrite") ||
		!schemaTrigger(ctx, db, odsFactSetTable, "ods_fact_set_parent_check", false,
			"BEFORE INSERT", "check_fact_set_parent") {
		return ErrFactSetSchema
	}
	return nil
}

func checkOriginalV1Schema(ctx context.Context, db *pgxpool.Pool) error {
	if !schemaCondition(ctx, db, `SELECT to_regclass($1) IS NOT NULL
        AND to_regclass($2) IS NULL AND to_regclass($3) IS NOT NULL`,
		odsEventTable, odsFactSetTable, "warehouse_wiki_quality.consumer_cursor") ||
		schemaCondition(ctx, db, `SELECT EXISTS (SELECT 1 FROM pg_attribute
        WHERE attrelid=to_regclass($1) AND attname='fact_set_payload_jcs_sha256'
        AND attnum>0 AND NOT attisdropped)`, odsEventTable) ||
		!schemaConstraint(ctx, db, odsEventTable, "ods_event_status_check", "c",
			"quality_verified", "technical_skip") ||
		!schemaConstraint(ctx, db, odsEventTable, "ods_event_check", "c",
			"authority_event_json", "authority_event_sha256") ||
		!schemaTrigger(ctx, db, odsEventTable, "wiki_quality_ods_immutable", false,
			"BEFORE", "UPDATE", "DELETE", "reject_ods_rewrite") ||
		schemaCondition(ctx, db, `SELECT EXISTS (SELECT 1 FROM pg_constraint
        WHERE conrelid=to_regclass($1) AND conname='ods_event_status_check'
        AND position('fact_set_verified' in pg_get_constraintdef(oid))>0)`, odsEventTable) ||
		schemaCondition(ctx, db, `SELECT EXISTS (SELECT 1 FROM pg_constraint
        WHERE conrelid=to_regclass($1) AND conname IN
        ('ods_event_status_v2_check','ods_event_evidence_v2_check'))`, odsEventTable) ||
		schemaCondition(ctx, db, `SELECT EXISTS (SELECT 1 FROM warehouse_wiki_quality.ods_event
        WHERE status='quality_verified' AND
        (authority_event_sha256 IS NULL OR authority_event_sha256 !~ '^[a-f0-9]{64}$'))`) {
		return ErrFactSetSchema
	}
	return nil
}

// ApplyFactSetMigration is an explicit operator/test action, never called by
// the runtime's read path. Complete v2 is a no-op; anything except complete
// source v1 is rejected before DDL. The SQL itself is reentrant as well.
func ApplyFactSetMigration(ctx context.Context, db *pgxpool.Pool) error {
	if ctx == nil || db == nil || ctx.Err() != nil {
		return ErrFactSetSchema
	}
	if CheckFactSetSchema(ctx, db) == nil {
		return nil
	}
	if err := checkOriginalV1Schema(ctx, db); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, factSetMigrationSQL, pgx.QueryExecModeSimpleProtocol); err != nil {
		return errors.Join(ErrFactSetSchema, err)
	}
	return CheckFactSetSchema(ctx, db)
}
