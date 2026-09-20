package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

var ErrWikiCompileSource = errors.New("wiki compile RTW source differs from fixed ticket")
var ErrWikiCompileFence = errors.New("wiki compile RTW or DC execution fence moved")
var ErrWikiCompileRun = errors.New("wiki compile native Runner did not finish one validated candidate")
var ErrWikiCompileObject = errors.New("wiki compile candidate object is not byte-readable")
var ErrWikiCompileAcceptPending = errors.New("wiki compile RTW acceptance outcome is unconfirmed")
var ErrWikiCompileTechnicalPending = errors.New("wiki compile DC technical completion contract is pending")
var ErrWikiCompileCancelTerminalConfirmed = errors.New("wiki compile DC cancellation terminal state confirmed for current attempt")
var ErrWikiCompileCancelTerminalPending = errors.New("wiki compile DC cancellation terminal state is unconfirmed")

type WikiCompileJobClient interface {
	ClaimJob(context.Context, jobs.Claim) (jobs.Job, error)
	GetJob(context.Context, string) (jobs.Job, error)
	AcknowledgeCancellation(context.Context, string, jobs.Lease) (jobs.Job, error)
	CompleteJob(context.Context, string, jobs.Complete) (jobs.CompletionReceipt, error)
}

type WikiCompileOwner interface {
	GetCompile(context.Context, string) (ridethewind.Compile, error)
	GetRevision(context.Context, string) (ridethewind.Revision, error)
	ClaimCompile(context.Context, ridethewind.ClaimCompileReq) (ridethewind.Compile, error)
	AcceptCompile(context.Context, ridethewind.AcceptCompileReq) (ridethewind.Compile, error)
}

// WikiCompileRunFactory creates one request-specific native Graph/Runner and
// DC-authorized model for a frozen logical Compile. The technical jobs token
// is not an identity for DC's native app-user model Gateway.
type WikiCompileRunFactory interface {
	Open(context.Context, content.WikiCompileInput, WikiCompileModelSession) (WikiCompileRun, error)
}

type WikiCompileRun interface {
	Run(context.Context, runtime.Request, runtime.Sink) (runtime.Result, error)
	ModelSessionProof() WikiCompileModelSessionProof
	Close() error
}

// The authoritative v1 adapter is BuildWikiCompileResultRef. The caller must
// pass current DC/RTW snapshots and the byte-readable candidate object.
// Nil keeps DC technical success disabled.
type WikiCompileCompletionRef func(context.Context, WikiCompileCompletionEvidence,
	artifacts.Store) (jobs.ResultRef, error)

type WikiCompileWorkerConfig struct {
	WorkerID            string
	LeaseSeconds        int
	CompletionRef       WikiCompileCompletionRef
	ResultRefContractID string
	ModelSession        WikiCompileModelSession
}

type WikiCompileResult struct {
	Candidate         content.WikiCompileCandidate
	Accepted          ridethewind.Compile
	TechnicalReceipt  jobs.CompletionReceipt
	TechnicalComplete bool
}

// WikiCompileWorker hides the RTW source, DC lease, native Runner, shared
// object and business acceptance sequence behind one claim-processing seam.
// It never publishes a Release or rewrites a human Wiki revision.
type WikiCompileWorker struct {
	config   WikiCompileWorkerConfig
	jobs     WikiCompileJobClient
	owner    WikiCompileOwner
	runs     WikiCompileRunFactory
	objects  artifacts.Store
	observed *telemetry.Bundle
}

func NewWikiCompileWorker(cfg WikiCompileWorkerConfig, jobs WikiCompileJobClient,
	owner WikiCompileOwner, runs WikiCompileRunFactory, objects artifacts.Store,
	observed *telemetry.Bundle) (*WikiCompileWorker, error) {
	if !cfg.ModelSession.valid() {
		return nil, ErrWikiCompileModelIdentity
	}
	if cfg.WorkerID == "" || cfg.LeaseSeconds < 5 || cfg.LeaseSeconds > 3600 ||
		(cfg.CompletionRef != nil && cfg.ResultRefContractID != WikiCompileResultRefContractID) ||
		nilDependency(jobs) || nilDependency(owner) || nilDependency(runs) ||
		nilDependency(objects) || observed == nil || !observed.Installed() || observed.Closed() {
		return nil, ErrWikiCompileJobContract
	}
	return &WikiCompileWorker{config: cfg, jobs: jobs, owner: owner,
		runs: runs, objects: objects, observed: observed}, nil
}

