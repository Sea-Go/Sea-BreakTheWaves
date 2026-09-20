package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime"
	"go.opentelemetry.io/otel/trace"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

type fakeIndexStore struct {
	build     content.Build
	dispatch  content.IndexDispatch
	delivered bool
	stage     string
}

func (s *fakeIndexStore) Get(context.Context, string) (content.Build, error) { return s.build, nil }
func (s *fakeIndexStore) AttachIndexDispatch(_ context.Context, buildID, jobID, workerID string,
	dc content.TechnicalFence, fence content.Fence) error {
	if s.dispatch.Fence.LeaseEpoch != fence.LeaseEpoch || s.dispatch.JobID != jobID ||
		s.dispatch.DC.AttemptID != dc.AttemptID || s.dispatch.DC.LeaseEpoch != dc.LeaseEpoch {
		s.delivered = false
	}
	s.dispatch.BuildID, s.dispatch.JobID, s.dispatch.WorkerID, s.dispatch.Fence = buildID, jobID, workerID, fence
	s.dispatch.DC = dc
	s.stage = "pending"
	return nil
}
func (s *fakeIndexStore) ClaimIndexDispatch(_ context.Context, buildID string) (content.IndexDispatch, bool, error) {
	if s.delivered || s.stage != "pending" || s.build.State != "READY" ||
		buildID != "" && buildID != s.dispatch.BuildID {
		return content.IndexDispatch{}, false, nil
	}
	s.dispatch.ClaimEpoch++
	s.dispatch.ClaimUntil = time.Now().Add(30 * time.Second)
	s.dispatch.Result = *s.build.Result
	return s.dispatch, true, nil
}
func (s *fakeIndexStore) NoteIndexRTWAccepted(_ context.Context, claim content.IndexDispatch) error {
	s.dispatch.RTWAccepted = true
	return nil
}
func (s *fakeIndexStore) DeferIndexDispatch(_ context.Context, claim content.IndexDispatch, stage, _ string) error {
	s.stage = stage
	return nil
}
func (s *fakeIndexStore) DeliverIndexDispatch(_ context.Context, claim content.IndexDispatch) error {
	if !s.dispatch.RTWAccepted {
		return content.ErrConflict
	}
	s.delivered, s.stage = true, "complete"
	return nil
}

type fakeIndexBuilds struct {
	build             ridethewind.Build
	claims            []ridethewind.ClaimBuildReq
	claimErr          error
	grantBeforeError  bool
	wrongClaim        bool
	acceptErr         error
	commitBeforeError bool
	accepts           []ridethewind.AcceptBuildReq
}

func (f *fakeIndexBuilds) GetBuild(context.Context, string) (ridethewind.Build, error) {
	return f.build, nil
}
func (f *fakeIndexBuilds) ClaimBuild(_ context.Context, q ridethewind.ClaimBuildReq) (ridethewind.Build, error) {
	f.claims = append(f.claims, q)
	if f.claimErr != nil && !f.grantBeforeError {
		return ridethewind.Build{}, f.claimErr
	}
	if q.LeaseEpoch == 0 {
		if f.build.AttemptId != q.AttemptId {
			f.build.LeaseEpoch++
		}
	} else {
		f.build.LeaseEpoch = q.LeaseEpoch
	}
	f.build.AttemptId, f.build.CancelVersion = q.AttemptId, q.CancelVersion
	f.build.LeaseExpiresAt = q.LeaseExpiresAt
	if f.wrongClaim {
		f.build.LeaseEpoch++
	}
	if f.claimErr != nil {
		return ridethewind.Build{}, f.claimErr
	}
	return f.build, nil
}

