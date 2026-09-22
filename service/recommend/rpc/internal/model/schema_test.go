package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
)

func TestPostgresGORMSchemaIdempotent(t *testing.T) {
	dsn := os.Getenv("RECOMMEND_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("RECOMMEND_TEST_POSTGRES_DSN unset; use an isolated PostgreSQL instance")
	}
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "recommend_gorm_" + hex.EncodeToString(nonce[:])
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{DSN: dsn, Schema: schema, MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.WithContext(context.Background()).Exec("DROP SCHEMA " + `"` + schema + `"` + " CASCADE").Error
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	}()
	if err := MigrateSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(ctx, db); err != nil {
		t.Fatalf("schema bootstrap replay: %v", err)
	}
	var tables, triggers, foreignKeys int
	if err := db.Raw(`SELECT count(*) FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=? AND c.relkind='r'`, schema).Scan(&tables).Error; err != nil || tables != 6 {
		t.Fatalf("table count=%d err=%v", tables, err)
	}
	if err := db.Raw(`SELECT count(*) FROM pg_catalog.pg_trigger x
		JOIN pg_catalog.pg_class c ON c.oid=x.tgrelid
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=? AND NOT x.tgisinternal AND x.tgname LIKE 'recommend_%_immutable'`,
		schema).Scan(&triggers).Error; err != nil || triggers != 4 {
		t.Fatalf("immutable trigger count=%d err=%v", triggers, err)
	}
	if err := db.Raw(`SELECT count(*) FROM pg_catalog.pg_constraint x
		JOIN pg_catalog.pg_class c ON c.oid=x.conrelid
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=? AND x.contype='f'`, schema).Scan(&foreignKeys).Error; err != nil || foreignKeys != 8 {
		t.Fatalf("foreign key count=%d err=%v", foreignKeys, err)
	}
}
