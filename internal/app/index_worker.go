package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

const IndexJobType = "content.build.v1"
const indexResultMediaType = "application/vnd.sea.index-manifest+json"

var ErrInvalidIndexJob = errors.New("invalid fixed content index job")
var ErrExpiredIndexLease = errors.New("content index lease expired")

// IndexJobInput binds the technical job to an already prepared immutable
// content build. Its DC OperationID is independent from BuildInput.OperationID.
type IndexJobInput struct {
	BuildID           string                `json:"build_id"`
	ReleaseID         string                `json:"release_id"`
	Generation        int64                 `json:"generation"`
	InputManifestHash string                `json:"input_manifest_hash"`
	ResumeIndexes     map[string]corpus.Ref `json:"resume_indexes,omitempty"`
}

func DecodeIndexClaim(job jobs.Job, workerID, resourceProfile string, now time.Time) (IndexJobInput, content.Fence, error) {
	var input IndexJobInput
	if workerID == "" || resourceProfile == "" || job.ID == "" || !artifacts.ValidHash(job.InputHash) ||
		job.State != "running" || job.WorkerID != workerID || job.Request.JobType != IndexJobType ||
		job.Request.ResourceProfile != resourceProfile || job.Request.Producer == "" || job.Request.OperationID == "" ||
		job.AttemptID == "" || job.LeaseEpoch <= 0 || job.CancelVersion < 0 {
		return input, content.Fence{}, ErrInvalidIndexJob
	}
	decoder := json.NewDecoder(bytes.NewReader(job.Request.Input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return IndexJobInput{}, content.Fence{}, fmt.Errorf("%w: %v", ErrInvalidIndexJob, err)
	}
	if decoder.Decode(new(any)) != io.EOF || input.BuildID == "" || input.ReleaseID == "" || input.Generation <= 0 ||
		!artifacts.ValidHash(input.InputManifestHash) {
		return IndexJobInput{}, content.Fence{}, ErrInvalidIndexJob
	}
	for lane, ref := range input.ResumeIndexes {
		if lane != "dense" && lane != "sparse" && lane != "multivector" ||
			!artifacts.ValidHash(ref.SHA256) || ref.Key != "sha256/"+ref.SHA256 {
			return IndexJobInput{}, content.Fence{}, ErrInvalidIndexJob
		}
	}
	expires, err := time.Parse(time.RFC3339Nano, job.LeaseExpiresAt)
	if err != nil {
		return IndexJobInput{}, content.Fence{}, fmt.Errorf("%w: invalid lease expiry", ErrInvalidIndexJob)
	}
	if !expires.After(now) {
		return IndexJobInput{}, content.Fence{}, ErrExpiredIndexLease
	}
	fence := content.Fence{BuildID: input.BuildID, AttemptID: job.AttemptID, LeaseEpoch: job.LeaseEpoch,
		CancelVersion: job.CancelVersion, ExpiresAt: expires}
	return input, fence, nil
}

type IndexJobClient interface {
	ClaimJob(context.Context, jobs.Claim) (jobs.Job, error)
	CompleteJob(context.Context, string, jobs.Complete) (jobs.CompletionReceipt, error)
	GetJob(context.Context, string) (jobs.Job, error)
}

type IndexBuildClient interface {
	GetBuild(context.Context, string) (ridethewind.Build, error)
	ClaimBuild(context.Context, ridethewind.ClaimBuildReq) (ridethewind.Build, error)
}

type IndexRunner interface {
	Run(context.Context, runtime.Request, runtime.Sink) (runtime.Result, error)
}

type IndexBuildStore interface {
	Get(context.Context, string) (content.Build, error)
}

type IndexWorkerConfig struct {
	WorkerID        string
	ResourceProfile string
	LeaseSeconds    int
}

// IndexWorker completes only the DC technical indexing job. The caller owns
// Runtime, artifact store, clients and Bundle. RTW AcceptBuild is not called.
type IndexWorker struct {
	config   IndexWorkerConfig
	jobs     IndexJobClient
	builds   IndexBuildClient
	runner   IndexRunner
	store    IndexBuildStore
	objects  artifacts.Store
	observed *telemetry.Bundle
}

func NewIndexWorker(cfg IndexWorkerConfig, jobs IndexJobClient, builds IndexBuildClient, runner IndexRunner,
	store IndexBuildStore, objects artifacts.Store, observed *telemetry.Bundle) (*IndexWorker, error) {
	if cfg.WorkerID == "" || cfg.ResourceProfile == "" || cfg.LeaseSeconds < 5 || cfg.LeaseSeconds > 3600 ||
		nilDependency(jobs) || nilDependency(builds) || nilDependency(runner) || nilDependency(store) ||
		nilDependency(objects) || observed == nil || !observed.Installed() {
		return nil, errors.New("index worker requires fixed identity, providers, runner, store and installed telemetry")
	}
	return &IndexWorker{cfg, jobs, builds, runner, store, objects, observed}, nil
}

func (w *IndexWorker) RunOnce(ctx context.Context) (bool, error) {
	started := time.Now()
	job, err := w.jobs.ClaimJob(ctx, jobs.Claim{WorkerID: w.config.WorkerID, JobType: IndexJobType,
		ResourceProfile: w.config.ResourceProfile, LeaseSeconds: w.config.LeaseSeconds})
	if errors.Is(err, datacenter.ErrNoWork) {
		return false, nil
	}
	if err != nil {
		if logger, logErr := w.observed.Logger("content", "application"); logErr == nil {
			logger.ErrorContext(ctx, "claim content index job failed", "event", "content.index_job.claim_failed",
				"outcome", "failed", "duration_ms", float64(time.Since(started))/float64(time.Millisecond),
				"error_code", "DC_CLAIM_FAILED", "error_type", fmt.Sprintf("%T", err), "error_message", err.Error())
		}
		return false, fmt.Errorf("claim content index job: %w", err)
	}
	_, err = w.ProcessClaim(ctx, job)
	return true, err
}

func sameIndexBuild(b content.Build, input IndexJobInput) bool {
	return b.BuildInput.BuildID == input.BuildID && b.ReleaseID == input.ReleaseID && b.Generation == input.Generation &&
		b.InputHash == input.InputManifestHash && b.ModuleID != "" && b.OperationID != "" && b.Chunks != nil
}

func sameRTWIndexBuild(b ridethewind.Build, fixed content.Build, fence content.Fence) bool {
	expires, err := time.Parse(time.RFC3339Nano, b.LeaseExpiresAt)
	return err == nil && b.State == "BUILDING" && b.BuildId == fixed.BuildInput.BuildID && b.ReleaseId == fixed.ReleaseID &&
		b.ModuleId == fixed.ModuleID && b.Generation == fixed.Generation && b.ManifestHash == fixed.InputHash &&
		b.AttemptId == fence.AttemptID && b.LeaseEpoch == fence.LeaseEpoch && b.CancelVersion == fence.CancelVersion &&
		expires.Equal(fence.ExpiresAt)
}

// ProcessClaim consumes one complete project Runtime run, including all Graph
// and Runner terminal events. Only a matching committed PG READY result can
// become a successful DC technical result Ref.
func (w *IndexWorker) ProcessClaim(parent context.Context, job jobs.Job) (receipt content.IndexGraphReceipt, resultErr error) {
	ctx, stage, err := w.observed.Begin(parent, "content", "content.worker.index", slog.String("job_id", job.ID),
		slog.String("operation_id", job.Request.OperationID), slog.String("attempt_id", job.AttemptID),
		slog.Int64("lease_epoch", job.LeaseEpoch), slog.Int64("cancel_version", job.CancelVersion))
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
		outcome, code := indexWorkerOutcome(resultErr)
		stage.End(ctx, outcome, code, resultErr, slog.String("index_manifest_hash", receipt.IndexManifest.SHA256))
	}()
	input, fence, err := DecodeIndexClaim(job, w.config.WorkerID, w.config.ResourceProfile, time.Now())
	if err != nil {
		return receipt, err
	}
	eligible = true
	if !fence.ExpiresAt.After(time.Now().Add(time.Second)) {
		return receipt, ErrExpiredIndexLease
	}
	ctx, cancel := context.WithDeadline(ctx, fence.ExpiresAt.Add(-time.Second))
	defer cancel()
	fixed, err := w.store.Get(ctx, input.BuildID)
	if err != nil {
		return receipt, fmt.Errorf("read prepared content build: %w", err)
	}
	if !sameIndexBuild(fixed, input) || (fixed.State != "BUILDING" && fixed.State != "READY") {
		return receipt, fmt.Errorf("%w: DC job differs from prepared content build", content.ErrConflict)
	}
	remote, err := w.builds.GetBuild(ctx, input.BuildID)
	if err != nil {
		return receipt, fmt.Errorf("read fixed RTW build: %w", err)
	}
	if remote.State != "BUILDING" || remote.BuildId != fixed.BuildInput.BuildID || remote.ModuleId != fixed.ModuleID ||
		remote.ReleaseId != fixed.ReleaseID || remote.Generation != fixed.Generation || remote.ManifestHash != fixed.InputHash ||
		remote.CancelVersion != fence.CancelVersion {
		return receipt, fmt.Errorf("%w: RTW build differs from prepared content build", content.ErrConflict)
	}
	claimed, err := w.builds.ClaimBuild(ctx, ridethewind.ClaimBuildReq{BuildId: input.BuildID,
		Generation: input.Generation, ManifestHash: input.InputManifestHash, CancelVersion: fence.CancelVersion,
		AttemptId: fence.AttemptID, LeaseEpoch: fence.LeaseEpoch, LeaseExpiresAt: fence.ExpiresAt.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return receipt, fmt.Errorf("claim RTW index build: %w", err)
	}
	if !sameRTWIndexBuild(claimed, fixed, fence) {
		return receipt, fmt.Errorf("%w: RTW claim returned another build or fence", content.ErrConflict)
	}
	option, err := content.IndexGraphRunOption(input.BuildID, fence, input.ResumeIndexes)
	if err != nil {
		return receipt, err
	}
	request := runtime.Request{Subject: runtime.SubjectRef{AuthorityID: "datacenter", TenantID: "technical",
		SubjectID: job.Request.Producer}, SessionID: job.ID, RunID: job.ID + ":" + job.AttemptID,
		Message: model.NewUserMessage("index fixed content generation"), Options: []agent.RunOption{option}}
	var graphCompletions int
	runResult, err := w.runner.Run(ctx, request, func(_ context.Context, e *event.Event) error {
		value, graphDone, decodeErr := content.IndexGraphReceiptFromCompletion(e)
		if decodeErr != nil {
			return decodeErr
		}
		if graphDone {
			graphCompletions++
			if graphCompletions != 1 {
				return content.ErrIndexGraphOutput
			}
			receipt = value
		}
		return nil
	})
	if err != nil {
		return receipt, fmt.Errorf("run content index graph: %w", err)
	}
	if !runResult.Completed || graphCompletions != 1 || receipt.BuildID != fixed.BuildInput.BuildID ||
		receipt.ReleaseID != fixed.ReleaseID || receipt.Generation != fixed.Generation ||
		receipt.ChunkManifest != *fixed.Chunks || receipt.AttemptID != fence.AttemptID ||
		receipt.LeaseEpoch != fence.LeaseEpoch || receipt.CancelVersion != fence.CancelVersion || receipt.State != "READY" {
		return receipt, content.ErrIndexGraphOutput
	}
	committed, err := w.store.Get(ctx, input.BuildID)
	if err != nil {
		return receipt, fmt.Errorf("read committed content index: %w", err)
	}
	if !sameIndexBuild(committed, input) || committed.State != "READY" || committed.Result == nil ||
		*committed.Result != receipt.IndexManifest || committed.Chunks == nil || *committed.Chunks != receipt.ChunkManifest ||
		len(committed.Lanes) != 3 {
		return receipt, fmt.Errorf("%w: graph receipt differs from committed content READY", content.ErrConflict)
	}
	if fixed.State == "READY" {
		// The immutable local READY belongs to an earlier successful attempt.
		// A later DC retry may acknowledge that same result after the
		// coordinator revalidates the new RTW claim, without rewriting PG.
		if fixed.Result == nil || *fixed.Result != *committed.Result || fixed.Chunks == nil ||
			*fixed.Chunks != *committed.Chunks || fixed.AttemptID != committed.AttemptID ||
			fixed.LeaseEpoch != committed.LeaseEpoch || fixed.CancelVersion != committed.CancelVersion ||
			!fixed.ExpiresAt.Equal(committed.ExpiresAt) || len(fixed.Lanes) != len(committed.Lanes) {
			return receipt, fmt.Errorf("%w: committed READY changed during replay", content.ErrConflict)
		}
		for _, lane := range []string{"dense", "sparse", "multivector"} {
			if fixed.Lanes[lane] != committed.Lanes[lane] {
				return receipt, fmt.Errorf("%w: READY lane changed during replay", content.ErrConflict)
			}
		}
	} else if committed.AttemptID != fence.AttemptID || committed.LeaseEpoch != fence.LeaseEpoch ||
		committed.CancelVersion != fence.CancelVersion || !committed.ExpiresAt.Equal(fence.ExpiresAt) {
		return receipt, fmt.Errorf("%w: READY was not committed under DC fence", content.ErrConflict)
	}
	for _, lane := range []string{"dense", "sparse", "multivector"} {
		if committed.Lanes[lane] != receipt.Lanes[lane] {
			return receipt, fmt.Errorf("%w: %s lane differs from committed READY", content.ErrConflict, lane)
		}
	}
	raw, err := w.objects.Get(ctx, receipt.IndexManifest)
	if err != nil {
		return receipt, fmt.Errorf("read committed index manifest: %w", err)
	}
	var manifest corpus.IndexManifest
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest.SchemaVersion != 1 ||
		manifest.BuildID != fixed.BuildInput.BuildID || manifest.ReleaseID != fixed.ReleaseID ||
		manifest.Generation != fixed.Generation || manifest.InputManifestHash != fixed.InputHash ||
		manifest.ChunkManifest != receipt.ChunkManifest || len(manifest.Lanes) != 3 {
		return receipt, content.ErrIndexGraphOutput
	}
	seen := make(map[string]bool, 3)
	for _, lane := range manifest.Lanes {
		name := lane.Profile.Lane
		if seen[name] || (name != "dense" && name != "sparse" && name != "multivector") ||
			lane.Artifact != receipt.Lanes[name] || !lane.ProbePassed {
			return receipt, content.ErrIndexGraphOutput
		}
		seen[name] = true
	}
	remote, err = w.builds.GetBuild(ctx, input.BuildID)
	if err != nil {
		return receipt, fmt.Errorf("recheck RTW index claim: %w", err)
	}
	if !sameRTWIndexBuild(remote, fixed, fence) {
		return receipt, fmt.Errorf("%w: RTW index claim moved before technical ACK", content.ErrConflict)
	}
	completed := jobs.Result{State: "succeeded", Ref: &jobs.ResultRef{URI: "sha256:" + receipt.IndexManifest.SHA256,
		Hash: receipt.IndexManifest.SHA256, MediaType: indexResultMediaType}}
	terminalAttempted = true
	if _, err := w.jobs.CompleteJob(ctx, job.ID, jobs.Complete{Lease: jobs.Lease{WorkerID: w.config.WorkerID,
		AttemptID: fence.AttemptID, LeaseEpoch: fence.LeaseEpoch, CancelVersion: fence.CancelVersion}, Result: completed}); err != nil {
		confirmed, readErr := w.jobs.GetJob(ctx, job.ID)
		if readErr == nil && confirmed.State == "succeeded" && confirmed.WorkerID == w.config.WorkerID &&
			confirmed.AttemptID == fence.AttemptID && confirmed.LeaseEpoch == fence.LeaseEpoch &&
			confirmed.CancelVersion == fence.CancelVersion && confirmed.Result != nil && confirmed.Result.State == "succeeded" &&
			confirmed.Result.Ref != nil && *confirmed.Result.Ref == *completed.Ref {
			return receipt, nil
		}
		return receipt, fmt.Errorf("complete content index job: %w", err)
	}
	return receipt, nil
}

func (w *IndexWorker) reportFailure(parent context.Context, job jobs.Job, cause error) error {
	expires, err := time.Parse(time.RFC3339Nano, job.LeaseExpiresAt)
	if err != nil || !expires.After(time.Now().Add(250*time.Millisecond)) {
		return nil
	}
	code, retryable := indexWorkerFailure(cause)
	ctx, cancel := context.WithDeadline(context.WithoutCancel(parent), expires.Add(-250*time.Millisecond))
	defer cancel()
	_, err = w.jobs.CompleteJob(ctx, job.ID, jobs.Complete{Lease: jobs.Lease{WorkerID: w.config.WorkerID,
		AttemptID: job.AttemptID, LeaseEpoch: job.LeaseEpoch, CancelVersion: job.CancelVersion},
		Result: jobs.Result{State: "failed", ErrorCode: code, Retryable: retryable}})
	if err != nil {
		return fmt.Errorf("report content index failure: %w", err)
	}
	return nil
}

func indexWorkerFailure(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrInvalidIndexJob), errors.Is(err, content.ErrInvalid), errors.Is(err, content.ErrInvalidated):
		return "content_index_invalid", false
	case errors.Is(err, ErrExpiredIndexLease), errors.Is(err, content.ErrConflict):
		return "content_index_conflict", true
	default:
		return "content_index_failed", true
	}
}

func indexWorkerOutcome(err error) (string, string) {
	if err == nil {
		return "succeeded", ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled", "CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed_out", "TIMEOUT"
	}
	code, _ := indexWorkerFailure(err)
	return "failed", code
}
