package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/recommend"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	recommendmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/recommend"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRTWRealItemPoolHandoff is the BTW half of an optional RTW-owned real
// publication/PG acceptance. An RTW test first manually publishes the content
// and passes its isolated Worker URL and PostgreSQL DSN in a private file.
func TestRTWRealItemPoolHandoff(t *testing.T) {
	path := os.Getenv("SEA_RTW_ITEM_POOL_FIXTURE")
	if path == "" {
		t.Skip("requires RTW current publication and isolated PostgreSQL fixture")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("RTW item fixture must be a regular private file: %v", err)
	}
	var fixture struct {
		RTWBase     string `json:"rtw_base"`
		WorkerToken string `json:"worker_token"`
		PGDSN       string `json:"pg_dsn"`
		ModuleID    string `json:"module_id"`
		RevisionID  string `json:"revision_id"`
		ItemID      string `json:"item_id"`
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &fixture) != nil || fixture.RTWBase == "" ||
		fixture.WorkerToken == "" || fixture.PGDSN == "" || fixture.ModuleID == "" ||
		fixture.RevisionID == "" || fixture.ItemID == "" {
		t.Fatal("incomplete RTW real item pool fixture")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, fixture.PGDSN)
	if err != nil {
		t.Fatal("open isolated PostgreSQL admin connection")
	}
	defer admin.Close()
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "recommend_rtw_" + hex.EncodeToString(nonce[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	}()
	config, err := pgxpool.ParseConfig(fixture.PGDSN)
	if err != nil {
		t.Fatal("parse isolated PostgreSQL DSN")
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, recommendmigration.SQL); err != nil {
		t.Fatal(err)
	}
	client, err := ridethewind.New(httpclient.Config{BaseURL: fixture.RTWBase, Token: fixture.WorkerToken})
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewRTWItemSource(client)
	if err != nil {
		t.Fatal(err)
	}
	store, err := recommend.NewStore(pool, source)
	if err != nil {
		t.Fatal(err)
	}
	release, err := store.Rebuild(ctx, fixture.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.Candidates(ctx, fixture.ModuleID, recommend.MaxCandidates)
	if err != nil || page.PoolReleaseID != release.ID || page.FeatureHash != release.FeatureHash ||
		!reflect.DeepEqual(page.Publication, release.Publication) || page.BehaviorState != "not_connected" {
		t.Fatalf("real RTW publication did not become fixed PG candidate pool: %+v %v", page, err)
	}
	var found bool
	for _, candidate := range page.Candidates {
		if candidate.Item.ItemID == fixture.ItemID && candidate.Item.RevisionID == fixture.RevisionID &&
			candidate.Sources[0] == "new_content" {
			found = true
		}
	}
	if !found {
		t.Fatalf("RTW published item %s/%s missing from bounded pool", fixture.ItemID, fixture.RevisionID)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM recommend_pool_releases WHERE pool_release_id=$1`, release.ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("immutable RTW item release not committed once: %d %v", rows, err)
	}
}