func TestIndexWorkerRecoversOnlyCommittedRTWGrantAfterLostReply(t *testing.T) {
	for _, committed := range []bool{true, false} {
		t.Run(map[bool]string{true: "committed", false: "not_committed"}[committed], func(t *testing.T) {
			worker, dc, rtw, store, _ := newIndexWorkerFixture(t, nil)
			rtw.claimErr = errors.New("RTW claim reply lost")
			rtw.grantBeforeError = committed
			worked, err := worker.RunOnce(context.Background())
			if !worked {
				t.Fatal("DC job was not claimed")
			}
			if committed {
				if err != nil || !store.delivered || store.build.State != "READY" ||
					len(dc.completed) != 1 || dc.completed[0].Result.State != "succeeded" ||
					len(rtw.claims) != 1 || rtw.claims[0].LeaseEpoch != 0 {
					t.Fatalf("committed RTW grant did not recover: err=%v store=%+v DC=%+v", err, store, dc.completed)
				}
			} else if err == nil || store.delivered || store.build.State == "READY" ||
				len(dc.completed) != 1 || dc.completed[0].Result.State != "failed" {
				t.Fatalf("uncommitted RTW grant promoted local READY: err=%v store=%+v DC=%+v", err, store, dc.completed)
			}
		})
	}
}
func (f *fakeIndexBuilds) AcceptBuild(_ context.Context, q ridethewind.AcceptBuildReq) (ridethewind.Build, error) {
	f.accepts = append(f.accepts, q)
	if f.acceptErr == nil || f.commitBeforeError {
		f.build.State, f.build.IndexManifestRef, f.build.IndexManifestHash = q.State, q.IndexManifestRef, q.IndexManifestHash
	}
	if f.acceptErr != nil {
		return ridethewind.Build{}, f.acceptErr
	}
	return f.build, nil
}

type fakeIndexer struct {
	store         *fakeIndexStore
	objects       artifacts.Store
	failure       error
	parentTrace   trace.TraceID
	observedTrace trace.TraceID
	oldReady      bool
	badProbe      bool
}

func (f *fakeIndexer) Index(ctx context.Context, id string, fence content.Fence, _ map[string]corpus.Ref) (content.IndexBuildResult, error) {
	f.observedTrace = trace.SpanContextFromContext(ctx).TraceID()
	if f.failure != nil {
		return content.IndexBuildResult{}, f.failure
	}
	b := f.store.build
	if b.BuildInput.BuildID != id || b.Chunks == nil {
		return content.IndexBuildResult{}, content.ErrConflict
	}
	lanes := map[string]corpus.Ref{}
	for _, lane := range []string{"dense", "sparse", "multivector"} {
		lanes[lane] = artifacts.Reference([]byte("synthetic " + lane))
	}
	manifest := corpus.IndexManifest{SchemaVersion: 1, BuildID: id, ReleaseID: b.ReleaseID,
		Generation: b.Generation, InputManifestHash: b.InputHash, ChunkManifest: *b.Chunks,
		Lanes: []corpus.LaneManifest{{Profile: corpus.Profile{Lane: "dense"}, Artifact: lanes["dense"], ProbePassed: true},
			{Profile: corpus.Profile{Lane: "sparse"}, Artifact: lanes["sparse"], ProbePassed: true},
			{Profile: corpus.Profile{Lane: "multivector"}, Artifact: lanes["multivector"], ProbePassed: true}}}
	if f.badProbe {
		manifest.Lanes[2].ProbePassed = false
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return content.IndexBuildResult{}, err
	}
	ref, err := f.objects.Put(ctx, raw)
	if err != nil {
		return content.IndexBuildResult{}, err
	}
	if f.oldReady {
		if b.Result == nil || *b.Result != ref {
			return content.IndexBuildResult{}, content.ErrConflict
		}
	} else {
		b.Fence = fence
		b.State, b.Result, b.Lanes = "READY", &ref, lanes
		f.store.build = b
	}
	return content.IndexBuildResult{BuildID: id, ReleaseID: b.ReleaseID, Generation: b.Generation,
		ChunkManifest: *b.Chunks, Lanes: lanes, IndexManifest: ref, State: "READY"}, nil
}

func testIndexJob(t *testing.T, now time.Time) jobs.Job {
	t.Helper()
	input := IndexJobInput{BuildID: "build-1", ReleaseID: "release-1", Generation: 3,
		InputManifestHash: strings.Repeat("a", 64)}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return jobs.Job{ID: "job-index-1", InputHash: strings.Repeat("b", 64), State: "running",
		AttemptID: "index-attempt-1", WorkerID: "worker-1", LeaseEpoch: 9,
		LeaseExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
		Request: jobs.Submit{Producer: "ridethewind", OperationID: "index-operation-2", RunRef: "run-index-1",
			JobType: IndexJobType, ResourceProfile: "cpu", Input: raw}}
}

