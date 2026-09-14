package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

const PrepareJobType = "content.prepare.v1"
const prepareResultMediaType = "application/vnd.sea.chunk-manifest+json"

type PrepareJobClient interface {
	ClaimJob(context.Context, jobs.Claim) (jobs.Job, error)
	CompleteJob(context.Context, string, jobs.Complete) (jobs.CompletionReceipt, error)
	GetJob(context.Context, string) (jobs.Job, error)
}

type PrepareBuildClient interface {
	GetBuild(context.Context, string) (ridethewind.Build, error)
	ClaimBuild(context.Context, ridethewind.ClaimBuildReq) (ridethewind.Build, error)
}

type PrepareRunner interface {
	Run(context.Context, runtime.Request, runtime.Sink) (runtime.Result, error)
}

type PrepareBuildStore interface {
	Get(context.Context, string) (content.Build, error)
}

type PrepareWorkerConfig struct {
	WorkerID        string
	ResourceProfile string
	LeaseSeconds    int
}

// PrepareWorker completes one technical chunk-preparation job. Its success
// receipt names a chunk manifest; it does not accept an RTW build as READY.
type PrepareWorker struct {
	config   PrepareWorkerConfig
	jobs     PrepareJobClient
	builds   PrepareBuildClient
	runner   PrepareRunner
	store    PrepareBuildStore
	objects  artifacts.Store
	observed *telemetry.Bundle
}

