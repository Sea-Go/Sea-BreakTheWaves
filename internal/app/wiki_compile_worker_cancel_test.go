package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
)

func TestWikiCompileWorkerCancellationAckExactAttemptAndRecovery(t *testing.T) {
	claimed, _, _ := wikiWorkerFixtureJob(t)
	for _, tc := range []struct {
		name      string
		lostReply bool
		conflict  bool
		terminal  bool
		want      error
	}{
		{name: "current-claimant-ack", want: ErrWikiCompileCancellationAcknowledged},
		{name: "lost-ack-reply-get-terminal", lostReply: true, want: ErrWikiCompileCancellationAcknowledged},
		{name: "concurrent-terminal-after-ack-conflict", conflict: true, terminal: true, want: ErrWikiCompileCancellationAcknowledged},
		{name: "ack-conflict-still-requested", conflict: true, want: ErrWikiCompileCancelAckPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := claimed
			current.State, current.CancelVersion = "cancel_requested", claimed.CancelVersion+1
			client := &wikiWorkerJobsFixture{job: current, lostAck: tc.lostReply,
				rejectAck: tc.conflict}
			if tc.terminal {
				client.terminalOnGet = 2 // DC Get converts an expired request or another same-attempt ACK.
			}
			worker := &WikiCompileWorker{jobs: client, config: WikiCompileWorkerConfig{WorkerID: claimed.WorkerID}}
			if err := worker.currentJob(context.Background(), claimed); !errors.Is(err, tc.want) ||
				client.acks != 1 || client.completes != 0 {
				t.Fatalf("DC cancel confirmation was not fenced: err=%v jobs=%+v", err, client)
			}
			if tc.want == ErrWikiCompileCancellationAcknowledged &&
				(client.job.State != "cancelled" || client.job.CancelVersion != claimed.CancelVersion+1) {
				t.Fatalf("terminal was not the same cancellation version: %+v", client.job)
			}
			if tc.want == ErrWikiCompileCancelAckPending && client.job.State != "cancel_requested" {
				t.Fatalf("conflicted ACK was mistaken for terminal: %+v", client.job)
			}
		})
	}
}

func TestWikiCompileWorkerCancellationAckRejectsOtherLeaseOrState(t *testing.T) {
	claimed, _, _ := wikiWorkerFixtureJob(t)
	t.Run("worker-config-other", func(t *testing.T) {
		current := claimed
		current.State, current.CancelVersion = "cancel_requested", claimed.CancelVersion+1
		client := &wikiWorkerJobsFixture{job: current}
		worker := &WikiCompileWorker{jobs: client, config: WikiCompileWorkerConfig{WorkerID: "other-worker"}}
		if err := worker.currentJob(context.Background(), claimed); !errors.Is(err, ErrWikiCompileFence) || client.acks != 0 {
			t.Fatalf("another configured worker acknowledged cancellation: err=%v jobs=%+v", err, client)
		}
	})
	for name, mutate := range map[string]func(*jobs.Job){
		"other-job":         func(j *jobs.Job) { j.ID = "other-job" },
		"other-worker":      func(j *jobs.Job) { j.WorkerID = "other-worker" },
		"other-attempt-id":  func(j *jobs.Job) { j.AttemptID = "other-attempt" },
		"other-attempt-no":  func(j *jobs.Job) { j.Attempt++ },
		"other-epoch":       func(j *jobs.Job) { j.LeaseEpoch++ },
		"other-input-hash":  func(j *jobs.Job) { j.InputHash = strings.Repeat("e", 64) },
		"other-submit":      func(j *jobs.Job) { j.Request.RunRef = "wiki-compile/other" },
		"skipped-version":   func(j *jobs.Job) { j.CancelVersion++ },
		"old-version":       func(j *jobs.Job) { j.CancelVersion = claimed.CancelVersion },
		"already-cancelled": func(j *jobs.Job) { j.State = "cancelled" },
		"running-new-cv":    func(j *jobs.Job) { j.State = "running" },
		"expired-lease": func(j *jobs.Job) {
			j.LeaseExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		},
	} {
		t.Run(name, func(t *testing.T) {
			current := claimed
			current.State, current.CancelVersion = "cancel_requested", claimed.CancelVersion+1
			mutate(&current)
			client := &wikiWorkerJobsFixture{job: current}
			worker := &WikiCompileWorker{jobs: client, config: WikiCompileWorkerConfig{WorkerID: claimed.WorkerID}}
			if err := worker.currentJob(context.Background(), claimed); !errors.Is(err, ErrWikiCompileFence) ||
				client.acks != 0 || client.completes != 0 {
				t.Fatalf("another job or expired lease acknowledged cancellation: err=%v jobs=%+v", err, client)
			}
		})
	}
}

