package wikiqualitysource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func v2TestSHA(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func v2DisposablePool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("WIKI_QUALITY_SCHEMA_V2_TEST_DSN")
	if dsn == "" {
		t.Skip("requires task-owned PG16 from schema-v2-acceptance.sh")
	}
	if os.Getenv("WIKI_QUALITY_SCHEMA_V2_DISPOSABLE") != "1" {
		t.Fatal("FactSet schema migration test needs explicit disposable-PG flag")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || config.ConnConfig.Host != "127.0.0.1" {
		t.Fatal("FactSet schema test DSN must be task-owned loopback PG")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	var version int
	if err := db.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&version); err != nil ||
		version/10000 != 16 {
		t.Fatal("FactSet schema contract acceptance must use PostgreSQL 16")
	}
	return ctx, db
}

func v2InstallOriginalV1(t *testing.T, ctx context.Context, db *pgxpool.Pool) {
	t.Helper()
	if _, err := db.Exec(ctx, `DROP SCHEMA IF EXISTS warehouse_wiki_quality CASCADE`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/ods-v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, string(raw), pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatalf("fixed v1 Wiki ODS fixture rejected: %v", err)
	}
}

func v2InsertV1Rows(t *testing.T, ctx context.Context, db *pgxpool.Pool) ([]byte, []byte, []byte) {
	t.Helper()
	techSpec, qualitySpec, qualityOriginal := []byte(`{"event":"old-technical"}`),
		[]byte(`{"event":"old-quality"}`), []byte(`{"source_quote":"historic fact"}`)
	if _, err := db.Exec(ctx, `INSERT INTO warehouse_wiki_quality.consumer_cursor
        (consumer,producer,committed_offset) VALUES($1,$2,2)`, DefaultConsumer, Producer); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct {
		offset     int64
		id, status string
		eventSpec  []byte
		original   []byte
	}{
		{1, "old-tech-1", "technical_skip", techSpec, nil},
		{2, "old-quality-2", "quality_verified", qualitySpec, qualityOriginal},
	} {
		var originalSHA any
		if spec.original != nil {
			originalSHA = v2TestSHA(spec.original)
		}
		if _, err := db.Exec(ctx, `INSERT INTO warehouse_wiki_quality.ods_event
            (producer,source_offset,event_id,event_type,event_spec,dc_input_hash,dc_receipt,
             dc_received_at,status,authority_event_json,authority_event_sha256)
            VALUES($1,$2,$3,$4,$5,$6,$7,now(),$8,$9,$10)`,
			Producer, spec.offset, spec.id, "knowledge.wiki.quality.fixture.v1",
			spec.eventSpec, v2TestSHA(spec.eventSpec), []byte(`{"receipt":"old"}`),
			spec.status, spec.original, originalSHA); err != nil {
			t.Fatalf("old accepted ODS v1 row rejected: %v", err)
		}
	}
	return techSpec, qualitySpec, qualityOriginal
}

func v2TriggerOID(t *testing.T, ctx context.Context, db *pgxpool.Pool, table, name string) uint32 {
	t.Helper()
	var oid uint32
	if err := db.QueryRow(ctx, `SELECT oid FROM pg_trigger
        WHERE tgrelid=to_regclass($1) AND tgname=$2`, table, name).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	return oid
}

func TestFactSetSchemaV1RejectsFreshDDLAndExplicitMigrationKeepsOldPrefix(t *testing.T) {
	ctx, db := v2DisposablePool(t)
	v2InstallOriginalV1(t, ctx, db)
	tech, quality, original := v2InsertV1Rows(t, ctx, db)
	if err := CheckFactSetSchema(ctx, db); !errors.Is(err, ErrFactSetSchema) {
		t.Fatalf("v1 schema posed as strong FactSet ODS: %v", err)
	}
	if err := Initialize(ctx, db); err == nil {
		t.Fatal("fresh schema DDL manufactured half-v2 from an old ODS parent")
	}
	if schemaCondition(ctx, db, `SELECT to_regclass($1) IS NOT NULL`, odsFactSetTable) {
		t.Fatal("fresh DDL created an orphan sidecar before explicit migration")
	}
	var beforeCursor int64
	if err := db.QueryRow(ctx, `SELECT committed_offset FROM warehouse_wiki_quality.consumer_cursor
        WHERE consumer=$1`, DefaultConsumer).Scan(&beforeCursor); err != nil || beforeCursor != 2 {
		t.Fatalf("old committed cursor changed before migration: %d %v", beforeCursor, err)
	}
	if err := ApplyFactSetMigration(ctx, db); err != nil || CheckFactSetSchema(ctx, db) != nil {
		t.Fatalf("explicit FactSet v1→v2 upgrade failed: %v", err)
	}
	firstOID := v2TriggerOID(t, ctx, db, odsEventTable, "ods_event_fact_set_sidecar_required")
	childOID := v2TriggerOID(t, ctx, db, odsFactSetTable, "ods_fact_set_parent_check")
	if err := ApplyFactSetMigration(ctx, db); err != nil {
		t.Fatalf("second explicit FactSet migration was not reentrant: %v", err)
	}
	if firstOID != v2TriggerOID(t, ctx, db, odsEventTable, "ods_event_fact_set_sidecar_required") ||
		childOID != v2TriggerOID(t, ctx, db, odsFactSetTable, "ods_fact_set_parent_check") {
		t.Fatal("second FactSet migration rebuilt protected PG triggers")
	}
	var gotTech, gotQuality, gotOriginal, gotReceipt []byte
	var cursor, factRows int64
	var oldInputHash, oldStatus, oldOriginalSHA string
	var oldPayloadSHA *string
	if err := db.QueryRow(ctx, `SELECT event_spec,dc_receipt FROM warehouse_wiki_quality.ods_event
        WHERE producer=$1 AND source_offset=1`, Producer).Scan(&gotTech, &gotReceipt); err != nil ||
		!bytes.Equal(gotTech, tech) || !bytes.Equal(gotReceipt, []byte(`{"receipt":"old"}`)) {
		t.Fatalf("old technical skip bytes changed during migration: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT event_spec,authority_event_json,dc_input_hash,status,
        authority_event_sha256,fact_set_payload_jcs_sha256
        FROM warehouse_wiki_quality.ods_event WHERE producer=$1 AND source_offset=2`, Producer).
		Scan(&gotQuality, &gotOriginal, &oldInputHash, &oldStatus, &oldOriginalSHA, &oldPayloadSHA); err != nil ||
		!bytes.Equal(gotQuality, quality) || !bytes.Equal(gotOriginal, original) ||
		oldInputHash != v2TestSHA(quality) || oldStatus != "quality_verified" ||
		oldOriginalSHA != v2TestSHA(original) || oldPayloadSHA != nil {
		t.Fatalf("historic quality Event raw/JCS/status was rewritten: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT committed_offset FROM warehouse_wiki_quality.consumer_cursor
        WHERE consumer=$1`, DefaultConsumer).Scan(&cursor); err != nil || cursor != 2 {
		t.Fatalf("FactSet migration advanced old DC prefix: %d %v", cursor, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM warehouse_wiki_quality.ods_fact_set`).Scan(&factRows); err != nil || factRows != 0 {
		t.Fatalf("v2 sidecar backfilled synthetic FactSets: %d %v", factRows, err)
	}
}

func v2InsertFactParent(ctx context.Context, tx pgx.Tx, offset int64, payloadSHA string) error {
	_, err := tx.Exec(ctx, `INSERT INTO warehouse_wiki_quality.ods_event
        (producer,source_offset,event_id,event_type,event_spec,dc_input_hash,dc_receipt,
         dc_received_at,status,authority_event_json,authority_event_sha256,fact_set_payload_jcs_sha256)
        VALUES($1,$2,$3,$4,$5,$6,$7,now(),'fact_set_verified',$8,$9,$10)`,
		Producer, offset, "catalog-"+strconv.FormatInt(offset, 10),
		"knowledge.wiki.fact-set.frozen.v1", []byte(`{"catalog":"fixture"}`),
		strings.Repeat("1", 64), []byte(`{"receipt":"fixture"}`),
		[]byte(`{"original":"fixture"}`), strings.Repeat("2", 64), payloadSHA)
	return err
}

func v2InsertFactSidecar(ctx context.Context, tx pgx.Tx, offset int64, payloadSHA string) error {
	_, err := tx.Exec(ctx, `INSERT INTO warehouse_wiki_quality.ods_fact_set
        (producer,source_offset,fact_set_id,revision_id,revision,base_revision_id,
         wiki_revision_id,source_scope_revision,payload_jcs,payload_jcs_sha256)
        VALUES($1,$2,'fact-set-1','fact-set-revision-1',1,'','wiki-revision-1',$3,$4,$5)`,
		Producer, offset, "scope_"+strings.Repeat("3", 64), []byte(`{"synthetic":true}`), payloadSHA)
	return err
}

func TestFactSetSchemaParentSidecarCASAndFailureRemainOneTransaction(t *testing.T) {
	ctx, db := v2DisposablePool(t)
	v2InstallOriginalV1(t, ctx, db)
	_, _, _ = v2InsertV1Rows(t, ctx, db)
	if err := ApplyFactSetMigration(ctx, db); err != nil {
		t.Fatal(err)
	}
	goodPayloadSHA := v2TestSHA([]byte(`{"synthetic":true}`))
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := v2InsertFactParent(ctx, tx, 3, goodPayloadSHA); err != nil {
		t.Fatal(err)
	}
	if err := v2InsertFactSidecar(ctx, tx, 3, strings.Repeat("9", 64)); err == nil {
		t.Fatal("sidecar SHA mismatch crossed parent-status trigger")
	}
	_ = tx.Rollback(ctx)
	var parents, children, cursor int64
	if err := db.QueryRow(ctx, `SELECT count(*) FROM warehouse_wiki_quality.ods_event
        WHERE source_offset=3`).Scan(&parents); err != nil || parents != 0 {
		t.Fatalf("bad sidecar left a verified parent: %d %v", parents, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM warehouse_wiki_quality.ods_fact_set`).Scan(&children); err != nil || children != 0 {
		t.Fatalf("bad sidecar left an orphan: %d %v", children, err)
	}
	if err := db.QueryRow(ctx, `SELECT committed_offset FROM warehouse_wiki_quality.consumer_cursor
        WHERE consumer=$1`, DefaultConsumer).Scan(&cursor); err != nil || cursor != 2 {
		t.Fatalf("bad sidecar advanced old DC cursor: %d %v", cursor, err)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := v2InsertFactParent(ctx, tx, 3, goodPayloadSHA); err != nil {
		t.Fatal(err)
	}
	if err := v2InsertFactSidecar(ctx, tx, 3, goodPayloadSHA); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("valid parent and sidecar did not commit together: %v", err)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := v2InsertFactParent(ctx, tx, 4, goodPayloadSHA); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("deferred parent check allowed a verified FactSet without sidecar")
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM warehouse_wiki_quality.ods_event
        WHERE source_offset=4`).Scan(&parents); err != nil || parents != 0 {
		t.Fatalf("orphan parent survived deferred-constraint rollback: %d %v", parents, err)
	}
}

func TestFactSetSchemaRejectsHalfOrWeakUpgradeWithoutRepair(t *testing.T) {
	ctx, db := v2DisposablePool(t)
	v2InstallOriginalV1(t, ctx, db)
	_, _, _ = v2InsertV1Rows(t, ctx, db)
	if _, err := db.Exec(ctx, `ALTER TABLE warehouse_wiki_quality.ods_event
        ADD COLUMN fact_set_payload_jcs_sha256 text`); err != nil {
		t.Fatal(err)
	}
	if err := CheckFactSetSchema(ctx, db); !errors.Is(err, ErrFactSetSchema) {
		t.Fatalf("only a payload column passed strong startup probe: %v", err)
	}
	if err := ApplyFactSetMigration(ctx, db); !errors.Is(err, ErrFactSetSchema) {
		t.Fatalf("partial v2 column was repaired behind operator's back: %v", err)
	}
	if _, err := db.Exec(ctx, factSetMigrationSQL, pgx.QueryExecModeSimpleProtocol); err == nil {
		t.Fatal("direct migration silently accepted a half-extended v2 parent")
	}
	if schemaCondition(ctx, db, `SELECT to_regclass($1) IS NOT NULL`, odsFactSetTable) {
		t.Fatal("half-upgrade created a sidecar despite explicit rejection")
	}
	v2InstallOriginalV1(t, ctx, db)
	if err := ApplyFactSetMigration(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE warehouse_wiki_quality.ods_fact_set
        DROP CONSTRAINT ods_fact_set_event_fk`); err != nil {
		t.Fatal(err)
	}
	if err := CheckFactSetSchema(ctx, db); !errors.Is(err, ErrFactSetSchema) {
		t.Fatalf("weak v2 sidecar without parent FK passed startup: %v", err)
	}
	if err := ApplyFactSetMigration(ctx, db); !errors.Is(err, ErrFactSetSchema) {
		t.Fatalf("weak FK was re-created by implicit migration: %v", err)
	}
	if _, err := db.Exec(ctx, factSetMigrationSQL, pgx.QueryExecModeSimpleProtocol); err == nil {
		t.Fatal("direct migration accepted a weak v2 missing its parent FK")
	}
}
