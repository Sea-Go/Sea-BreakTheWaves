package featurebaseline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/usermodel"
	usermodelmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/usermodel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type servingFixedEncoder struct{}

func (servingFixedEncoder) EncodeUser(_ context.Context, snapshot usermodel.FeatureSnapshot,
	pair usermodel.PairRef) (usermodel.EncoderOutput, error) {
	count, err := strconv.ParseFloat(value(snapshot, "reading_count").Value, 64)
	if err != nil {
		return usermodel.EncoderOutput{}, err
	}
	recent := 0.0
	if value(snapshot, "recent_article").Value == "article/known" {
		recent = 1
	}
	return usermodel.EncoderOutput{PairID: pair.PairID, SpaceID: pair.SpaceID,
		EncoderID: pair.EncoderID, FeatureSnapshotID: snapshot.ID,
		Source: "fixed_baseline", Vector: []float64{count, recent}}, nil
}

type servingFixedApproval struct{ pair usermodel.PairRef }

func (a servingFixedApproval) AuthorizePair(context.Context, usermodel.PairRef) (usermodel.PairAuthorization, error) {
	return usermodel.PairAuthorization{Pair: a.pair, ApprovalRef: "fixture-recommend-fixed-baseline",
		Revision: 1, Active: true}, nil
}

