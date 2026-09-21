package featurebaseline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"testing"

	usermodelmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
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

func value(snapshot usermodel.FeatureSnapshot, key string) usermodel.FeatureValue {
	for _, v := range snapshot.Values {
		if v.Name == key {
			return v
		}
	}
	return usermodel.FeatureValue{}
}

// servingWarehouseRunner owns an isolated PostgreSQL schema. The dbt project is
// supplied by the external data-engineering archive when this optional test is
// enabled; no data-generation project is embedded in the service repository.
func servingWarehouseRunner(t *testing.T, observed ...*telemetry.Bundle) (Runner, usermodel.SubjectRef) {
	t.Helper()
	dsn := os.Getenv("USERMODEL_TEST_POSTGRES_DSN")
	ch, s3, dbt := os.Getenv("USERMODEL_TEST_CH_URL"), os.Getenv("USERMODEL_TEST_S3_PREFIX"), os.Getenv("USERMODEL_TEST_DBT")
	if dsn == "" || ch == "" || s3 == "" || dbt == "" {
		t.Skip("set USERMODEL_TEST_* to run against the external data-engineering archive")
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
	var bundle *telemetry.Bundle
	if len(observed) > 0 {
		bundle = observed[0]
	}
	r := Runner{Source: usermodel.NewStore(pool, bundle), ClickHouse: ch, S3Prefix: s3, DBT: dbt,
		ProjectDir:  os.Getenv("USERMODEL_TEST_DBT_PROJECT"),
		ProfilesDir: os.Getenv("USERMODEL_TEST_DBT_PROFILES"), WorkDir: t.TempDir()}
	return r, usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "ws08d-1001"}
}