func (w *WikiCompileWorker) RunOnce(ctx context.Context) (bool, error) {
	if w == nil || ctx == nil {
		return false, ErrWikiCompileJobContract
	}
	job, err := w.jobs.ClaimJob(ctx, jobs.Claim{WorkerID: w.config.WorkerID,
		JobType: WikiCompileJobType, ResourceProfile: wikiCompileResource,
		LeaseSeconds: w.config.LeaseSeconds})
	if errors.Is(err, datacenter.ErrNoWork) {
		return false, nil
	}
	if err != nil {
		if logger, logErr := w.observed.Logger("content", "application"); logErr == nil {
			logger.ErrorContext(ctx, "claim Wiki compile job failed", "event", "content.wiki_compile.claim_failed",
				"outcome", "failed", "error_code", "DC_CLAIM_FAILED")
		}
		return false, err
	}
	result, err := w.ProcessClaim(ctx, job)
	return result.Accepted.State == "ACCEPTED", err
}

func sameWikiCompileTicket(remote ridethewind.Compile, claim wikiCompileClaim) bool {
	t := claim.Ticket
	if remote.CompileId != t.CompileID || remote.ModuleId != t.ModuleID ||
		remote.PageId != t.PageID || remote.BaseRevisionId != t.BaseRevisionID ||
		remote.InputHash != t.CompileInputHash || remote.Generation != claim.Generation ||
		remote.CancelVersion != claim.Cancel || wikiCompileGuidanceHash(remote.Guidance) != t.GuidanceSHA256 ||
		len(remote.SourceRevisionIds) != len(t.SourceRevisionIDs) {
		return false
	}
	for i, id := range t.SourceRevisionIDs {
		if remote.SourceRevisionIds[i] != id {
			return false
		}
	}
	return true
}

func sameWikiCompileLease(remote ridethewind.Compile, job jobs.Job) bool {
	expiry, err := time.Parse(time.RFC3339Nano, remote.LeaseExpiresAt)
	jobExpiry, jobErr := time.Parse(time.RFC3339Nano, job.LeaseExpiresAt)
	return err == nil && jobErr == nil && expiry.Equal(jobExpiry) && expiry.After(time.Now()) &&
		remote.State == "BUILDING" && remote.AttemptId == job.AttemptID &&
		remote.LeaseEpoch == job.LeaseEpoch && remote.CancelVersion == job.CancelVersion
}

func (w *WikiCompileWorker) currentJob(ctx context.Context, job jobs.Job) error {
	current, err := w.jobs.GetJob(ctx, job.ID)
	if err != nil {
		return ErrWikiCompileFence
	}
	if !sameWikiCompileJobAttempt(current, job) {
		return ErrWikiCompileFence
	}
	if current.State == "cancel_requested" && job.State == "running" &&
		job.WorkerID == w.config.WorkerID &&
		job.CancelVersion >= 0 && job.CancelVersion < int64(^uint64(0)>>1) &&
		current.CancelVersion == job.CancelVersion+1 {
		claimExpiry, claimErr := time.Parse(time.RFC3339Nano, job.LeaseExpiresAt)
		currentExpiry, currentErr := time.Parse(time.RFC3339Nano, current.LeaseExpiresAt)
		if claimErr != nil || currentErr != nil || !claimExpiry.After(time.Now()) ||
			!currentExpiry.After(time.Now()) {
			return ErrWikiCompileFence
		}
		return w.ackCancellation(ctx, job, current.CancelVersion)
	}
	if current.CancelVersion != job.CancelVersion {
		return ErrWikiCompileFence
	}
	if _, err := DecodeWikiCompileClaim(current, w.config.WorkerID, time.Now()); err != nil {
		return ErrWikiCompileFence
	}
	return nil
}

func sameWikiCompileJobAttempt(current, claimed jobs.Job) bool {
	return current.ID == claimed.ID && current.InputHash == claimed.InputHash &&
		current.AttemptID == claimed.AttemptID && current.WorkerID == claimed.WorkerID &&
		current.Attempt == claimed.Attempt && current.LeaseEpoch == claimed.LeaseEpoch &&
		reflect.DeepEqual(current.Request, claimed.Request)
}

