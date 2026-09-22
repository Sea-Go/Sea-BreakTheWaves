package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
)

func TestOpenUsesConfiguredSchemaOnEveryConnection(t *testing.T) {
	dsn := os.Getenv("DATABASE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DATABASE_TEST_POSTGRES_DSN unset; use an isolated PostgreSQL instance")
	}
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "GORM_Pool_" + hex.EncodeToString(nonce[:])
	quoted := `"` + schema + `"`
	db, err := Open(context.Background(), Config{
		DSN: dsn, Schema: schema, MaxOpenConns: 4, MaxIdleConns: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec("DROP SCHEMA " + quoted + " CASCADE").Error
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.Exec("CREATE TABLE pool_schema_check(id integer PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var current string
			if err := db.Raw("SELECT current_schema()").Scan(&current).Error; err != nil {
				t.Error(err)
				results <- ""
				return
			}
			results <- current
		}()
	}
	wg.Wait()
	close(results)
	for current := range results {
		if current != schema {
			t.Fatalf("pooled connection schema = %q, want %q", current, schema)
		}
	}
	var table *string
	if err := db.Raw("SELECT to_regclass('pool_schema_check')::text").Scan(&table).Error; err != nil || table == nil {
		t.Fatalf("schema-local table was not visible: table=%v err=%v", table, err)
	}
	readOnly, err := Open(context.Background(), Config{
		DSN: dsn, Schema: schema, ReadOnly: true, MaxOpenConns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := readOnly.Raw("SHOW transaction_read_only").Scan(&mode).Error; err != nil || mode != "on" {
		t.Fatalf("read-only connection mode=%q err=%v", mode, err)
	}
	if sqlDB, dbErr := readOnly.DB(); dbErr == nil {
		_ = sqlDB.Close()
	}
}