func NewPrepareWorker(cfg PrepareWorkerConfig, jobs PrepareJobClient, builds PrepareBuildClient,
	runner PrepareRunner, store PrepareBuildStore, objects artifacts.Store, observed *telemetry.Bundle) (*PrepareWorker, error) {
	if cfg.WorkerID == "" || cfg.ResourceProfile == "" || cfg.LeaseSeconds < 5 || cfg.LeaseSeconds > 3600 ||
		nilDependency(jobs) || nilDependency(builds) || nilDependency(runner) || nilDependency(store) ||
		nilDependency(objects) || observed == nil || !observed.Installed() {
		return nil, errors.New("prepare worker requires fixed identity, providers, runner, store and installed telemetry")
	}
	return &PrepareWorker{cfg, jobs, builds, runner, store, objects, observed}, nil
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// RunOnce polls only the fixed technical job type. No-work is a normal result;
// a claimed job is processed under the exact DC/RTW lease and Runner lifecycle.
func (w *PrepareWorker) RunOnce(ctx context.Context) (bool, error) {
	started := time.Now()
	job, err := w.jobs.ClaimJob(ctx, jobs.Claim{WorkerID: w.config.WorkerID, JobType: PrepareJobType,
		ResourceProfile: w.config.ResourceProfile, LeaseSeconds: w.config.LeaseSeconds})
	if errors.Is(err, datacenter.ErrNoWork) {
		return false, nil
	}
	if err != nil {
		logger, logErr := w.observed.Logger("content", "application")
		if logErr == nil {
			logger.ErrorContext(ctx, "claim content prepare job failed", "event", "content.job.claim_failed",
				"outcome", "failed", "duration_ms", float64(time.Since(started))/float64(time.Millisecond),
				"error_code", "DC_CLAIM_FAILED", "error_type", fmt.Sprintf("%T", err), "error_message", err.Error())
		}
		return false, fmt.Errorf("claim content prepare job: %w", err)
	}
	_, err = w.ProcessClaim(ctx, job)
	return true, err
}

// ProcessClaim is also the deterministic integration seam for a DC job already
// granted to this worker. A failed/uncertain result is never promoted to READY.
func (w *PrepareWorker) ProcessClaim(parent context.Context, job jobs.Job) (receipt content.PrepareGraphReceipt, resultErr error) {
	ctx, stage, err := w.observed.Begin(parent, "content", "content.worker.prepare",
		slog.String("job_id", job.ID), slog.String("operation_id", job.Request.OperationID),
		slog.String("attempt_id", job.AttemptID), slog.Int64("lease_epoch", job.LeaseEpoch))
	if err != nil {
		return receipt, err
	}
	eligible, terminalAttempted := false, false
	defer func() {
		if eligible && !terminalAttempted && resultErr != nil && parent.Err() == nil {
			if err := w.reportFailure(ctx, job, resultErr); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}
		outcome, code := prepareWorkerOutcome(resultErr)
		stage.End(ctx, outcome, code, resultErr, slog.Int("chunk_count", receipt.ChunkCount),
			slog.String("chunk_manifest_hash", receipt.ChunkManifest.SHA256))
	}()
	input, fence, err := DecodePrepareClaim(job, w.config.WorkerID, PrepareJobType, w.config.ResourceProfile, time.Now())
	if err != nil {
		return receipt, err
	}
	eligible = true
	// Stop before the lease deadline so the local fence and DC technical ACK
	// have a bounded chance to commit. A later claim obtains a new attempt.
	if !fence.ExpiresAt.After(time.Now().Add(time.Second)) {
		return receipt, ErrExpiredPrepareLease
	}
	ctx, cancel := context.WithDeadline(ctx, fence.ExpiresAt.Add(-time.Second))
	defer cancel()
	remote, err := w.builds.GetBuild(ctx, input.BuildID)
	if err != nil {
		return receipt, fmt.Errorf("read fixed RTW build: %w", err)
	}
	if remote.BuildId != input.BuildID || remote.ModuleId != input.ModuleID || remote.ReleaseId != input.ReleaseID ||
		remote.Generation != input.Generation || remote.ManifestHash != input.InputHash || remote.CancelVersion != fence.CancelVersion || remote.State != "BUILDING" {
		return receipt, fmt.Errorf("%w: RTW build differs from DC fixed job", content.ErrConflict)
	}
	_, err = w.builds.ClaimBuild(ctx, ridethewind.ClaimBuildReq{BuildId: input.BuildID,
		Generation: input.Generation, ManifestHash: input.InputHash, CancelVersion: fence.CancelVersion,
		AttemptId: fence.AttemptID, LeaseEpoch: fence.LeaseEpoch, LeaseExpiresAt: fence.ExpiresAt.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return receipt, fmt.Errorf("claim RTW build: %w", err)
	}
	option, err := content.PrepareGraphRunOption(input, fence)
	if err != nil {
		return receipt, err
	}
	request := runtime.Request{Subject: runtime.SubjectRef{AuthorityID: "datacenter", TenantID: "technical",
		SubjectID: job.Request.Producer}, SessionID: job.ID, RunID: job.ID + ":" + job.AttemptID,
		Message: model.NewUserMessage("prepare fixed content build"), Options: []agent.RunOption{option}}
	var graphCompletions int
	runResult, err := w.runner.Run(ctx, request, func(_ context.Context, e *event.Event) error {
		value, graphDone, decodeErr := content.PrepareGraphReceiptFromCompletion(e)
		if decodeErr != nil {
			return decodeErr
		}
		if graphDone {
			graphCompletions++
			if graphCompletions != 1 {
				return content.ErrPrepareGraphOutput
			}
			receipt = value
		}
		return nil
	})
	if err != nil {
		return receipt, fmt.Errorf("run content prepare graph: %w", err)
	}
	if !runResult.Completed || graphCompletions != 1 || receipt.BuildID != input.BuildID ||
		receipt.ReleaseID != input.ReleaseID || receipt.Generation != input.Generation ||
		receipt.OperationID != input.OperationID || receipt.AttemptID != fence.AttemptID ||
		receipt.LeaseEpoch != fence.LeaseEpoch || receipt.CancelVersion != fence.CancelVersion || receipt.State != "BUILDING" {
		return receipt, content.ErrPrepareGraphOutput
	}
	stored, err := w.store.Get(ctx, input.BuildID)
	if err != nil {
		return receipt, fmt.Errorf("read committed content build: %w", err)
	}
	if stored.State != "BUILDING" || stored.Chunks == nil || *stored.Chunks != receipt.ChunkManifest ||
		stored.AttemptID != fence.AttemptID || stored.LeaseEpoch != fence.LeaseEpoch || stored.CancelVersion != fence.CancelVersion ||
		stored.ModuleID != input.ModuleID || stored.ReleaseID != input.ReleaseID || stored.Generation != input.Generation ||
		stored.InputHash != input.InputHash || stored.OperationID != input.OperationID {
		return receipt, fmt.Errorf("%w: graph receipt differs from committed content state", content.ErrConflict)
	}
	raw, err := w.objects.Get(ctx, receipt.ChunkManifest)
	if err != nil {
		return receipt, fmt.Errorf("read committed chunk artifact: %w", err)
	}
	var chunks corpus.ChunkManifest
	if err := json.Unmarshal(raw, &chunks); err != nil || chunks.ModuleID != input.ModuleID ||
		chunks.ReleaseID != input.ReleaseID || chunks.InputManifestHash != input.InputHash || len(chunks.Chunks) != receipt.ChunkCount {
		return receipt, content.ErrPrepareGraphOutput
	}
	completed := jobs.Result{State: "succeeded", Ref: &jobs.ResultRef{URI: "sha256:" + receipt.ChunkManifest.SHA256,
		Hash: receipt.ChunkManifest.SHA256, MediaType: prepareResultMediaType}}
	terminalAttempted = true
	if _, err := w.jobs.CompleteJob(ctx, job.ID, jobs.Complete{Lease: jobs.Lease{WorkerID: w.config.WorkerID,
		AttemptID: fence.AttemptID, LeaseEpoch: fence.LeaseEpoch, CancelVersion: fence.CancelVersion}, Result: completed}); err != nil {
		// The receipt could have been lost after DC committed. Read the exact job
		// before deciding whether this attempt remains uncertain.
		confirmed, readErr := w.jobs.GetJob(ctx, job.ID)
		if readErr == nil && confirmed.State == "succeeded" && confirmed.WorkerID == w.config.WorkerID &&
			confirmed.AttemptID == fence.AttemptID && confirmed.LeaseEpoch == fence.LeaseEpoch &&
			confirmed.CancelVersion == fence.CancelVersion && confirmed.Result != nil && confirmed.Result.State == "succeeded" && confirmed.Result.Ref != nil &&
			*confirmed.Result.Ref == *completed.Ref {
			return receipt, nil
		}
		return receipt, fmt.Errorf("complete content prepare job: %w", err)
	}
	return receipt, nil
}

func (w *PrepareWorker) reportFailure(parent context.Context, job jobs.Job, cause error) error {
	if job.LeaseExpiresAt == "" {
		return nil
	}
	expires, err := time.Parse(time.RFC3339Nano, job.LeaseExpiresAt)
	if err != nil || !expires.After(time.Now().Add(250*time.Millisecond)) {
		return nil
	}
	code, retryable := prepareWorkerFailure(cause)
	ctx, cancel := context.WithDeadline(context.WithoutCancel(parent), expires.Add(-250*time.Millisecond))
	defer cancel()
	_, err = w.jobs.CompleteJob(ctx, job.ID, jobs.Complete{Lease: jobs.Lease{WorkerID: w.config.WorkerID,
		AttemptID: job.AttemptID, LeaseEpoch: job.LeaseEpoch, CancelVersion: job.CancelVersion},
		Result: jobs.Result{State: "failed", ErrorCode: code, Retryable: retryable}})
	if err != nil {
		return fmt.Errorf("report content prepare failure: %w", err)
	}
	return nil
}

func prepareWorkerFailure(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrInvalidPrepareJob), errors.Is(err, content.ErrInvalid), errors.Is(err, content.ErrInvalidated):
		return "content_prepare_invalid", false
	case errors.Is(err, ErrExpiredPrepareLease), errors.Is(err, content.ErrConflict):
		return "content_prepare_conflict", true
	default:
		return "content_prepare_failed", true
	}
}

func prepareWorkerOutcome(err error) (string, string) {
	if err == nil {
		return "succeeded", ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled", "CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed_out", "TIMEOUT"
	}
	code, _ := prepareWorkerFailure(err)
	return "failed", code
}