// DC's Cancel changes the running attempt to cancel_requested/CV+1. Only
// that same claimant may attempt ACK. A lost reply is recovered by GetJob.
// Even a 200 may return a Job already cancelled by concurrent lease expiry;
// without an applied-ACK receipt, this only proves the exact terminal state.
func (w *WikiCompileWorker) ackCancellation(ctx context.Context, claimed jobs.Job, cancelVersion int64) error {
	lease := jobs.Lease{WorkerID: claimed.WorkerID, AttemptID: claimed.AttemptID,
		LeaseEpoch: claimed.LeaseEpoch, CancelVersion: cancelVersion}
	ack, err := w.jobs.AcknowledgeCancellation(ctx, claimed.ID, lease)
	if err == nil {
		if sameWikiCompileJobAttempt(ack, claimed) && ack.CancelVersion == cancelVersion &&
			ack.State == "cancelled" && ack.Result == nil {
			return ErrWikiCompileCancelTerminalConfirmed
		}
		return ErrWikiCompileFence
	}
	current, readErr := w.jobs.GetJob(ctx, claimed.ID)
	if readErr != nil {
		return ErrWikiCompileCancelTerminalPending
	}
	if !sameWikiCompileJobAttempt(current, claimed) || current.CancelVersion != cancelVersion {
		return ErrWikiCompileFence
	}
	if current.State == "cancelled" && current.Result == nil {
		return ErrWikiCompileCancelTerminalConfirmed
	}
	if current.State == "cancel_requested" {
		return ErrWikiCompileCancelTerminalPending
	}
	return ErrWikiCompileFence
}

func (w *WikiCompileWorker) currentCompile(ctx context.Context, job jobs.Job,
	claim wikiCompileClaim) (ridethewind.Compile, error) {
	remote, err := w.owner.GetCompile(ctx, claim.Ticket.CompileID)
	if err != nil {
		return remote, err
	}
	if !sameWikiCompileTicket(remote, claim) || !sameWikiCompileLease(remote, job) {
		return remote, ErrWikiCompileFence
	}
	return remote, nil
}

func (w *WikiCompileWorker) loadSources(ctx context.Context, remote ridethewind.Compile,
	claim wikiCompileClaim, job jobs.Job) (content.WikiCompileInput, error) {
	input := content.WikiCompileInput{CompileID: remote.CompileId, ModuleID: remote.ModuleId,
		PageID: remote.PageId, BaseRevision: remote.BaseRevisionId,
		SourceIDs: append([]string(nil), remote.SourceRevisionIds...),
		Guidance:  remote.Guidance, InputHash: remote.InputHash, Generation: remote.Generation,
		AttemptID: job.AttemptID, LeaseEpoch: job.LeaseEpoch, CancelVersion: job.CancelVersion}
	input.Sources = make([]content.WikiCompileSource, len(remote.SourceRevisionIds))
	for i, id := range remote.SourceRevisionIds {
		revision, err := w.owner.GetRevision(ctx, id)
		if err != nil {
			return content.WikiCompileInput{}, err
		}
		if revision.RevisionId != id || revision.ModuleId != remote.ModuleId ||
			revision.Kind != "source" || revision.Withdrawn ||
			!artifacts.ValidHash(revision.ContentHash) ||
			revision.ObjectKey != "sha256/"+revision.ContentHash ||
			artifacts.Hash([]byte(revision.Content)) != revision.ContentHash {
			return content.WikiCompileInput{}, ErrWikiCompileSource
		}
		input.Sources[i] = content.WikiCompileSource{RevisionID: id, Kind: revision.Kind,
			Title: revision.Title, Content: revision.Content, SHA256: revision.ContentHash}
	}
	if _, err := content.WikiCompileRunOption(input); err != nil {
		return content.WikiCompileInput{}, ErrWikiCompileSource
	}
	return input, nil
}

func wikiCompileAcceptRequest(job jobs.Job, c content.WikiCompileCandidate,
	objectRef string) ridethewind.AcceptCompileReq {
	refs := make([]ridethewind.SourceRef, len(c.SourceRefs))
	for i, ref := range c.SourceRefs {
		refs[i] = ridethewind.SourceRef{RevisionId: ref.RevisionID, Locator: ref.Locator}
	}
	return ridethewind.AcceptCompileReq{State: "READY", CompileId: c.CompileID,
		Generation: c.Generation, InputHash: c.InputHash, AttemptId: job.AttemptID,
		LeaseEpoch: job.LeaseEpoch, CancelVersion: job.CancelVersion,
		ObjectKey: objectRef, ContentHash: c.ContentSHA256, Title: c.Title, SourceRefs: refs}
}