func newIndexWorkerFixture(t *testing.T, failure error) (*IndexWorker, *fakePrepareJobs, *fakeIndexBuilds, *fakeIndexStore, *fakeIndexer) {
	t.Helper()
	now := time.Now().UTC()
	job := testIndexJob(t, now)
	input, _, err := DecodeIndexClaim(job, "worker-1", "cpu", now)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chunks := artifacts.Reference([]byte("prepared chunks"))
	store := &fakeIndexStore{build: content.Build{BuildInput: content.BuildInput{
		BuildID: input.BuildID, ModuleID: "module-1", ReleaseID: input.ReleaseID,
		Generation: input.Generation, InputHash: input.InputManifestHash,
		OperationID: "original-prepare-operation", Revisions: []string{"revision-1"}},
		State: "BUILDING", Chunks: &chunks, Lanes: map[string]corpus.Ref{}}}
	builds := &fakeIndexBuilds{build: ridethewind.Build{BuildId: input.BuildID, ModuleId: store.build.ModuleID,
		ReleaseId: input.ReleaseID, Generation: input.Generation, ManifestHash: input.InputManifestHash,
		CancelVersion: job.CancelVersion, State: "BUILDING"}}
	indexer := &fakeIndexer{store: store, objects: objects, failure: failure}
	ag, err := content.NewIndexGraphAgent(indexer)
	if err != nil {
		t.Fatal(err)
	}
	sessions := inmemory.NewSessionService()
	t.Cleanup(func() { _ = sessions.Close() })
	runner, err := runtime.New("index-worker-test", ag, sessions, testWorkerObservation(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close() })
	dc := &fakePrepareJobs{job: job}
	worker, err := NewIndexWorker(IndexWorkerConfig{WorkerID: "worker-1", ResourceProfile: "cpu", LeaseSeconds: 45},
		dc, builds, runner, store, objects, testWorkerObservation(t))
	if err != nil {
		t.Fatal(err)
	}
	return worker, dc, builds, store, indexer
}

func TestDecodeIndexClaimSeparatesTechnicalAndStableOperations(t *testing.T) {
	now := time.Now().UTC()
	job := testIndexJob(t, now)
	input, fence, err := DecodeIndexClaim(job, "worker-1", "cpu", now)
	if err != nil || input.BuildID != fence.BuildID || fence.AttemptID != job.AttemptID ||
		job.Request.OperationID != "index-operation-2" {
		t.Fatalf("input=%+v fence=%+v err=%v", input, fence, err)
	}
	for _, mutate := range []func(*jobs.Job){
		func(j *jobs.Job) { j.WorkerID = "other" },
		func(j *jobs.Job) { j.State = "cancel_requested" },
		func(j *jobs.Job) { j.Request.JobType = PrepareJobType },
		func(j *jobs.Job) { j.Request.Input = append(j.Request.Input, []byte(` {}`)...) },
		func(j *jobs.Job) { j.Request.Input = []byte(`{"build_id":"b","unknown":true}`) },
	} {
		bad := job
		mutate(&bad)
		if _, _, err := DecodeIndexClaim(bad, "worker-1", "cpu", now); !errors.Is(err, ErrInvalidIndexJob) {
			t.Fatalf("invalid claim accepted: %v", err)
		}
	}
	job.LeaseExpiresAt = now.Format(time.RFC3339Nano)
	if _, _, err := DecodeIndexClaim(job, "worker-1", "cpu", now); !errors.Is(err, ErrExpiredIndexLease) {
		t.Fatalf("expired lease accepted: %v", err)
	}
}

func TestIndexWorkerAcksOnlyCommittedReadyThroughFramework(t *testing.T) {
	worker, dc, rtw, store, indexer := newIndexWorkerFixture(t, nil)
	ctx, parent := testWorkerObservation(t).Tracer().Start(context.Background(), "index-test-parent")
	worked, err := worker.RunOnce(ctx)
	parent.End()
	if err != nil || !worked || len(dc.completed) != 1 || len(rtw.claims) != 1 {
		t.Fatalf("worked=%t err=%v completed=%+v claims=%+v", worked, err, dc.completed, rtw.claims)
	}
	if dc.completed[0].Result.State != "succeeded" || dc.completed[0].Result.Ref == nil ||
		dc.completed[0].Result.Ref.MediaType != indexResultMediaType ||
		dc.completed[0].Result.Ref.Hash != store.build.Result.SHA256 ||
		store.build.OperationID != "original-prepare-operation" || rtw.build.State != "READY" || !store.delivered {
		t.Fatalf("RTW acceptance/DC ACK changed fixed input or missed outbox: %+v %+v %+v", dc.completed, store.build, rtw.build)
	}
	if !indexer.observedTrace.IsValid() || indexer.observedTrace != parent.SpanContext().TraceID() {
		t.Fatalf("Index Graph lost parent trace: %s vs %s", indexer.observedTrace, parent.SpanContext().TraceID())
	}
	metrics := httptest.NewRecorder()
	testWorkerObservation(t).MetricsHandler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if metrics.Code != 200 || !strings.Contains(metrics.Body.String(), "trpc_agent_go_agent_") ||
		!strings.Contains(metrics.Body.String(), `sea_btw_operations_total{component="content",outcome="succeeded"}`) {
		t.Fatalf("index worker missing framework/native operation metrics: status=%d", metrics.Code)
	}
	var finished bool
	for _, line := range strings.Split(strings.TrimSpace(workerOutput.String()), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("worker emitted non-JSON log line: %v", err)
		}
		if entry["event"] == "content.worker.index.finished" && entry["outcome"] == "succeeded" {
			finished = true
			if entry["trace_id"] == "" || entry["duration_ms"] == nil || entry["job_id"] != dc.job.ID {
				t.Fatalf("index worker JSON lost correlation/terminal fields: %+v", entry)
			}
		}
	}
	if !finished {
		t.Fatal("no terminal index worker JSON record")
	}
}

