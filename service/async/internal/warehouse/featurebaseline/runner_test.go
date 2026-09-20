package featurebaseline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	usermodelmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/usermodel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testRunner(t *testing.T) (Runner, usermodel.SubjectRef) {
	t.Helper()
	dsn, chURL, s3URL, dbt := os.Getenv("USERMODEL_TEST_POSTGRES_DSN"), os.Getenv("USERMODEL_TEST_CH_URL"), os.Getenv("USERMODEL_TEST_S3_PREFIX"), os.Getenv("USERMODEL_TEST_DBT")
	if dsn == "" || chURL == "" || s3URL == "" || dbt == "" {
		t.Skip("run warehouse/feature_baselines/acceptance.py for isolated PG/CH/S3/dbt")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "feature_handoff_" + hex.EncodeToString([]byte(time.Now().UTC().Format("150405.000000")))
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	if _, err := pool.Exec(ctx, usermodelmigration.SQL); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../../../migrations/usermodel/002_ontology.sql", "../../../migrations/usermodel/003_features.sql"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "../../.."))
	r := Runner{Source: usermodel.NewStore(pool, nil), ClickHouse: chURL, S3Prefix: s3URL, DBT: dbt,
		ProjectDir: filepath.Join(root, "warehouse/feature_baselines"), ProfilesDir: filepath.Join(root, "warehouse/environment"), WorkDir: t.TempDir()}
	return r, usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
}

func featureEvent(subject usermodel.SubjectRef, id string, sequence int64, occurred time.Time) usermodel.Event {
	sum := sha256.Sum256([]byte(id))
	return usermodel.Event{Subject: subject, EventKey: usermodel.EventKey{Producer: "rtw.product", EventID: id},
		Action: usermodel.Assert, Kind: usermodel.Reading, Predicate: "read", ValueRef: "article/known",
		EvidenceRef: "rtw/event/" + id, EvidenceHash: hex.EncodeToString(sum[:]), OccurredAt: occurred,
		ObservedAt: occurred, SourcePartition: "product-1", SourceSequence: sequence, ItemID: "article-1"}
}

func featureSpec() usermodel.FeatureSpec {
	return usermodel.FeatureSpec{Version: "h10c-reading-v1", Features: []usermodel.FeatureDefinition{
		{Name: "reading_count", Source: "fact", Kind: usermodel.Reading, Predicate: "read", Mode: "count", WindowSeconds: 3600, Default: "0"},
		{Name: "recent_article", Source: "fact", Kind: usermodel.Reading, Predicate: "read", Mode: "latest", WindowSeconds: 3600,
			Default: "unknown", Vocabulary: []string{"article/known"}, OOV: "other"},
	}}
}

func value(snapshot usermodel.FeatureSnapshot, key string) usermodel.FeatureValue {
	for _, v := range snapshot.Values {
		if v.Name == key {
			return v
		}
	}
	return usermodel.FeatureValue{}
}

func TestRealWarehouseFeatureGenerationAndUserModelHandoff(t *testing.T) {
	r, subject := testRunner(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-25 * time.Minute)
	for _, e := range []usermodel.Event{featureEvent(subject, "read-1", 1, at), featureEvent(subject, "read-3", 3, at.Add(time.Minute))} {
		if receipt, err := r.Source.Append(ctx, e); err != nil || receipt.Status != "accepted" {
			t.Fatalf("append %+v: %v", receipt, err)
		}
	}
	m1, err := r.Build(ctx, subject, 1, "h10c_handoff_g1", featureSpec())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("DWS generation=%s source=%s values=%s baseline=%s dbt_manifest=%s", m1.Generation, m1.SourceSHA256, m1.DWSValuesSHA256, m1.BaselineSHA256, m1.DBTManifestSHA256)
	if m1.ContributionCount != 1 || len(m1.Watermarks) != 1 || m1.Watermarks[0].ContiguousSequence != 1 || m1.InputStateVersion != 2 {
		t.Fatalf("gap falsely covered: %+v", m1)
	}
	receipt, snapshot, err := r.Accept(ctx, m1, featureSpec())
	if err != nil || receipt.Replay || snapshot.BaselineState != "accepted" || value(snapshot, "reading_count").Value != "2" || len(snapshot.Tail) != 1 {
		t.Fatalf("first DWS->PG handoff: %+v %+v %v", receipt, snapshot, err)
	}
	if replay, _, err := r.Accept(ctx, m1, featureSpec()); err != nil || !replay.Replay {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if _, err := r.Source.Append(ctx, featureEvent(subject, "read-2", 2, at.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if tail, err := r.Source.RefreshFeatureSnapshot(ctx, subject, featureSpec(), now, now); err != nil || value(tail, "reading_count").Value != "3" || len(tail.Tail) != 2 {
		t.Fatalf("gap closure tail: %+v %v", tail, err)
	}
	// An already accepted fact with a future business time must remain beyond
	// the warehouse coverage prefix, so it can enter the near-line tail later.
	future := featureEvent(subject, "read-future", 4, time.Now().UTC().Add(20*time.Minute).Truncate(time.Microsecond))
	if _, err := r.Source.Append(ctx, future); err != nil {
		t.Fatal(err)
	}
	m2, err := r.Build(ctx, subject, 2, "h10c_handoff_g2", featureSpec())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("DWS generation=%s source=%s values=%s baseline=%s dbt_manifest=%s", m2.Generation, m2.SourceSHA256, m2.DWSValuesSHA256, m2.BaselineSHA256, m2.DBTManifestSHA256)
	if m2.ContributionCount != 3 || m2.Watermarks[0].ContiguousSequence != 3 || m2.InputStateVersion != 4 {
		t.Fatalf("generation 2: %+v", m2)
	}
	receipt, snapshot, err = r.Accept(ctx, m2, featureSpec())
	if err != nil || receipt.Replay || value(snapshot, "reading_count").Value != "3" || len(snapshot.Tail) != 0 {
		t.Fatalf("second DWS->PG handoff: %+v %+v %v", receipt, snapshot, err)
	}
	if snapshot.NextChangeAt == nil || !snapshot.NextChangeAt.Equal(future.OccurredAt) {
		t.Fatalf("future accepted fact did not schedule snapshot invalidation: %+v", snapshot.NextChangeAt)
	}
	if ready, err := r.Source.ReadyFeatureSnapshot(ctx, subject); err != nil || ready.ID != snapshot.ID {
		t.Fatalf("ready snapshot: %+v %v", ready, err)
	}
	if old, err := r.Source.FeatureSnapshotByID(ctx, subject, snapshot.ID); err != nil || old.ID != snapshot.ID {
		t.Fatalf("immutable snapshot: %+v %v", old, err)
	}
	changed := m2
	changed.ContributionCount++
	if _, _, err := r.Accept(ctx, changed, featureSpec()); err == nil {
		t.Fatal("mutated manifest accepted")
	}
	changed = m2
	changed.BaselineSHA256 = "0" + changed.BaselineSHA256[1:]
	if _, _, err := r.Accept(ctx, changed, featureSpec()); err == nil {
		t.Fatal("mutated artifact hash accepted")
	}
	if _, _, err := r.Accept(ctx, m1, featureSpec()); !errors.Is(err, usermodel.ErrConflict) {
		t.Fatalf("old generation changed head: %v", err)
	}
}