func sameAcceptedWikiCompile(remote ridethewind.Compile, claim wikiCompileClaim, job jobs.Job) bool {
	return sameWikiCompileTicket(remote, claim) && remote.State == "ACCEPTED" &&
		remote.AttemptId == job.AttemptID && remote.LeaseEpoch == job.LeaseEpoch &&
		remote.CancelVersion == job.CancelVersion && remote.ResultHash != "" && remote.RevisionId != ""
}

func (w *WikiCompileWorker) acceptWithRecovery(ctx context.Context, job jobs.Job,
	claim wikiCompileClaim, request ridethewind.AcceptCompileReq) (ridethewind.Compile, error) {
	accepted, err := w.owner.AcceptCompile(ctx, request)
	if err == nil {
		if !sameAcceptedWikiCompile(accepted, claim, job) {
			return ridethewind.Compile{}, ErrWikiCompileAcceptPending
		}
		return accepted, nil
	}
	// A lost RTW HTTP response is not proof of failure. Read current source
	// state first, then replay the identical request so RTW checks ResultHash.
	current, readErr := w.owner.GetCompile(ctx, claim.Ticket.CompileID)
	if readErr != nil || !sameWikiCompileTicket(current, claim) ||
		!(sameAcceptedWikiCompile(current, claim, job) || sameWikiCompileLease(current, job)) {
		return ridethewind.Compile{}, ErrWikiCompileAcceptPending
	}
	replayed, retryErr := w.owner.AcceptCompile(ctx, request)
	if retryErr != nil || !sameAcceptedWikiCompile(replayed, claim, job) {
		return ridethewind.Compile{}, ErrWikiCompileAcceptPending
	}
	return replayed, nil
}

// readAcceptedEvidence never infers the current editing head from a historical
// revision. RTW Accept itself checked the head/base in its business transaction;
// a later manual edit does not revoke the immutable accepted revision.
func (w *WikiCompileWorker) readAcceptedEvidence(ctx context.Context, job jobs.Job,
	claim wikiCompileClaim, accepted ridethewind.Compile, candidate content.WikiCompileCandidate,
	object corpus.Ref, sourcePack content.WikiCompileInput) (WikiCompileCompletionEvidence, error) {
	var evidence WikiCompileCompletionEvidence
	if err := w.currentJob(ctx, job); err != nil {
		return evidence, err
	}
	currentJob, err := w.jobs.GetJob(ctx, job.ID)
	if err != nil || !sameWikiCompileJobAttempt(currentJob, job) ||
		currentJob.CancelVersion != job.CancelVersion || currentJob.State != "running" ||
		currentJob.Result != nil {
		return evidence, ErrWikiCompileFence
	}
	currentAccepted, err := w.owner.GetCompile(ctx, claim.Ticket.CompileID)
	if err != nil || !sameAcceptedWikiCompile(currentAccepted, claim, currentJob) ||
		currentAccepted.ResultHash != accepted.ResultHash ||
		currentAccepted.RevisionId != accepted.RevisionId ||
		currentAccepted.ErrorCode != "" {
		return evidence, ErrWikiCompileTechnicalPending
	}
	revision, err := w.owner.GetRevision(ctx, currentAccepted.RevisionId)
	if err != nil {
		return evidence, ErrWikiCompileTechnicalPending
	}
	if len(sourcePack.SourceIDs) != len(claim.Ticket.SourceRevisionIDs) ||
		len(sourcePack.Sources) != len(sourcePack.SourceIDs) {
		return evidence, ErrWikiCompileTechnicalPending
	}
	for i, sourceID := range sourcePack.SourceIDs {
		fixed := sourcePack.Sources[i]
		currentSource, err := w.owner.GetRevision(ctx, sourceID)
		if err != nil || sourceID != claim.Ticket.SourceRevisionIDs[i] ||
			currentSource.RevisionId != sourceID || currentSource.Withdrawn ||
			currentSource.Kind != "source" || currentSource.ModuleId != currentAccepted.ModuleId ||
			currentSource.ContentHash != fixed.SHA256 ||
			currentSource.ObjectKey != "sha256/"+fixed.SHA256 ||
			!bytes.Equal([]byte(currentSource.Content), []byte(fixed.Content)) ||
			artifacts.Hash([]byte(currentSource.Content)) != fixed.SHA256 {
			return evidence, ErrWikiCompileTechnicalPending
		}
	}
	evidence = WikiCompileCompletionEvidence{Job: currentJob, Accepted: currentAccepted,
		Revision: revision, Candidate: candidate, CandidateObject: object}
	if _, _, err := wikiResultEvidence(ctx, evidence, w.objects, time.Now()); err != nil {
		return WikiCompileCompletionEvidence{}, ErrWikiCompileTechnicalPending
	}
	return evidence, nil
}