func TestIndexWorkerFailureAndForeignClaimNeverAckReady(t *testing.T) {
	for _, scenario := range []string{"graph_error", "wrong_rtw_claim", "stale_local", "bad_probe_manifest", "no_work"} {
		t.Run(scenario, func(t *testing.T) {
			worker, dc, rtw, store, indexer := newIndexWorkerFixture(t, nil)
			switch scenario {
			case "graph_error":
				// The framework surfaces the node failure as a terminal Event.
				worker, dc, rtw, store, _ = newIndexWorkerFixture(t, content.ErrConflict)
			case "wrong_rtw_claim":
				rtw.wrongClaim = true
			case "stale_local":
				store.build.Generation++
			case "bad_probe_manifest":
				indexer.badProbe = true
			case "no_work":
				dc.claimErr = datacenter.ErrNoWork
			}
			worked, err := worker.RunOnce(context.Background())
			if scenario == "no_work" {
				if worked || err != nil || len(dc.completed) != 0 {
					t.Fatalf("no work changed state: %t %v %+v", worked, err, dc.completed)
				}
				return
			}
			if !worked || err == nil || len(dc.completed) != 1 || dc.completed[0].Result.State != "failed" ||
				dc.completed[0].Result.Ref != nil {
				t.Fatalf("failure promoted READY: %t %v %+v %+v", worked, err, dc.completed, store.build)
			}
			if scenario != "bad_probe_manifest" && store.build.State == "READY" {
				t.Fatalf("failure committed local READY: %+v", store.build)
			}
		})
	}
}

func TestIndexWorkerLostReceiptAndNewAttemptReplay(t *testing.T) {
	worker, dc, _, store, indexer := newIndexWorkerFixture(t, nil)
	dc.completeErr = errors.New("receipt lost after commit")
	worked, err := worker.RunOnce(context.Background())
	if !worked || err != nil || store.build.State != "READY" || len(dc.completed) != 1 {
		t.Fatalf("lost success receipt failed recovery: %t %v %+v", worked, err, dc.completed)
	}
	oldFence, oldRef := store.build.Fence, *store.build.Result
	indexer.oldReady = true
	newJob := dc.job
	newJob.AttemptID, newJob.LeaseEpoch = "index-attempt-2", dc.job.LeaseEpoch+1
	dc.job, dc.completeErr, dc.completed = newJob, nil, nil
	worked, err = worker.RunOnce(context.Background())
	if !worked || err != nil || len(dc.completed) != 1 || dc.completed[0].Result.State != "succeeded" ||
		store.build.Fence != oldFence || *store.build.Result != oldRef {
		t.Fatalf("new DC attempt rewrote old READY: %t %v %+v %+v", worked, err, dc.completed, store.build)
	}
}

