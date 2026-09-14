package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
)

func sameAcceptedIndex(remote ridethewind.Build, fixed content.Build, result corpus.Ref) bool {
	return remote.State == "READY" && remote.BuildId == fixed.BuildInput.BuildID &&
		remote.ModuleId == fixed.ModuleID && remote.ReleaseId == fixed.ReleaseID &&
		remote.Generation == fixed.Generation && remote.ManifestHash == fixed.InputHash &&
		remote.CancelVersion == fixed.CancelVersion && remote.IndexManifestRef == result.Key &&
		remote.IndexManifestHash == result.SHA256 && remote.ErrorCode == ""
}

func sameIndexJobAttempt(job jobs.Job, claim content.IndexDispatch) bool {
	return job.ID == claim.JobID && job.WorkerID == claim.WorkerID &&
		job.AttemptID == claim.DC.AttemptID && job.LeaseEpoch == claim.DC.LeaseEpoch &&
		job.CancelVersion == claim.DC.CancelVersion && job.Request.JobType == IndexJobType
}

func sameIndexJobSuccess(job jobs.Job, claim content.IndexDispatch) bool {
	return sameIndexJobAttempt(job, claim) && job.State == "succeeded" && job.Result != nil &&
		job.Result.State == "succeeded" && job.Result.Ref != nil &&
		*job.Result.Ref == (jobs.ResultRef{URI: "sha256:" + claim.Result.SHA256,
			Hash: claim.Result.SHA256, MediaType: indexResultMediaType})
}