// A callback cannot turn a well-shaped but unreadable or differently bound
// URI into technical success. The manifest bytes must be exactly the JCS of
// the same current evidence and readable from this shared Store.
func (w *WikiCompileWorker) verifyManifestRef(ctx context.Context,
	evidence WikiCompileCompletionEvidence, ref jobs.ResultRef) error {
	manifest, _, err := wikiResultEvidence(ctx, evidence, w.objects, time.Now())
	if err != nil {
		return ErrWikiCompileTechnicalPending
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return ErrWikiCompileTechnicalPending
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return ErrWikiCompileTechnicalPending
	}
	hash := artifacts.Hash(canonical)
	if ref.URI != "sha256:"+hash || ref.Hash != hash ||
		ref.MediaType != wikiCompileResultMediaType {
		return ErrWikiCompileTechnicalPending
	}
	readback, err := w.objects.Get(ctx, corpus.Ref{Key: "sha256/" + hash, SHA256: hash})
	if err != nil || !bytes.Equal(readback, canonical) {
		return ErrWikiCompileTechnicalPending
	}
	return nil
}

func wikiCompileTechnicalResultHash(result jobs.Result) (string, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return "", err
	}
	return artifacts.Hash(canonical), nil
}

func (w *WikiCompileWorker) completeTechnical(ctx context.Context, job jobs.Job,
	claim wikiCompileClaim, accepted ridethewind.Compile,
	candidate content.WikiCompileCandidate, object corpus.Ref,
	sourcePack content.WikiCompileInput) (jobs.CompletionReceipt, error) {
	if w.config.CompletionRef == nil || w.config.ResultRefContractID != WikiCompileResultRefContractID {
		return jobs.CompletionReceipt{}, ErrWikiCompileTechnicalPending
	}
	evidence, err := w.readAcceptedEvidence(ctx, job, claim, accepted, candidate, object, sourcePack)
	if err != nil {
		return jobs.CompletionReceipt{}, err
	}
	ref, err := w.config.CompletionRef(ctx, evidence, w.objects)
	if err != nil || w.verifyManifestRef(ctx, evidence, ref) != nil {
		return jobs.CompletionReceipt{}, ErrWikiCompileTechnicalPending
	}
	// A manifest may remain immutable when a cancellation/withdrawal races us;
	// only the subsequent DC lease CAS can complete the technical job.
	current, err := w.readAcceptedEvidence(ctx, job, claim, accepted, candidate, object, sourcePack)
	if err != nil {
		return jobs.CompletionReceipt{}, err
	}
	if err := w.verifyManifestRef(ctx, current, ref); err != nil {
		return jobs.CompletionReceipt{}, err
	}
	result := jobs.Result{State: "succeeded", Ref: &ref}
	wantResultHash, err := wikiCompileTechnicalResultHash(result)
	if err != nil {
		return jobs.CompletionReceipt{}, ErrWikiCompileTechnicalPending
	}
	request := jobs.Complete{Lease: jobs.Lease{WorkerID: w.config.WorkerID,
		AttemptID: job.AttemptID, LeaseEpoch: job.LeaseEpoch,
		CancelVersion: job.CancelVersion}, Result: result}
	receipt, err := w.jobs.CompleteJob(ctx, job.ID, request)
	if err == nil {
		if receipt.JobID != job.ID || receipt.AttemptID != job.AttemptID ||
			receipt.LeaseEpoch != job.LeaseEpoch || receipt.CancelVersion != job.CancelVersion ||
			receipt.TechnicalState != "succeeded" || receipt.ResultHash != wantResultHash {
			return jobs.CompletionReceipt{}, ErrWikiCompileTechnicalPending
		}
		return receipt, nil
	}
	post, readErr := w.jobs.GetJob(ctx, job.ID)
	if readErr == nil && post.State == "succeeded" &&
		sameWikiCompileJobAttempt(post, job) && post.CancelVersion == job.CancelVersion &&
		post.Result != nil && post.Result.State == "succeeded" &&
		post.Result.Ref != nil && *post.Result.Ref == ref {
		return jobs.CompletionReceipt{JobID: job.ID, AttemptID: job.AttemptID,
			LeaseEpoch: job.LeaseEpoch, CancelVersion: job.CancelVersion,
			TechnicalState: "succeeded"}, nil // GetJob proves status, not receipt hash.
	}
	return jobs.CompletionReceipt{}, ErrWikiCompileTechnicalPending
}

