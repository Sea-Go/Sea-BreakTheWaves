package preflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/usermodel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var schemaSource = "../schema.go"

func fixtureDB(t *testing.T) (*pgx.Conn, string) {
	t.Helper()
	dsn := os.Getenv("USERMODEL_PREFLIGHT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL fixture is not enabled")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect isolated PostgreSQL fixture")
	}
	schema := "um_srpf_" + time.Now().Format("150405") + "_" + strings.ReplaceAll(t.Name(), "/", "_")
	if _, err := conn.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal("create fixture schema", err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse isolated PostgreSQL fixture", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("open isolated PostgreSQL GORM fixture", err)
	}
	if err := usermodel.MigratePoolInSchema(ctx, pool, schema); err != nil {
		t.Fatal("apply isolated GORM schema fixture", err)
	}
	if _, err := conn.Exec(ctx, `SET search_path TO `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal("select fixture schema", err)
	}
	pool.Close()
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		_ = conn.Close(context.Background())
	})
	return conn, schema
}

func TestFrozenGORMCatalogAndReadOnlyEmptyAudit(t *testing.T) {
	conn, schema := fixtureDB(t)
	body, err := os.ReadFile(schemaSource)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, _ = h.Write(body)
	want, sourceSHA, err := Contract()
	if err != nil {
		t.Fatal(err)
	}
	if sourceSHA != hex.EncodeToString(h.Sum(nil)) {
		t.Fatal("frozen source hash differs from the GORM schema")
	}
	live, err := Inspect(context.Background(), conn, schema)
	if err != nil {
		t.Fatal(err)
	}
	if live.SHA256() != want.SHA256() {
		t.Fatal("isolated PG16 catalog differs from frozen GORM contract")
	}
	var stages []Observation
	report, err := Run(context.Background(), conn, schema, func(o Observation) { stages = append(stages, o) })
	if err != nil {
		t.Fatal(err)
	}
	if report.LiveCatalogSHA256 != want.SHA256() {
		t.Fatalf("Run catalog %s != contract %s", report.LiveCatalogSHA256, want.SHA256())
	}
	if report.L1+report.L2+report.L3 != 0 {
		t.Fatalf("empty GORM fixture has findings: %+v", report.Findings)
	}
	if report.Transaction != "repeatable_read/read_only" || len(stages) < 3 || report.ReportSHA256 == "" {
		t.Fatal("read-only audit contract is incomplete")
	}
	t.Logf("empty fixture reportSHA=%s contractSHA=%s liveCatalogSHA=%s L1=%d L2=%d L3=%d", report.ReportSHA256, report.ContractSHA256, report.LiveCatalogSHA256, report.L1, report.L2, report.L3)
}

func TestSyntheticAndCrossTenantRowsBlockProjection(t *testing.T) {
	conn, schema := fixtureDB(t)
	ctx := context.Background()
	_, err := conn.Exec(ctx, `INSERT INTO usermodel_subject_state(authority_id,tenant_id,subject_id,state_version)
		VALUES ('rtw.identity','platform','42',3),
		('rtw.identity','tenant-a','issued-user-1',0),
		('rtw.identity','tenant-a','42',0),
		('rtw.identity','platform','042',0),
		('rtw.identity','platform','0',0),
		('rtw.identity','platform','-1',0),
		('rtw.identity','platform','+1',0),
		('rtw.identity','platform','9223372036854775807',0),
		('rtw.identity','platform','9223372036854775808',0)`)
	if err != nil {
		t.Fatal(err)
	}
	// The legacy head has no FK after this deliberate schema-drift fixture.
	_, err = conn.Exec(ctx, `ALTER TABLE usermodel_feature_heads DROP CONSTRAINT fk_usermodel_feature_heads_baseline`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO usermodel_feature_heads(authority_id,tenant_id,subject_id,revision)
		VALUES('rtw.identity','platform','42',99)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO usermodel_feature_snapshot_versions(authority_id,tenant_id,subject_id,snapshot_id,snapshot_body)
		VALUES('rtw.identity','platform','42',repeat('a',64),'{}')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO usermodel_serving_bundles(authority_id,tenant_id,subject_id,bundle_id,pair_id,space_id,feature_snapshot_id,bundle_body)
		VALUES('rtw.identity','platform','42',repeat('b',64),'pair-one','space',repeat('a',64),'{}')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO usermodel_serving_pointers(authority_id,tenant_id,subject_id,pair_id,bundle_id,state,approval_ref,approval_revision,pointer_version)
		VALUES('rtw.identity','platform','42','pair-two',repeat('b',64),'active','fixture',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM usermodel_subject_state`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	first, err := Run(ctx, conn, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(ctx, conn, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReportSHA256 != second.ReportSHA256 {
		t.Fatal("report content hash changed on the same PG snapshot")
	}
	for _, rule := range []string{"invalid_legacy_subject", "projected_unique_collision:usermodel_subject_state_pkey",
		"missing_or_changed_constraint", "orphan_expected_fk:", "outbox_version_gap", "serving_pointer_pair_owner"} {
		if !hasRule(first, rule) {
			t.Fatalf("negative fixture did not detect %s: %+v", rule, first.Findings)
		}
	}
	if first.L1 == 0 || first.L2 == 0 {
		t.Fatal("negative fixture did not block L1 and L2")
	}
	if first.TotalRows != 13 || len(first.TableRows) != 23 {
		t.Fatal("negative fixture scoped table row scan changed")
	}
	if findingCount(first, "invalid_legacy_subject", "usermodel_subject_state") != 7 {
		t.Fatal("canonical positive int64 boundary was not enforced")
	}
	encoded, _ := json.Marshal(first)
	for _, private := range []string{"tenant-a", "issued-user-1", "event_body", "payload", "postgres://"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("report exposed %s", private)
		}
	}
	var after int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM usermodel_subject_state`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("preflight changed fixture rows")
	}
	t.Logf("negative fixture reportSHA=%s liveCatalogSHA=%s rows=%d L1=%d L2=%d L3=%d", first.ReportSHA256, first.LiveCatalogSHA256, first.TotalRows, first.L1, first.L2, first.L3)
}

func hasRule(r Report, prefix string) bool {
	for _, f := range r.Findings {
		if strings.HasPrefix(f.Rule, prefix) {
			return true
		}
	}
	return false
}

func findingCount(r Report, rule, table string) int64 {
	for _, f := range r.Findings {
		if f.Rule == rule && f.Table == table {
			return f.Count
		}
	}
	return 0
}
