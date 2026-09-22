package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"github.com/google/uuid"
)

func TestWikiCompileWorkerResultRefGatesAcrossAcceptedRevision(t *testing.T) {
	if os.Getenv("SEA_WIKI_RESULT_GATES_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWikiCompileWorkerResultRefGatesAcrossAcceptedRevision$", "-test.v")
		cmd.Env = append(os.Environ(), "SEA_WIKI_RESULT_GATES_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native accepted revision gates failed: %v\n%s", err, output)
		}
		return
	}
	var logs bytes.Buffer
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-wiki-result-test",
		Environment: "test", Version: "candidate", InstanceID: "result-fixture",
		Output: &logs, Level: slog.LevelInfo, TraceExporter: &factSpanExporter{}, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer observed.Close(context.Background())
	frozen := wikiWorkerFixtureSession(t, "11111111-1111-4111-8111-111111111111", 0x44)
	newScenario := func(t *testing.T) (jobs.Job, *wikiWorkerOwnerFixture,
		*wikiWorkerJobsFixture, *wikiWorkerTrackedObjects, *wikiWorkerFixedModel) {
		t.Helper()
		job, compile, revisions := wikiWorkerFixtureJob(t)
		job.ID, job.AttemptID = uuid.NewString(), uuid.NewString()
		local, err := artifacts.NewLocal(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		objects := &wikiWorkerTrackedObjects{store: local}
		freshRevisions := make(map[string]ridethewind.Revision, len(revisions))
		for id, revision := range revisions {
			freshRevisions[id] = revision
		}
		owner := &wikiWorkerOwnerFixture{compile: compile, revisions: freshRevisions, objects: objects}
		jobsClient := &wikiWorkerJobsFixture{job: job, owner: owner}
		return job, owner, jobsClient, objects, &wikiWorkerFixedModel{}
	}
	open := func(t *testing.T, job jobs.Job, owner *wikiWorkerOwnerFixture,
		jobsClient *wikiWorkerJobsFixture, objects *wikiWorkerTrackedObjects,
		model *wikiWorkerFixedModel, build WikiCompileCompletionRef) *WikiCompileWorker {
		t.Helper()
		worker, err := NewWikiCompileWorker(WikiCompileWorkerConfig{WorkerID: job.WorkerID,
			LeaseSeconds: 20, ModelSession: frozen, CompletionRef: build,
			ResultRefContractID: WikiCompileResultRefContractID}, jobsClient, owner,
			wikiWorkerRunFixture{model: model, observed: observed}, objects, observed)
		if err != nil {
			t.Fatal(err)
		}
		return worker
	}

	t.Run("manual-head-before-accept-rejects-old-base", func(t *testing.T) {
		job, owner, jobsClient, objects, model := newScenario(t)
		owner.headRevisionID = "manual-revision-new-head"
		result, err := open(t, job, owner, jobsClient, objects, model,
			BuildWikiCompileResultRef).ProcessClaim(context.Background(), job)
		if !errors.Is(err, ErrWikiCompileAcceptPending) || result.Accepted.State != "" ||
			objects.puts != 1 || jobsClient.completes != 0 || owner.compile.State != "BUILDING" {
			t.Fatalf("manual edit before RTW Accept escaped into result manifest/ACK: %+v %v", result, err)
		}
	})
	t.Run("withdrawn-accepted-revision-before-manifest", func(t *testing.T) {
		job, owner, jobsClient, objects, model := newScenario(t)
		owner.withdrawWikiOnRead = 1
		result, err := open(t, job, owner, jobsClient, objects, model,
			BuildWikiCompileResultRef).ProcessClaim(context.Background(), job)
		if !errors.Is(err, ErrWikiCompileTechnicalPending) || result.Accepted.State != "ACCEPTED" ||
			objects.puts != 1 || jobsClient.completes != 0 {
			t.Fatalf("withdrawn accepted Wiki revision reached manifest/ACK: %+v %v", result, err)
		}
	})
	t.Run("withdrawn-accepted-revision-after-manifest", func(t *testing.T) {
		job, owner, jobsClient, objects, model := newScenario(t)
		owner.withdrawWikiOnRead = 2
		result, err := open(t, job, owner, jobsClient, objects, model,
			BuildWikiCompileResultRef).ProcessClaim(context.Background(), job)
		if !errors.Is(err, ErrWikiCompileTechnicalPending) || result.Accepted.State != "ACCEPTED" ||
			objects.puts != 2 || jobsClient.completes != 0 {
			t.Fatalf("manifest was confused with DC successful completion after withdrawal: %+v %v", result, err)
		}
	})
	t.Run("withdrawn-fixed-source-after-manifest", func(t *testing.T) {
		job, owner, jobsClient, objects, model := newScenario(t)
		owner.withdrawSourceOnRead = 3
		result, err := open(t, job, owner, jobsClient, objects, model,
			BuildWikiCompileResultRef).ProcessClaim(context.Background(), job)
		if !errors.Is(err, ErrWikiCompileTechnicalPending) || result.Accepted.State != "ACCEPTED" ||
			objects.puts != 2 || jobsClient.completes != 0 {
			t.Fatalf("withdrawn source after manifest reached DC success: %+v %v", result, err)
		}
	})
	t.Run("bad-ref-without-readable-manifest", func(t *testing.T) {
		job, owner, jobsClient, objects, model := newScenario(t)
		forged := func(context.Context, WikiCompileCompletionEvidence, artifacts.Store) (jobs.ResultRef, error) {
			hash := strings.Repeat("f", 64)
			return jobs.ResultRef{URI: "sha256:" + hash, Hash: hash,
				MediaType: wikiCompileResultMediaType}, nil
		}
		result, err := open(t, job, owner, jobsClient, objects, model,
			forged).ProcessClaim(context.Background(), job)
		if !errors.Is(err, ErrWikiCompileTechnicalPending) || result.Accepted.State != "ACCEPTED" ||
			objects.puts != 1 || jobsClient.completes != 0 {
			t.Fatalf("forged manifest-shaped Ref reached DC success: %+v %v", result, err)
		}
	})
	t.Run("DC-cancel-after-manifest", func(t *testing.T) {
		job, owner, jobsClient, objects, model := newScenario(t)
		buildThenCancel := func(ctx context.Context, e WikiCompileCompletionEvidence,
			store artifacts.Store) (jobs.ResultRef, error) {
			ref, err := BuildWikiCompileResultRef(ctx, e, store)
			jobsClient.job.State, jobsClient.job.CancelVersion = "cancel_requested", jobsClient.job.CancelVersion+1
			return ref, err
		}
		result, err := open(t, job, owner, jobsClient, objects, model,
			buildThenCancel).ProcessClaim(context.Background(), job)
		if !errors.Is(err, ErrWikiCompileCancelTerminalConfirmed) || result.Accepted.State != "ACCEPTED" ||
			objects.puts != 2 || jobsClient.acks != 1 || jobsClient.completes != 0 {
			t.Fatalf("DC cancellation after manifest was misreported as success: %+v %v", result, err)
		}
	})
	t.Run("DC-old-epoch-after-manifest", func(t *testing.T) {
		job, owner, jobsClient, objects, model := newScenario(t)
		buildThenMoveLease := func(ctx context.Context, e WikiCompileCompletionEvidence,
			store artifacts.Store) (jobs.ResultRef, error) {
			ref, err := BuildWikiCompileResultRef(ctx, e, store)
			jobsClient.job.LeaseEpoch++
			return ref, err
		}
		result, err := open(t, job, owner, jobsClient, objects, model,
			buildThenMoveLease).ProcessClaim(context.Background(), job)
		if !errors.Is(err, ErrWikiCompileFence) || result.Accepted.State != "ACCEPTED" ||
			objects.puts != 2 || jobsClient.completes != 0 {
			t.Fatalf("moved DC lease after manifest reached success: %+v %v", result, err)
		}
	})
	t.Run("DC-complete-CAS-conflict-keeps-manifest-partial", func(t *testing.T) {
		job, owner, jobsClient, objects, model := newScenario(t)
		jobsClient.rejectComplete = true
		result, err := open(t, job, owner, jobsClient, objects, model,
			BuildWikiCompileResultRef).ProcessClaim(context.Background(), job)
		if !errors.Is(err, ErrWikiCompileTechnicalPending) || result.Accepted.State != "ACCEPTED" ||
			result.TechnicalComplete || objects.puts != 2 || jobsClient.completes != 1 ||
			jobsClient.job.State != "running" || jobsClient.job.Result != nil {
			t.Fatalf("DC CAS conflict promoted immutable manifest to successful ACK: %+v %v", result, err)
		}
	})
	t.Run("manual-head-after-accept-keeps-historical-job-valid", func(t *testing.T) {
		job, owner, jobsClient, objects, model := newScenario(t)
		buildThenManualEdit := func(ctx context.Context, e WikiCompileCompletionEvidence,
			store artifacts.Store) (jobs.ResultRef, error) {
			ref, err := BuildWikiCompileResultRef(ctx, e, store)
			owner.headRevisionID = "manual-revision-after-accept"
			return ref, err
		}
		result, err := open(t, job, owner, jobsClient, objects, model,
			buildThenManualEdit).ProcessClaim(context.Background(), job)
		if err != nil || !result.TechnicalComplete || result.Accepted.State != "ACCEPTED" ||
			owner.headRevisionID != "manual-revision-after-accept" ||
			objects.puts != 2 || jobsClient.completes != 1 {
			t.Fatalf("valid old AI job was blocked or replaced new manual head: %+v %v", result, err)
		}
	})
	if !bytes.Contains(logs.Bytes(), []byte(`"event":"content.wiki_compile.process.finished"`)) ||
		!bytes.Contains(logs.Bytes(), []byte(`"outcome":"partial"`)) ||
		!bytes.Contains(logs.Bytes(), []byte(`"outcome":"succeeded"`)) {
		t.Fatalf("result gates lacked bounded application observability: %s", logs.Bytes())
	}
}