// DispatchReadyOnce is restart-safe: the PG outbox selects the fixed READY
// result, RTW verifies its immutable artifact, DC accepts only its own job,
// and delivered_at is written after both receipts are confirmed. An expired
// remote lease is held for a new DC attempt instead of replayed as success.
func (w *IndexWorker) DispatchReadyOnce(parent context.Context, buildID string) (worked bool, err error) {
	claim, found, err := w.store.ClaimIndexDispatch(parent, buildID)
	if err != nil || !found {
		return false, err
	}
	worked = true
	ctx, stage, beginErr := w.observed.Begin(parent, "content", "content.worker.index_dispatch",
		slog.String("build_id", claim.BuildID), slog.String("job_id", claim.JobID),
		slog.String("attempt_id", claim.DC.AttemptID), slog.Int64("dc_lease_epoch", claim.DC.LeaseEpoch),
		slog.Int64("rtw_build_lease_epoch", claim.Fence.LeaseEpoch))
	if beginErr != nil {
		if deferErr := w.store.DeferIndexDispatch(context.WithoutCancel(parent), claim, "pending", "observation_unavailable"); deferErr != nil {
			beginErr = errors.Join(beginErr, deferErr)
		}
		return true, beginErr
	}
	defer func() {
		outcome, code := indexWorkerOutcome(err)
		stage.End(ctx, outcome, code, err, slog.String("index_manifest_hash", claim.Result.SHA256))
	}()
	// All HTTP and PG effects share a budget strictly inside the database
	// claim. An expired caller cannot continue accepting a remote result while
	// another scanner owns the next claim_epoch.
	var cancel context.CancelFunc
	ctx, cancel = context.WithDeadline(ctx, claim.ClaimUntil.Add(-time.Second))
	defer cancel()
	deferred := false
	defer func() {
		if err != nil && !deferred {
			if deferErr := w.store.DeferIndexDispatch(context.WithoutCancel(ctx), claim, "pending", "dispatch_retry"); deferErr != nil {
				err = errors.Join(err, fmt.Errorf("persist dispatch retry: %w", deferErr))
			}
		}
	}()
	fixed, readErr := w.store.Get(ctx, claim.BuildID)
	if readErr != nil {
		return true, readErr
	}
	if fixed.State != "READY" || fixed.Result == nil || *fixed.Result != claim.Result || len(fixed.Lanes) != 3 {
		return true, fmt.Errorf("%w: dispatch is not committed READY", content.ErrConflict)
	}
	remote, readErr := w.builds.GetBuild(ctx, claim.BuildID)
	if readErr != nil {
		return true, fmt.Errorf("read RTW dispatch build: %w", readErr)
	}
	if remote.State == "BUILDING" {
		if !sameRTWIndexBuild(remote, fixed, claim.Fence) || !claim.Fence.ExpiresAt.After(time.Now().Add(250*time.Millisecond)) {
			deferred = true
			if deferErr := w.store.DeferIndexDispatch(ctx, claim, "needs_new_attempt", "rtw_claim_stale"); deferErr != nil {
				return true, deferErr
			}
			return true, ErrExpiredIndexLease
		}
		request := ridethewind.AcceptBuildReq{BuildId: claim.BuildID, Generation: fixed.Generation,
			ManifestHash: fixed.InputHash, AttemptId: claim.Fence.AttemptID,
			LeaseEpoch: claim.Fence.LeaseEpoch, CancelVersion: claim.Fence.CancelVersion,
			State: "READY", IndexManifestRef: claim.Result.Key, IndexManifestHash: claim.Result.SHA256}
		accepted, acceptErr := w.builds.AcceptBuild(ctx, request)
		if acceptErr != nil {
			// The HTTP reply may be lost after RTW committed. Read the authority.
			accepted, readErr = w.builds.GetBuild(ctx, claim.BuildID)
			if readErr != nil {
				return true, fmt.Errorf("RTW AcceptBuild reply uncertain: %w; read: %v", acceptErr, readErr)
			}
		}
		remote = accepted
	}
	if !sameAcceptedIndex(remote, fixed, claim.Result) {
		if remote.State != "BUILDING" {
			deferred = true
			if deferErr := w.store.DeferIndexDispatch(ctx, claim, "manual", "rtw_terminal_mismatch"); deferErr != nil {
				return true, deferErr
			}
		}
		return true, fmt.Errorf("%w: RTW did not accept fixed READY result", content.ErrConflict)
	}
	if !claim.RTWAccepted {
		if noteErr := w.store.NoteIndexRTWAccepted(ctx, claim); noteErr != nil {
			return true, fmt.Errorf("persist RTW acceptance: %w", noteErr)
		}
	}
	job, readErr := w.jobs.GetJob(ctx, claim.JobID)
	if readErr != nil {
		return true, fmt.Errorf("read DC technical job: %w", readErr)
	}
	if !sameIndexJobSuccess(job, claim) {
		if !sameIndexJobAttempt(job, claim) || job.State != "running" ||
			!claim.DC.ExpiresAt.After(time.Now().Add(250*time.Millisecond)) {
			state := "needs_new_attempt"
			if job.State == "failed" || job.State == "cancelled" || job.State == "succeeded" ||
				job.Attempt >= job.Request.MaxAttempts && job.State != "queued" {
				state = "manual"
			}
			deferred = true
			if deferErr := w.store.DeferIndexDispatch(ctx, claim, state, "dc_attempt_unavailable"); deferErr != nil {
				return true, deferErr
			}
			return true, fmt.Errorf("%w: DC technical attempt unavailable", content.ErrConflict)
		}
		result := jobs.Result{State: "succeeded", Ref: &jobs.ResultRef{
			URI: "sha256:" + claim.Result.SHA256, Hash: claim.Result.SHA256, MediaType: indexResultMediaType}}
		_, completeErr := w.jobs.CompleteJob(ctx, claim.JobID, jobs.Complete{
			Lease: jobs.Lease{WorkerID: claim.WorkerID, AttemptID: claim.DC.AttemptID,
				LeaseEpoch: claim.DC.LeaseEpoch, CancelVersion: claim.DC.CancelVersion}, Result: result})
		if completeErr != nil {
			job, readErr = w.jobs.GetJob(ctx, claim.JobID)
			if readErr != nil || !sameIndexJobSuccess(job, claim) {
				return true, fmt.Errorf("DC technical ACK uncertain: %w", completeErr)
			}
		}
	}
	if deliverErr := w.store.DeliverIndexDispatch(ctx, claim); deliverErr != nil {
		return true, fmt.Errorf("persist completed index dispatch: %w", deliverErr)
	}
	return true, nil
}
