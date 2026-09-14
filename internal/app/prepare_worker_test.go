package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

type workerTestExporter struct{}

func (workerTestExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (workerTestExporter) Shutdown(context.Context) error                             { return nil }

var workerObservationOnce sync.Once
var workerObservation *telemetry.Bundle
var workerObservationErr error
var workerOutput bytes.Buffer

func testWorkerObservation(t *testing.T) *telemetry.Bundle {
	t.Helper()
	workerObservationOnce.Do(func() {
		workerObservation, workerObservationErr = telemetry.New(context.Background(), telemetry.Config{
			Service: "btw-prepare-worker-test", Environment: "test", Version: "fixture-revision", InstanceID: "test-worker",
			Output: &workerOutput, Level: slog.LevelInfo, TraceExporter: workerTestExporter{}, SampleRatio: 1,
		})
		if workerObservationErr == nil {
			workerObservationErr = workerObservation.InstallGlobals()
		}
	})
	if workerObservationErr != nil {
		t.Fatal(workerObservationErr)
	}
	return workerObservation
}

func TestMain(m *testing.M) {
	code := m.Run()
	if workerObservation != nil {
		if err := workerObservation.Close(context.Background()); err != nil {
			code = 1
		}
	}
	os.Exit(code)
}

type fakePrepareJobs struct {
	job         jobs.Job
	claimErr    error
	completeErr error
	completed   []jobs.Complete
	getOverride *jobs.Job
}

func (f *fakePrepareJobs) ClaimJob(context.Context, jobs.Claim) (jobs.Job, error) {
	if f.claimErr != nil {
		return jobs.Job{}, f.claimErr
	}
	return f.job, nil
}
func (f *fakePrepareJobs) CompleteJob(_ context.Context, _ string, result jobs.Complete) (jobs.CompletionReceipt, error) {
	f.completed = append(f.completed, result)
	if f.completeErr != nil {
		return jobs.CompletionReceipt{}, f.completeErr
	}
	return jobs.CompletionReceipt{TechnicalState: result.Result.State}, nil
}
func (f *fakePrepareJobs) GetJob(context.Context, string) (jobs.Job, error) {
	if f.getOverride != nil {
		copy := *f.getOverride
		if copy.Result == nil && len(f.completed) > 0 {
			copy.Result = &f.completed[len(f.completed)-1].Result
		}
		return copy, nil
	}
	if len(f.completed) > 0 && f.completed[len(f.completed)-1].Result.State == "succeeded" {
		copy := f.job
		copy.State = "succeeded"
		copy.Result = &f.completed[len(f.completed)-1].Result
		return copy, nil
	}
	return f.job, nil
}

type fakePrepareBuilds struct {
	build            ridethewind.Build
	claims           []ridethewind.ClaimBuildReq
	claimErr         error
	grantBeforeError bool
}

func (f *fakePrepareBuilds) GetBuild(context.Context, string) (ridethewind.Build, error) {
	return f.build, nil
}
func (f *fakePrepareBuilds) ClaimBuild(_ context.Context, req ridethewind.ClaimBuildReq) (ridethewind.Build, error) {
	f.claims = append(f.claims, req)
	if f.claimErr != nil && !f.grantBeforeError {
		return ridethewind.Build{}, f.claimErr
	}
	if req.LeaseEpoch == 0 {
		if f.build.AttemptId != req.AttemptId {
			f.build.LeaseEpoch++
		}
	} else {
		f.build.LeaseEpoch = req.LeaseEpoch
	}
	f.build.AttemptId, f.build.CancelVersion, f.build.LeaseExpiresAt =
		req.AttemptId, req.CancelVersion, req.LeaseExpiresAt
	if f.claimErr != nil {
		return ridethewind.Build{}, f.claimErr
	}
	return f.build, nil
}

func TestPrepareWorkerRecoversOnlyCommittedRTWGrantAfterLostReply(t *testing.T) {
	for _, committed := range []bool{true, false} {
		t.Run(map[bool]string{true: "committed", false: "not_committed"}[committed], func(t *testing.T) {
			worker, dc, rtw := newPreparedWorkerFixture(t, nil)
			rtw.claimErr = errors.New("RTW claim reply lost")
			rtw.grantBeforeError = committed
			worked, err := worker.RunOnce(context.Background())
			if !worked {
				t.Fatal("DC prepare job was not claimed")
			}
			if committed {
				if err != nil || len(dc.completed) != 1 || dc.completed[0].Result.State != "succeeded" ||
					rtw.build.LeaseEpoch != 1 || len(rtw.claims) != 1 || rtw.claims[0].LeaseEpoch != 0 {
					t.Fatalf("committed prepare grant did not recover: err=%v build=%+v DC=%+v", err, rtw.build, dc.completed)
				}
			} else if err == nil || len(dc.completed) != 1 || dc.completed[0].Result.State != "failed" {
				t.Fatalf("uncommitted prepare grant falsely succeeded: err=%v DC=%+v", err, dc.completed)
			}
		})
	}
}

type fakePreparedStore struct{ build content.Build }

func (f *fakePreparedStore) Get(context.Context, string) (content.Build, error) { return f.build, nil }

type fakePreparer struct {
	objects artifacts.Store
	store   *fakePreparedStore
	failure error
}

func (f *fakePreparer) Prepare(ctx context.Context, input content.BuildInput, fence content.Fence) (content.Prepared, error) {
	if f.failure != nil {
		return content.Prepared{}, f.failure
	}
	manifest := corpus.ChunkManifest{SchemaVersion: 1, ModuleID: input.ModuleID, ReleaseID: input.ReleaseID,
		InputManifestHash: input.InputHash, Chunks: []corpus.Chunk{{ID: "chunk-1", Text: "synthetic"}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return content.Prepared{}, err
	}
	ref, err := f.objects.Put(ctx, raw)
	if err != nil {
		return content.Prepared{}, err
	}
	f.store.build = content.Build{BuildInput: input, Fence: fence, State: "BUILDING", Chunks: &ref}
	return content.Prepared{Manifest: manifest, Ref: ref, Build: f.store.build}, nil
}

func newPreparedWorkerFixture(t *testing.T, failure error) (*PrepareWorker, *fakePrepareJobs, *fakePrepareBuilds) {
	t.Helper()
	now := time.Now().UTC()
	job := testPrepareJob(t, now)
	jobsClient := &fakePrepareJobs{job: job}
	input, fence, err := DecodePrepareClaim(job, "worker-1", PrepareJobType, "cpu", now)
	if err != nil {
		t.Fatal(err)
	}
	builds := &fakePrepareBuilds{build: ridethewind.Build{BuildId: input.BuildID, ModuleId: input.ModuleID,
		ReleaseId: input.ReleaseID, Generation: input.Generation, ManifestHash: input.InputHash,
		CancelVersion: fence.CancelVersion, State: "BUILDING"}}
	objects, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &fakePreparedStore{}
	ag, err := content.NewPrepareGraphAgent(&fakePreparer{objects: objects, store: store, failure: failure})
	if err != nil {
		t.Fatal(err)
	}
	sessions := inmemory.NewSessionService()
	t.Cleanup(func() { _ = sessions.Close() })
	runner, err := runtime.New("content-worker-test", ag, sessions, testWorkerObservation(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close() })
	worker, err := NewPrepareWorker(PrepareWorkerConfig{WorkerID: "worker-1", ResourceProfile: "cpu", LeaseSeconds: 45},
		jobsClient, builds, runner, store, objects, testWorkerObservation(t))
	if err != nil {
		t.Fatal(err)
	}
	return worker, jobsClient, builds
}

func TestPrepareWorkerCompletesTechnicalChunkJobThroughGraph(t *testing.T) {
	worker, dc, rtw := newPreparedWorkerFixture(t, nil)
	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked || len(rtw.claims) != 1 || len(dc.completed) != 1 {
		t.Fatalf("worked=%t err=%v RTW claims=%d DC completions=%d", worked, err, len(rtw.claims), len(dc.completed))
	}
	if rtw.claims[0].LeaseEpoch != 0 || rtw.build.LeaseEpoch != 1 ||
		dc.completed[0].LeaseEpoch != dc.job.LeaseEpoch || dc.job.LeaseEpoch != 2 {
		t.Fatalf("prepare conflated DC epoch and RTW build fence: claim=%+v build=%+v DC=%+v",
			rtw.claims[0], rtw.build, dc.completed[0])
	}
	completed := dc.completed[0].Result
	if completed.State != "succeeded" || completed.Ref == nil || completed.Ref.MediaType != prepareResultMediaType ||
		completed.Ref.URI != "sha256:"+completed.Ref.Hash || completed.ErrorCode != "" {
		t.Fatalf("technical chunk receipt invalid: %+v", completed)
	}
	if rtw.build.State != "BUILDING" {
		t.Fatal("chunk preparation promoted RTW build to READY")
	}
}

func TestPrepareWorkerGraphFailureCannotAcknowledgeSuccess(t *testing.T) {
	worker, dc, _ := newPreparedWorkerFixture(t, content.ErrInvalid)
	worked, err := worker.RunOnce(context.Background())
	if !worked || err == nil || len(dc.completed) != 1 || dc.completed[0].Result.State != "failed" ||
		dc.completed[0].Result.Ref != nil || dc.completed[0].Result.Retryable == false {
		t.Fatalf("graph error or failure ack lost: worked=%t err=%v completed=%+v", worked, err, dc.completed)
	}
}

func TestPrepareWorkerNoWorkHasNoEffects(t *testing.T) {
	worker, dc, rtw := newPreparedWorkerFixture(t, nil)
	dc.claimErr = datacenter.ErrNoWork
	worked, err := worker.RunOnce(context.Background())
	if worked || err != nil || len(rtw.claims) != 0 || len(dc.completed) != 0 {
		t.Fatalf("empty poll changed state: %t %v %+v %+v", worked, err, rtw.claims, dc.completed)
	}
}

func TestPrepareWorkerRejectsTypedNilProvider(t *testing.T) {
	worker, _, _ := newPreparedWorkerFixture(t, nil)
	var missing *fakePrepareJobs
	if _, err := NewPrepareWorker(worker.config, missing, worker.builds, worker.runner, worker.store, worker.objects, worker.observed); err == nil {
		t.Fatal("typed nil DC provider accepted")
	}
}

func TestPrepareWorkerLostSuccessReceiptChecksCommittedJob(t *testing.T) {
	worker, dc, _ := newPreparedWorkerFixture(t, nil)
	dc.completeErr = errors.New("synthetic receipt lost")
	worked, err := worker.RunOnce(context.Background())
	if !worked || err != nil || len(dc.completed) != 1 || dc.completed[0].Result.State != "succeeded" {
		t.Fatalf("lost receipt caused false failure: worked=%t err=%v completed=%+v", worked, err, dc.completed)
	}
}

func TestPrepareWorkerDoesNotAdoptAnotherAttemptAfterLostReceipt(t *testing.T) {
	worker, dc, _ := newPreparedWorkerFixture(t, nil)
	dc.completeErr = errors.New("synthetic receipt lost")
	other := dc.job
	other.State = "succeeded"
	other.AttemptID = "other-attempt"
	dc.getOverride = &other
	worked, err := worker.RunOnce(context.Background())
	if !worked || err == nil || len(dc.completed) != 1 || dc.completed[0].Result.State != "succeeded" {
		t.Fatalf("foreign attempt accepted or rewritten: worked=%t err=%v completed=%+v", worked, err, dc.completed)
	}
}
