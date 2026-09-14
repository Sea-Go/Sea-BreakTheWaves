package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"go.opentelemetry.io/otel/trace"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

type fakeIndexStore struct{ build content.Build }

func (s *fakeIndexStore) Get(context.Context, string) (content.Build, error) { return s.build, nil }

type fakeIndexBuilds struct {
	build      ridethewind.Build
	claims     []ridethewind.ClaimBuildReq
	wrongClaim bool
}

func (f *fakeIndexBuilds) GetBuild(context.Context, string) (ridethewind.Build, error) {
	return f.build, nil
}
func (f *fakeIndexBuilds) ClaimBuild(_ context.Context, q ridethewind.ClaimBuildReq) (ridethewind.Build, error) {
	f.claims = append(f.claims, q)
	f.build.AttemptId, f.build.LeaseEpoch, f.build.CancelVersion = q.AttemptId, q.LeaseEpoch, q.CancelVersion
	f.build.LeaseExpiresAt = q.LeaseExpiresAt
	if f.wrongClaim {
		f.build.LeaseEpoch++
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
		store.build.OperationID != "original-prepare-operation" || rtw.build.State != "BUILDING" {
		t.Fatalf("technical ACK changed fixed input/publication: %+v %+v %+v", dc.completed, store.build, rtw.build)
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
	newJob.ID, newJob.AttemptID, newJob.LeaseEpoch = "job-index-2", "index-attempt-2", oldFence.LeaseEpoch+1
	dc.job, dc.completeErr, dc.completed = newJob, nil, nil
	worked, err = worker.RunOnce(context.Background())
	if !worked || err != nil || len(dc.completed) != 1 || dc.completed[0].Result.State != "succeeded" ||
		store.build.Fence != oldFence || *store.build.Result != oldRef {
		t.Fatalf("new DC attempt rewrote old READY: %t %v %+v %+v", worked, err, dc.completed, store.build)
	}
}
