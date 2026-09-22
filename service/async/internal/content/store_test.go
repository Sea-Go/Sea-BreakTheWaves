package content

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("CONTENT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONTENT_TEST_POSTGRES_DSN unset; use scripts/test-content.sh for real PG acceptance")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "content_test_" + hex.EncodeToString(nonce[:])
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
	if err := MigratePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return NewStore(pool)
}

func ledgerInput() BuildInput {
	return BuildInput{BuildID: "build", ModuleID: "module", ReleaseID: "release", Generation: 1,
		InputHash: artifacts.Hash([]byte("input")), OperationID: "operation", Revisions: []string{"revision"}}
}

func claimFixture(t *testing.T, s *Store) Fence {
	t.Helper()
	f := Fence{BuildID: "build", AttemptID: "worker-a", LeaseEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}
	if _, err := s.Claim(context.Background(), ledgerInput(), f); err != nil {
		t.Fatal(err)
	}
	return f
}

func recordFixture(t *testing.T, s *Store, f Fence) corpus.Ref {
	t.Helper()
	ctx := context.Background()
	if err := s.recordChunks(ctx, f, artifacts.Reference([]byte("chunks"))); err != nil {
		t.Fatal(err)
	}
	for _, lane := range []string{"dense", "sparse", "multivector"} {
		if err := s.RecordLane(ctx, f, lane, artifacts.Reference([]byte(lane))); err != nil {
			t.Fatal(err)
		}
	}
	return artifacts.Reference([]byte("manifest"))
}

func TestPostgresExecutionFencingAndImmutableInput(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first := claimFixture(t, s)
	var wg sync.WaitGroup
	for epoch := int64(2); epoch <= 16; epoch++ {
		wg.Add(1)
		go func(epoch int64) {
			defer wg.Done()
			f := first
			f.LeaseEpoch = epoch
			f.AttemptID = "replacement"
			_, err := s.Claim(ctx, ledgerInput(), f)
			if err != nil && !errors.Is(err, ErrConflict) {
				t.Errorf("claim: %v", err)
			}
		}(epoch)
	}
	wg.Wait()
	b, err := s.Get(ctx, "build")
	if err != nil {
		t.Fatal(err)
	}
	if b.LeaseEpoch != 16 {
		t.Fatalf("latest epoch=%d", b.LeaseEpoch)
	}
	if err := s.recordChunks(ctx, first, artifacts.Reference([]byte("old"))); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale accepted: %v", err)
	}
	changed := ledgerInput()
	changed.Revisions = []string{"different"}
	if _, err := s.Claim(ctx, changed, b.Fence); !errors.Is(err, ErrConflict) {
		t.Fatalf("input mutation accepted: %v", err)
	}
	if err := s.recordChunks(ctx, b.Fence, artifacts.Reference([]byte("current"))); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAllLanesRequiredAndResultReplay(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	f := claimFixture(t, s)
	result := artifacts.Reference([]byte("ready"))
	if err := s.commitReady(ctx, f, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing lanes accepted: %v", err)
	}
	result = recordFixture(t, s, f)
	if err := s.RecordLane(ctx, f, "dense", artifacts.Reference([]byte("different"))); !errors.Is(err, ErrConflict) {
		t.Fatal("lane artifact replaced")
	}
	if err := s.commitReady(ctx, f, result); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE content_builds SET lease_expires_at=clock_timestamp()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if err := s.commitReady(ctx, f, result); err != nil {
		t.Fatalf("identical terminal replay: %v", err)
	}
	var count int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM content_outbox").Scan(&count); err != nil || count != 1 {
		t.Fatalf("outbox=%d err=%v", count, err)
	}
	if err := s.commitReady(ctx, f, artifacts.Reference([]byte("different"))); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal result replaced")
	}
}

func TestPostgresExpiryDuringOutboxRollsBackReady(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	f := claimFixture(t, s)
	result := recordFixture(t, s, f)
	_, err := s.db.Exec(ctx, `CREATE FUNCTION delay_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.25); RETURN NEW; END $$;
        CREATE TRIGGER delay_outbox BEFORE INSERT ON content_outbox FOR EACH ROW EXECUTE FUNCTION delay_outbox();`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE content_builds SET lease_expires_at=clock_timestamp()+interval '150 milliseconds'"); err != nil {
		t.Fatal(err)
	}
	if err := s.commitReady(ctx, f, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("late commit=%v", err)
	}
	b, err := s.Get(ctx, "build")
	if err != nil {
		t.Fatal(err)
	}
	if b.State != "BUILDING" || b.Result != nil {
		t.Fatalf("late ready persisted: %+v", b)
	}
	var count int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM content_outbox").Scan(&count); err != nil || count != 0 {
		t.Fatalf("outbox rollback=%d %v", count, err)
	}
}

func TestPostgresCancelAndTombstone(t *testing.T) {
	ctx := context.Background()
	t.Run("cancel", func(t *testing.T) {
		s := testStore(t)
		f := claimFixture(t, s)
		result := recordFixture(t, s, f)
		if err := s.Cancel(ctx, "build", 1); err != nil {
			t.Fatal(err)
		}
		if err := s.commitReady(ctx, f, result); !errors.Is(err, ErrConflict) {
			t.Fatal("cancelled result accepted")
		}
		if err := s.Cancel(ctx, "build", 1); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tombstone", func(t *testing.T) {
		s := testStore(t)
		f := claimFixture(t, s)
		result := recordFixture(t, s, f)
		hash := artifacts.Hash([]byte("withdrawal"))
		if err := s.Tombstone(ctx, "module", "revision", 5, hash); err != nil {
			t.Fatal(err)
		}
		if err := s.Tombstone(ctx, "module", "revision", 2, artifacts.Hash([]byte("older"))); err != nil {
			t.Fatal(err)
		}
		if err := s.Tombstone(ctx, "module", "revision", 5, artifacts.Hash([]byte("changed"))); !errors.Is(err, ErrConflict) {
			t.Fatal("same version accepted different payload")
		}
		if err := s.commitReady(ctx, f, result); !errors.Is(err, ErrInvalidated) {
			t.Fatalf("withdrawn accepted: %v", err)
		}
		input := ledgerInput()
		input.BuildID = "new-generation"
		input.Generation = 2
		f.BuildID = input.BuildID
		if _, err := s.Claim(ctx, input, f); !errors.Is(err, ErrInvalidated) {
			t.Fatal("new generation revived withdrawn revision")
		}
	})
}

func TestPostgresClaimExpiryAfterModuleLockWait(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockModule(ctx, tx, "module"); err != nil {
		t.Fatal(err)
	}
	f := Fence{BuildID: "build", AttemptID: "worker", LeaseEpoch: 1, ExpiresAt: time.Now().Add(100 * time.Millisecond)}
	result := make(chan error, 1)
	go func() { _, err := s.Claim(ctx, ledgerInput(), f); result <- err }()
	time.Sleep(150 * time.Millisecond)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrConflict) {
		t.Fatalf("claim after expiry: %v", err)
	}
}
