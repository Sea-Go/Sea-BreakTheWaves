package content

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
)

func TestPostgresIndexDispatchLeaseRestartAndFencing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := MigratePool(ctx, s.db); err != nil {
		t.Fatalf("versioned migration replay: %v", err)
	}
	fence := claimFixture(t, s)
	technical := TechnicalFence{AttemptID: fence.AttemptID, LeaseEpoch: fence.LeaseEpoch,
		CancelVersion: fence.CancelVersion, ExpiresAt: fence.ExpiresAt}
	if err := s.AttachIndexDispatch(ctx, "build", "dc-job", "worker", technical, fence); err != nil {
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
	newDC := TechnicalFence{AttemptID: newFence.AttemptID, LeaseEpoch: newFence.LeaseEpoch,
		CancelVersion: newFence.CancelVersion, ExpiresAt: newFence.ExpiresAt}
	if err := s.AttachIndexDispatch(ctx, "build", "dc-job", "worker", newDC, newFence); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachIndexDispatch(ctx, "build", "fresh-job", "worker", technical, fence); !errors.Is(err, ErrConflict) {
		t.Fatalf("new DC job reset its local epoch and replaced global RTW fence: %v", err)
	}
	third, found, err := s.ClaimIndexDispatch(ctx, "")
	if err != nil || !found || third.JobID != "dc-job" || third.Result != result || third.RTWAccepted {
		t.Fatalf("new RTW fence inherited an old acceptance receipt: %+v %t %v", third, found, err)
	}
	if err := s.DeferIndexDispatch(ctx, third, "needs_new_attempt", "dc_attempt_unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.ClaimIndexDispatch(ctx, ""); err != nil || found {
		t.Fatalf("old attempt still spinning: found=%t err=%v", found, err)
	}
	// A replacement DC job starts its own epoch at one, while RTW's build-wide
	// fence must advance to three. Neither local READY nor its outbox is rewritten.
	crossDC := TechnicalFence{AttemptID: "cross-job-attempt", LeaseEpoch: 1,
		CancelVersion: 0, ExpiresAt: time.Now().Add(time.Minute)}
	crossRTW := newFence
	crossRTW.AttemptID, crossRTW.LeaseEpoch, crossRTW.ExpiresAt =
		crossDC.AttemptID, 3, crossDC.ExpiresAt
	if err := s.AttachIndexDispatch(ctx, "build", "fresh-job", "worker", crossDC, crossRTW); err != nil {
		t.Fatalf("cross-job DC epoch one rejected RTW global fence three: %v", err)
	}
	fourth, found, err := s.ClaimIndexDispatch(ctx, "build")
	if err != nil || !found || fourth.JobID != "fresh-job" || fourth.DC.LeaseEpoch != 1 ||
		fourth.Fence.LeaseEpoch != 3 || fourth.RTWAccepted || fourth.Result != result {
		t.Fatalf("cross-job dispatch lost separate authority epochs: %+v %t %v", fourth, found, err)
	}
	if err := s.NoteIndexRTWAccepted(ctx, third); !errors.Is(err, ErrConflict) {
		t.Fatalf("old RTW scanner confirmed new cross-job fence: %v", err)
	}
	if err := s.NoteIndexRTWAccepted(ctx, fourth); err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverIndexDispatch(ctx, fourth); err != nil {
		t.Fatal(err)
	}
	// Once RTW accepted immutable READY, another DC job epoch one may ACK the
	// same Ref without minting a fourth RTW fence.
	ackDC := TechnicalFence{AttemptID: "ack-job-attempt", LeaseEpoch: 1,
		CancelVersion: 0, ExpiresAt: time.Now().Add(time.Minute)}
	if err := s.AttachIndexDispatch(ctx, "build", "ack-job", "worker", ackDC, crossRTW); err != nil {
		t.Fatalf("new DC job could not reuse accepted RTW READY: %v", err)
	}
	fifth, found, err := s.ClaimIndexDispatch(ctx, "build")
	if err != nil || !found || fifth.JobID != "ack-job" || fifth.DC.LeaseEpoch != 1 ||
		fifth.Fence.LeaseEpoch != 3 || !fifth.RTWAccepted || fifth.Result != result {
		t.Fatalf("accepted READY handoff changed RTW fence or lost DC lease: %+v %t %v", fifth, found, err)
	}
	if err := s.DeliverIndexDispatch(ctx, fifth); err != nil {
		t.Fatal(err)
	}
	build, err := s.Get(ctx, "build")
	if err != nil || build.State != "READY" || build.Result == nil || *build.Result != artifacts.Reference([]byte("manifest")) ||
		build.AttemptID != fence.AttemptID || build.LeaseEpoch != fence.LeaseEpoch ||
		!build.ExpiresAt.Equal(fence.ExpiresAt) {
		t.Fatalf("dispatch changed local READY: %+v %v", build, err)
	}
}