func wikiCompileObservation(err error) (string, string, error) {
	if err == nil {
		return "succeeded", "", nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled", "CANCELLED", context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return "timed_out", "TIMEOUT", context.DeadlineExceeded
	case errors.Is(err, ErrWikiCompileTechnicalPending):
		return "partial", "DC_COMPLETION_CONTRACT_PENDING", ErrWikiCompileTechnicalPending
	case errors.Is(err, ErrWikiCompileAcceptPending):
		return "partial", "RTW_ACCEPT_OUTCOME_UNKNOWN", ErrWikiCompileAcceptPending
	case errors.Is(err, ErrWikiCompileCancelTerminalConfirmed):
		return "cancelled", "DC_CANCELLED_CONFIRMED", ErrWikiCompileCancelTerminalConfirmed
	case errors.Is(err, ErrWikiCompileCancelTerminalPending):
		return "partial", "DC_CANCEL_TERMINAL_PENDING", ErrWikiCompileCancelTerminalPending
	case errors.Is(err, ErrWikiCompileModelIdentity):
		return "rejected", "MODEL_SESSION_MISMATCH", ErrWikiCompileModelIdentity
	case errors.Is(err, ErrWikiCompileJobContract), errors.Is(err, ErrWikiCompileSource),
		errors.Is(err, ErrWikiCompileFence), errors.Is(err, ErrWikiCompileLease):
		return "rejected", "SOURCE_OR_FENCE_REJECTED", ErrWikiCompileJobContract
	default:
		return "failed", "UPSTREAM_OR_RUN_FAILED", ErrWikiCompileRun
	}
}

