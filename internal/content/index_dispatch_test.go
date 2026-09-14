package content

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	contentmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/content"
)

func TestPostgresIndexDispatchLeaseRestartAndFencing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.db.Exec(ctx, contentmigration.SQL); err != nil {
		t.Fatalf("versioned migration replay: %v", err)
	}
	fence := claimFixture(t, s)
	if err := s.AttachIndexDispatch(ctx, "build", "dc-job", "worker", fence); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.ClaimIndexDispatch(ctx, ""); err != nil || found {
		t.Fatalf("BUILDING produced dispatch: found=%t err=%v", found, err)
	}
	result := recordFixture(t, s, fence)
	if err := s.commitReady(ctx, fence, result); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan IndexDispatch, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, found, err := s.ClaimIndexDispatch(ctx, "")
			if err != nil {
				t.Errorf("concurrent claim: %v", err)
			}
			if found {
				claims <- claim
			}
		}()
	}
	wg.Wait()
	close(claims)
	var first IndexDispatch
	for claim := range claims {
		if first.BuildID != "" {
			t.Fatal("duplicate concurrent claim")
		}
		first = claim
	}
	if first.Result != result || first.JobID != "dc-job" || first.ClaimEpoch != 1 {
		t.Fatalf("wrong persisted dispatch: %+v", first)
	}
	if _, err := s.db.Exec(ctx, "UPDATE content_index_dispatch SET claim_until=clock_timestamp()-interval '1 second' WHERE build_id='build'"); err != nil {
		t.Fatal(err)
	}
	// A restarted consumer claims the durable outbox; the former claim is fenced.
	second, found, err := s.ClaimIndexDispatch(ctx, "build")
	if err != nil || !found || second.ClaimEpoch != first.ClaimEpoch+1 {
		t.Fatalf("restart claim=%+v found=%t err=%v", second, found, err)
	}
	if err := s.NoteIndexRTWAccepted(ctx, first); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale scanner confirmed RTW: %v", err)
	}
	if err := s.NoteIndexRTWAccepted(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverIndexDispatch(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.ClaimIndexDispatch(ctx, ""); err != nil || found {
		t.Fatalf("completed event rescanned: found=%t err=%v", found, err)
	}
	var delivered bool
	if err := s.db.QueryRow(ctx, "SELECT delivered_at IS NOT NULL FROM content_outbox WHERE build_id='build'").Scan(&delivered); err != nil || !delivered {
		t.Fatalf("outbox completion: delivered=%t err=%v", delivered, err)
	}
	newFence := fence
	newFence.AttemptID, newFence.LeaseEpoch, newFence.ExpiresAt = "new-attempt", 2, time.Now().Add(time.Minute)
	if err := s.AttachIndexDispatch(ctx, "build", "dc-job", "worker", newFence); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachIndexDispatch(ctx, "build", "fresh-job", "worker", fence); !errors.Is(err, ErrConflict) {
		t.Fatalf("new DC job reset its local epoch and replaced global RTW fence: %v", err)
	}
	third, found, err := s.ClaimIndexDispatch(ctx, "")
	if err != nil || !found || third.JobID != "dc-job" || third.Result != result || !third.RTWAccepted {
		t.Fatalf("new DC attempt lost immutable accepted READY: %+v %t %v", third, found, err)
	}
	if err := s.DeferIndexDispatch(ctx, third, "needs_new_attempt", "dc_attempt_unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.ClaimIndexDispatch(ctx, ""); err != nil || found {
		t.Fatalf("old attempt still spinning: found=%t err=%v", found, err)
	}
	build, err := s.Get(ctx, "build")
	if err != nil || build.State != "READY" || build.Result == nil || *build.Result != artifacts.Reference([]byte("manifest")) ||
		build.AttemptID != fence.AttemptID || build.LeaseEpoch != fence.LeaseEpoch ||
		!build.ExpiresAt.Equal(fence.ExpiresAt) {
		t.Fatalf("dispatch changed local READY: %+v %v", build, err)
	}
}