// Opt-in against a task-owned local DataCenter/PG fixture. This drives the
// official client and actual Jobs service, rather than treating our in-memory
// state transitions as an online DC lease.
func TestWikiCompileWorkerRealDCJobsCancellation(t *testing.T) {
	endpoint := os.Getenv("SEA_TEST_DC_URL")
	if endpoint == "" {
		t.Skip("requires isolated loopback DataCenter Jobs fixture")
	}
	client, err := datacenter.New(httpclient.Config{BaseURL: endpoint, Token: "runtime-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture, _, _ := wikiWorkerFixtureJob(t)
	request := fixture.Request
	request.OperationID = "command:" + artifacts.Hash([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	created, err := client.SubmitJob(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := client.ClaimJob(ctx, jobs.Claim{WorkerID: fixture.WorkerID,
		JobType: WikiCompileJobType, ResourceProfile: wikiCompileResource, LeaseSeconds: 30})
	if err != nil || claimed.ID != created.ID || claimed.CancelVersion != 0 {
		t.Fatalf("real DC did not claim frozen Wiki ticket: %+v %v", claimed, err)
	}
	cancelled, err := client.CancelJob(ctx, claimed.ID, jobs.Cancel{
		OperationID:           "cancel:" + artifacts.Hash([]byte(claimed.ID)),
		ExpectedCancelVersion: claimed.CancelVersion, Reason: "superseded"})
	if err != nil || cancelled.TechnicalState != "cancel_requested" ||
		cancelled.CancelVersion != claimed.CancelVersion+1 {
		t.Fatalf("real DC did not create running cancellation fence: %+v %v", cancelled, err)
	}
	worker := &WikiCompileWorker{jobs: client, config: WikiCompileWorkerConfig{WorkerID: claimed.WorkerID}}
	wrong := claimed
	wrong.WorkerID = "other-worker"
	if err := worker.currentJob(ctx, wrong); !errors.Is(err, ErrWikiCompileFence) {
		t.Fatalf("foreign claimant could enter cancellation ACK: %v", err)
	}
	if current, err := client.GetJob(ctx, claimed.ID); err != nil || current.State != "cancel_requested" {
		t.Fatalf("foreign claimant altered real DC cancellation: %+v %v", current, err)
	}
	if err := worker.currentJob(ctx, claimed); !errors.Is(err, ErrWikiCompileCancellationAcknowledged) {
		t.Fatalf("same claimant failed to ACK actual DC cancellation: %v", err)
	}
	terminal, err := client.GetJob(ctx, claimed.ID)
	if err != nil || terminal.State != "cancelled" || terminal.CancelVersion != cancelled.CancelVersion ||
		terminal.WorkerID != claimed.WorkerID || terminal.AttemptID != claimed.AttemptID ||
		terminal.LeaseEpoch != claimed.LeaseEpoch || terminal.Result != nil {
		t.Fatalf("real DC technical cancellation lacked exact terminal proof: %+v %v", terminal, err)
	}
	if err := worker.currentJob(ctx, claimed); !errors.Is(err, ErrWikiCompileFence) {
		t.Fatalf("terminal DC job was acknowledged as a second logical action: %v", err)
	}
}