// ProcessClaim runs only the current job attempt. Even a valid Graph
// completion is insufficient until Runner EOF, RTW source rechecks, object
// readback and RTW business Accept have all succeeded. DC completion is
// separately gated on a versioned ResultRef provider.
func (w *WikiCompileWorker) ProcessClaim(parent context.Context, job jobs.Job) (result WikiCompileResult, resultErr error) {
	if w == nil || parent == nil || w.observed == nil {
		return result, ErrWikiCompileJobContract
	}
	jobID := job.ID
	if len(jobID) > 128 {
		jobID = "invalid"
	}
	ctx, stage, err := w.observed.Begin(parent, "content", "content.wiki_compile.process",
		slog.String("job_id", jobID))
	if err != nil {
		return result, err
	}
	defer func() {
		outcome, code, cause := wikiCompileObservation(resultErr)
		stage.End(ctx, outcome, code, cause,
			slog.Bool("rtw_accepted", result.Accepted.State == "ACCEPTED"),
			slog.Bool("dc_completed", result.TechnicalComplete),
			slog.Bool("dc_cancel_terminal", errors.Is(resultErr, ErrWikiCompileCancelTerminalConfirmed)))
	}()
	claim, err := DecodeWikiCompileClaim(job, w.config.WorkerID, time.Now())
	if err != nil {
		return result, err
	}
	stage.SetAttributes(slog.String("operation_id", job.Request.OperationID),
		slog.String("attempt_id", job.AttemptID), slog.Int64("lease_epoch", job.LeaseEpoch))
	if err := w.currentJob(ctx, job); err != nil {
		return result, err
	}
	remote, err := w.owner.GetCompile(ctx, claim.Ticket.CompileID)
	if err != nil {
		return result, fmt.Errorf("read RTW compile source: %w", err)
	}
	if !sameWikiCompileTicket(remote, claim) {
		return result, ErrWikiCompileSource
	}
	if remote.State == "ACCEPTED" && sameAcceptedWikiCompile(remote, claim, job) {
		result.Accepted = remote
		return result, ErrWikiCompileTechnicalPending // No local candidate/ref proof after restart.
	}
	if remote.State != "BUILDING" || remote.LeaseEpoch > job.LeaseEpoch ||
		remote.CancelVersion != job.CancelVersion {
		return result, ErrWikiCompileFence
	}
	input, err := w.loadSources(ctx, remote, claim, job)
	if err != nil {
		return result, fmt.Errorf("read fixed RTW source revisions: %w", err)
	}
	if err := w.currentJob(ctx, job); err != nil {
		return result, err
	}
	claimed, err := w.owner.ClaimCompile(ctx, ridethewind.ClaimCompileReq{
		CompileId: remote.CompileId, Generation: remote.Generation, InputHash: remote.InputHash,
		AttemptId: job.AttemptID, LeaseEpoch: job.LeaseEpoch,
		CancelVersion: job.CancelVersion, LeaseExpiresAt: job.LeaseExpiresAt})
	if err != nil || !sameWikiCompileTicket(claimed, claim) || !sameWikiCompileLease(claimed, job) {
		return result, ErrWikiCompileFence
	}
	run, err := w.runs.Open(ctx, input, w.config.ModelSession)
	if err != nil || nilDependency(run) {
		return result, ErrWikiCompileRun
	}
	if run.ModelSessionProof() != w.config.ModelSession.Proof() {
		_ = run.Close()
		return result, ErrWikiCompileModelIdentity
	}
	closed := false
	defer func() {
		if !closed {
			_ = run.Close()
		}
	}()
	option, err := content.WikiCompileRunOption(input)
	if err != nil {
		return result, ErrWikiCompileSource
	}
	var graphDone int
	var candidate content.WikiCompileCandidate
	technicalSubject := runtime.SubjectRef{AuthorityID: "ridethewind.knowledge",
		TenantID: "wiki-compile-run", SubjectID: remote.CompileId}
	runReceipt, runErr := run.Run(ctx, runtime.Request{Subject: technicalSubject,
		SessionID: "wiki-compile/" + remote.CompileId + "/" + job.AttemptID,
		RunID:     "wiki-compile:" + job.ID + ":" + job.AttemptID,
		Message:   model.NewUserMessage("compile the fixed RTW Wiki source revisions"),
		Options:   []agent.RunOption{option}}, func(_ context.Context, e *event.Event) error {
		got, done, err := content.WikiCompileCandidateFromCompletion(e, input)
		if err != nil {
			return err
		}
		if done {
			graphDone++
			candidate = got
		}
		return nil
	})
	closeErr := run.Close()
	closed = true
	if run.ModelSessionProof() != w.config.ModelSession.Proof() {
		return result, ErrWikiCompileModelIdentity
	}
	if runErr != nil || closeErr != nil || !runReceipt.Completed || graphDone != 1 ||
		candidate.CompileID != remote.CompileId || candidate.AttemptID != job.AttemptID ||
		candidate.LeaseEpoch != job.LeaseEpoch || candidate.CancelVersion != job.CancelVersion {
		return result, ErrWikiCompileRun
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := w.currentJob(ctx, job); err != nil {
		return result, err
	}
	if _, err := w.currentCompile(ctx, job, claim); err != nil {
		return result, ErrWikiCompileFence
	}
	body := []byte(candidate.Markdown)
	ref, err := w.objects.Put(ctx, body)
	if err != nil || !artifacts.ValidHash(ref.SHA256) ||
		ref.Key != "sha256/"+ref.SHA256 || ref.SHA256 != candidate.ContentSHA256 {
		return result, ErrWikiCompileObject
	}
	readback, err := w.objects.Get(ctx, ref)
	if err != nil || !bytes.Equal(readback, body) {
		return result, ErrWikiCompileObject
	}
	result.Candidate = candidate
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := w.currentJob(ctx, job); err != nil {
		return result, err
	}
	if _, err := w.currentCompile(ctx, job, claim); err != nil {
		return result, ErrWikiCompileFence
	}
	accepted, err := w.acceptWithRecovery(ctx, job, claim,
		wikiCompileAcceptRequest(job, candidate, ref.Key))
	if err != nil {
		return result, err
	}
	result.Accepted = accepted
	receipt, err := w.completeTechnical(ctx, job, claim, accepted, candidate, ref, input)
	if err != nil {
		return result, err
	}
	result.TechnicalReceipt, result.TechnicalComplete = receipt, true
	return result, nil
}