// This consumer test intentionally owns its schema and migration. It uses the
// existing H10.c runtime but changes no warehouse producer or acceptance code.
func servingWarehouseRunner(t *testing.T) (Runner, usermodel.SubjectRef) {
	t.Helper()
	dsn := os.Getenv("USERMODEL_TEST_POSTGRES_DSN")
	ch, s3, dbt := os.Getenv("USERMODEL_TEST_CH_URL"), os.Getenv("USERMODEL_TEST_S3_PREFIX"), os.Getenv("USERMODEL_TEST_DBT")
	if dsn == "" || ch == "" || s3 == "" || dbt == "" {
		t.Skip("run warehouse/feature_baselines/acceptance.py with isolated PG/CH/S3/dbt")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := "serving_handoff_" + hex.EncodeToString(nonce[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
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
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	if _, err := pool.Exec(ctx, usermodelmigration.SQL); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"../../../migrations/usermodel/002_ontology.sql",
		"../../../migrations/usermodel/003_features.sql",
		"../../../migrations/usermodel/004_serving.sql",
	} {
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
	return Runner{Source: usermodel.NewStore(pool, nil), ClickHouse: ch, S3Prefix: s3, DBT: dbt,
			ProjectDir:  filepath.Join(root, "warehouse/feature_baselines"),
			ProfilesDir: filepath.Join(root, "warehouse/environment"), WorkDir: t.TempDir()},
		usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "ws08d-1001"}
}

func TestRealWarehouseTwoGenerationsIntoServingBundle(t *testing.T) {
	r, subject := servingWarehouseRunner(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-25 * time.Minute)
	for _, e := range []usermodel.Event{
		featureEvent(subject, "serving-read-1", 1, at),
		featureEvent(subject, "serving-read-3", 3, at.Add(time.Minute)),
	} {
		if receipt, err := r.Source.Append(ctx, e); err != nil || receipt.Status != "accepted" {
			t.Fatalf("accepted PG fact: %+v %v", receipt, err)
		}
	}
	spec := featureSpec()
	m1, err := r.Build(ctx, subject, 1, "h10c_serving_g1", spec)
	if err != nil || m1.ContributionCount != 1 {
		t.Fatalf("real DWS generation 1: %+v %v", m1, err)
	}
	_, snap1, err := r.Accept(ctx, m1, spec)
	if err != nil || snap1.Generation != m1.Generation || snap1.Revision != 1 ||
		len(snap1.Tail) != 1 || value(snap1, "reading_count").Value != "2" {
		t.Fatalf("real S3/PG feature snapshot 1: %+v %v", snap1, err)
	}
	pair := usermodel.PairRef{PairID: "h10c-fixed-baseline-pair-v1",
		SpaceID: "h10c-fixed-baseline-space-v1", EncoderID: "fixed-count-recency-v1",
		Kind: "fixed_baseline", FeatureSpecVersion: spec.Version, FeatureSpecHash: m1.SpecHash,
		Dimension: 2, Metric: "dot"}
	b1, err := r.Source.BuildServingBundle(ctx, subject, pair, servingFixedEncoder{})
	if err != nil || b1.FeatureSnapshotID != snap1.ID || b1.Generation != m1.Generation ||
		b1.Vector[0] != 2 || len(b1.Tail) != 1 {
		t.Fatalf("real generation 1 to bundle: %+v %v", b1, err)
	}
	p1, err := r.Source.ActivateServingBundle(ctx, subject, pair, b1.ID, 0, servingFixedApproval{pair})
	if err != nil || p1.Version != 1 {
		t.Fatalf("fixed baseline pointer 1: %+v %v", p1, err)
	}
	if _, err := r.Source.Append(ctx, featureEvent(subject, "serving-read-2", 2, at.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Source.ReadyServingBundle(ctx, subject, pair); !errors.Is(err, usermodel.ErrPending) {
		t.Fatalf("stale first bundle served after source gap closure: %v", err)
	}
	m2, err := r.Build(ctx, subject, 2, "h10c_serving_g2", spec)
	if err != nil || m2.ContributionCount != 3 || m2.Watermarks[0].ContiguousSequence != 3 {
		t.Fatalf("real DWS generation 2: %+v %v", m2, err)
	}
	_, snap2, err := r.Accept(ctx, m2, spec)
	if err != nil || snap2.Revision != 2 || snap2.ID == snap1.ID || len(snap2.Tail) != 0 ||
		value(snap2, "reading_count").Value != "3" {
		t.Fatalf("real S3/PG feature snapshot 2: %+v %v", snap2, err)
	}
	b2, err := r.Source.BuildServingBundle(ctx, subject, pair, servingFixedEncoder{})
	if err != nil || b2.FeatureSnapshotID != snap2.ID || b2.Generation != m2.Generation ||
		b2.Vector[0] != 3 || len(b2.Tail) != 0 {
		t.Fatalf("real generation 2 to bundle: %+v %v", b2, err)
	}
	if _, err := r.Source.ActivateServingBundle(ctx, subject, pair, b2.ID, 0, servingFixedApproval{pair}); !errors.Is(err, usermodel.ErrConflict) {
		t.Fatalf("stale CAS replaced current pair: %v", err)
	}
	p2, err := r.Source.ActivateServingBundle(ctx, subject, pair, b2.ID, p1.Version, servingFixedApproval{pair})
	if err != nil || p2.Version != 2 {
		t.Fatalf("fixed baseline pointer 2: %+v %v", p2, err)
	}
	if current, _, err := r.Source.ReadyServingBundle(ctx, subject, pair); err != nil || current.ID != b2.ID {
		t.Fatalf("current real DWS representation: %+v %v", current, err)
	}
	if old, err := r.Source.ServingBundleByID(ctx, subject, b1.ID); err != nil || old.ID != b1.ID {
		t.Fatalf("first generation historical ID: %+v %v", old, err)
	}
	other := subject
	other.SubjectID = "ws08d-1002"
	if _, err := r.Source.ServingBundleByID(ctx, other, b2.ID); !errors.Is(err, usermodel.ErrNotFound) {
		t.Fatalf("cross-subject bundle read: %v", err)
	}
	t.Logf("WS07-C to WS08-D source1=%s dws1=%s snapshot1=%s bundle1=%s source2=%s dws2=%s snapshot2=%s bundle2=%s",
		m1.SourceSHA256, m1.DWSValuesSHA256, snap1.ID, b1.ID,
		m2.SourceSHA256, m2.DWSValuesSHA256, snap2.ID, b2.ID)
}
