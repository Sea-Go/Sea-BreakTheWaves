package recommend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	recommendmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/recommend"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type itemSource struct {
	mu      sync.Mutex
	current Publication
	revs    map[string]Revision
	failed  error
	calls   int
}

func (s *itemSource) Current(_ context.Context, _ string) (Publication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failed != nil {
		return Publication{}, s.failed
	}
	return clonePublication(s.current), nil
}

func (s *itemSource) Revision(_ context.Context, id string) (Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revs[id], nil
}

func itemFixture() *itemSource {
	ref := artifacts.Reference([]byte("item-index"))
	publication := Publication{ModuleID: "module-1", ReleaseID: "release-1", Generation: 1,
		PublicationRevision: "1", ValidRevisionIDs: []string{"rev-1", "rev-2"},
		Indexes: map[string]corpus.Ref{"dense": ref, "sparse": ref, "multivector": ref}}
	revision := func(id, item, title string, created time.Time) Revision {
		hash := artifacts.Hash([]byte(id))
		return Revision{RevisionID: id, ModuleID: publication.ModuleID, ItemID: item,
			Kind: "source", Title: title, MediaType: "text/markdown", ContentHash: hash,
			ObjectKey: "sha256/" + hash, CreatedAt: created}
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return &itemSource{current: publication, revs: map[string]Revision{
		"rev-1": revision("rev-1", "article-1", "Old article", start),
		"rev-2": revision("rev-2", "article-2", "New article", start.Add(time.Hour)),
	}}
}

func TestBuildPoolUsesOneCurrentRTWGeneration(t *testing.T) {
	source := itemFixture()
	release, err := BuildPool(context.Background(), source, "module-1")
	if err != nil || !validRelease(release) || len(release.Items) != 2 ||
		release.Items[0].ItemID != "article-1" || release.Items[1].RevisionID != "rev-2" ||
		source.calls != 2 {
		t.Fatalf("RTW content release: %+v calls=%d err=%v", release, source.calls, err)
	}
	bad := itemFixture()
	bad.revs["rev-2"] = Revision{RevisionID: "rev-2", ModuleID: "module-1", Withdrawn: true}
	if _, err := BuildPool(context.Background(), bad, "module-1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("withdrawn revision entered pool: %v", err)
	}
	bad = itemFixture()
	bad.revs["rev-2"] = bad.revs["rev-1"]
	if _, err := BuildPool(context.Background(), bad, "module-1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong revision identity entered pool: %v", err)
	}
}

type flipSource struct {
	*itemSource
	flipped bool
}

func (s *flipSource) Current(ctx context.Context, module string) (Publication, error) {
	if s.flipped {
		s.current.PublicationRevision = "2"
	}
	s.flipped = true
	return s.itemSource.Current(ctx, module)
}

func TestBuildPoolRejectsPublicationChangeDuringRevisionReads(t *testing.T) {
	_, err := BuildPool(context.Background(), &flipSource{itemSource: itemFixture()}, "module-1")
	if !errors.Is(err, ErrStale) {
		t.Fatalf("changed RTW pointer accepted: %v", err)
	}
}

type lateFlipSource struct {
	*itemSource
	reads int
}

func (s *lateFlipSource) Current(ctx context.Context, module string) (Publication, error) {
	s.reads++
	if s.reads == 3 {
		s.itemSource.mu.Lock()
		s.itemSource.current.ReleaseID = "release-2"
		s.itemSource.current.Generation = 2
		s.itemSource.current.PublicationRevision = "2"
		s.itemSource.mu.Unlock()
	}
	return s.itemSource.Current(ctx, module)
}

func TestRealPostgresRejectsRTWMoveBeforePoolCommit(t *testing.T) {
	db := isolatedPool(t)
	store, err := NewStore(db, &lateFlipSource{itemSource: itemFixture()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rebuild(context.Background(), "module-1"); !errors.Is(err, ErrStale) {
		t.Fatalf("RTW moved before PG commit but pool accepted: %v", err)
	}
	var count int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM recommend_pool_releases`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale pool artifact survived rollback: %d %v", count, err)
	}
}

func isolatedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("RECOMMEND_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires internal/recommend/test-postgres.sh isolated PostgreSQL")
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
	schema := "recommend_" + hex.EncodeToString(nonce[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	if _, err := pool.Exec(ctx, recommendmigration.SQL); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestRealPostgresImmutablePoolRebuildAndWithdrawal(t *testing.T) {
	db := isolatedPool(t)
	source := itemFixture()
	store, err := NewStore(db, source)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Rebuild(context.Background(), "module-1")
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	concurrent := make(chan error, 4)
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			replayed, runErr := store.Rebuild(context.Background(), "module-1")
			if runErr == nil && replayed.ID != first.ID {
				runErr = ErrConflict
			}
			concurrent <- runErr
		}()
	}
	group.Wait()
	close(concurrent)
	for err := range concurrent {
		if err != nil {
			t.Fatalf("concurrent same-generation rebuild: %v", err)
		}
	}
	page, err := store.Candidates(context.Background(), "module-1", 1)
	if err != nil || page.PoolReleaseID != first.ID || page.BehaviorState != "not_connected" ||
		page.Status != "ready" || len(page.Candidates) != 1 || page.Candidates[0].Item.ItemID != "article-2" ||
		page.Candidates[0].Sources[0] != "new_content" {
		t.Fatalf("bounded cold pool: %+v %v", page, err)
	}
	if _, err := store.Candidates(context.Background(), "module-1", MaxCandidates+1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded candidate read accepted: %v", err)
	}
	replayed, err := store.Rebuild(context.Background(), "module-1")
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("same generation was not idempotent: %+v %v", replayed, err)
	}
	var versions, releaseCount int
	if err := db.QueryRow(context.Background(), `SELECT pointer_version FROM recommend_pool_heads WHERE module_id='module-1'`).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("idempotent pointer advanced: %d %v", versions, err)
	}
	if _, err := db.Exec(context.Background(), `UPDATE recommend_pool_releases SET feature_version='forged' WHERE pool_release_id=$1`, first.ID); err == nil {
		t.Fatal("immutable release updated")
	}
	source.mu.Lock()
	source.current.ReleaseID, source.current.Generation, source.current.PublicationRevision = "release-2", 2, "2"
	source.current.ValidRevisionIDs = []string{"rev-2"}
	source.mu.Unlock()
	if _, err := store.Candidates(context.Background(), "module-1", 2); !errors.Is(err, ErrStale) {
		t.Fatalf("changed RTW publication served old pool: %v", err)
	}
	second, err := store.Rebuild(context.Background(), "module-1")
	if err != nil || second.ID == first.ID || len(second.Items) != 1 {
		t.Fatalf("new RTW generation did not rebuild pool: %+v %v", second, err)
	}
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM recommend_pool_releases`).Scan(&releaseCount); err != nil || releaseCount != 2 {
		t.Fatalf("old immutable generation lost: %d %v", releaseCount, err)
	}
	if historical, err := store.Release(context.Background(), first.ID); err != nil || !reflect.DeepEqual(historical, first) {
		t.Fatalf("historical release changed: %+v %v", historical, err)
	}
	source.mu.Lock()
	source.failed = ErrUnavailable // RTW withdrawal or unavailable current publication
	source.mu.Unlock()
	if _, err := store.Candidates(context.Background(), "module-1", 2); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("withdrawn RTW content was still served: %v", err)
	}
}