func TestIndexDispatchUnknownRTWReplyAndScannerRecovery(t *testing.T) {
	t.Run("committed_reply_lost", func(t *testing.T) {
		worker, dc, rtw, store, _ := newIndexWorkerFixture(t, nil)
		rtw.acceptErr = errors.New("RTW reply lost after commit")
		rtw.commitBeforeError = true
		worked, err := worker.RunOnce(context.Background())
		if !worked || err != nil || !store.delivered || len(dc.completed) != 1 ||
			rtw.build.State != "READY" || len(rtw.accepts) != 1 {
			t.Fatalf("uncertain committed RTW receipt not recovered: %t %v %+v", worked, err, store)
		}
	})
	t.Run("restart_scans_unaccepted_outbox", func(t *testing.T) {
		worker, dc, rtw, store, _ := newIndexWorkerFixture(t, nil)
		rtw.acceptErr = errors.New("RTW unavailable before commit")
		worked, err := worker.RunOnce(context.Background())
		if !worked || err == nil || store.delivered || len(dc.completed) != 0 || store.stage != "pending" {
			t.Fatalf("failed RTW accepted prematurely: %t %v %+v", worked, err, store)
		}
		// Simulate a newly constructed worker: only the persisted Store and
		// authoritative remote states are retained, not a method-local receipt.
		rtw.acceptErr = nil
		restarted := *worker
		worked, err = restarted.RunOnce(context.Background())
		if !worked || err != nil || !store.delivered || len(dc.completed) != 1 || len(rtw.accepts) != 2 {
			t.Fatalf("restart did not scan/finish READY outbox: %t %v %+v", worked, err, store)
		}
	})
}

func TestIndexDispatchTerminalAndStaleAttemptsNeverAck(t *testing.T) {
	for _, scenario := range []string{"stale_rtw_claim", "withdrawn_rtw_build", "dc_attempt_exhausted"} {
		t.Run(scenario, func(t *testing.T) {
			worker, dc, rtw, store, _ := newIndexWorkerFixture(t, nil)
			rtw.acceptErr = errors.New("prepare durable outbox before RTW commit")
			if worked, err := worker.RunOnce(context.Background()); !worked || err == nil || store.build.State != "READY" {
				t.Fatalf("did not prepare pending READY: %t %v", worked, err)
			}
			rtw.acceptErr = nil
			switch scenario {
			case "stale_rtw_claim":
				rtw.build.LeaseEpoch++
			case "withdrawn_rtw_build":
				rtw.build.State = "CANCELLED"
			case "dc_attempt_exhausted":
				failed := dc.job
				failed.State, failed.Attempt, failed.Request.MaxAttempts = "failed", 1, 1
				dc.getOverride = &failed
			}
			worked, err := worker.DispatchReadyOnce(context.Background(), "")
			if !worked || err == nil || store.delivered || len(dc.completed) != 0 {
				t.Fatalf("bad dispatch promoted DC: scenario=%s worked=%t err=%v store=%+v", scenario, worked, err, store)
			}
			expect := "needs_new_attempt"
			if scenario != "stale_rtw_claim" {
				expect = "manual"
			}
			if store.stage != expect {
				t.Fatalf("stage=%s want=%s", store.stage, expect)
			}
		})
	}
}

func TestIndexDispatchRevalidatesOldRTWAcceptanceForNewDCAttempt(t *testing.T) {
	worker, dc, rtw, store, _ := newIndexWorkerFixture(t, nil)
	if worked, err := worker.RunOnce(context.Background()); !worked || err != nil || !store.delivered {
		t.Fatalf("initial accepted build: %t %v", worked, err)
	}
	oldRef := *store.build.Result
	newFence := store.dispatch.Fence
	newFence.AttemptID, newFence.LeaseEpoch = "retry-attempt", newFence.LeaseEpoch+1
	newFence.ExpiresAt = time.Now().Add(time.Minute)
	if err := store.AttachIndexDispatch(context.Background(), store.build.BuildInput.BuildID, "retry-job", "worker-1",
		content.TechnicalFence{AttemptID: newFence.AttemptID, LeaseEpoch: newFence.LeaseEpoch,
			CancelVersion: newFence.CancelVersion, ExpiresAt: newFence.ExpiresAt}, newFence); err != nil {
		t.Fatal(err)
	}
	if !store.dispatch.RTWAccepted {
		t.Fatal("fixture did not retain acceptance before re-reading immutable READY")
	}
	rtw.build.IndexManifestHash = strings.Repeat("f", 64)
	worked, err := worker.DispatchReadyOnce(context.Background(), store.build.BuildInput.BuildID)
	if !worked || err == nil || store.delivered || store.stage != "manual" ||
		len(dc.completed) != 1 || *store.build.Result != oldRef {
		t.Fatalf("stale RTW accepted marker hid current remote mismatch: worked=%t err=%v store=%+v", worked, err, store)
	}
}
