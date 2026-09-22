package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/jackc/pgx/v5/pgxpool"
	tracecollector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

type cancelReleaseSnapshot struct {
	ActiveReleaseID string `json:"active_release_id"`
	ActiveBuildID   string `json:"active_build_id"`
	PointerRevision int64  `json:"pointer_revision"`
}

func cancelReleaseAdmin(t *testing.T, ctx context.Context, fixture realRTWIndexFixture,
	method, path string, input, output any) {
	t.Helper()
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, fixture.BaseURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+fixture.AdminToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || envelope.Code != http.StatusOK {
		t.Fatalf("RTW admin %s %s status=%d code=%d", method, path, response.StatusCode, envelope.Code)
	}
	if output != nil {
		if err := json.Unmarshal(envelope.Data, output); err != nil {
			t.Fatal(err)
		}
	}
}

// The original module already has a published pointer. A cancelled candidate
// and a new Release/Build generation must never mutate that pointer.
func TestRTWRealBGECancelNewRelease(t *testing.T) {
	fixture, enabled := readRealRTWIndexFixture(t)
	if !enabled {
		t.Skip("set RTW-owned cancellation fixture")
	}
	if fixture.BGERuntimeFile == "" || fixture.DCJobURL == "" || fixture.DCJobDSN == "" ||
		fixture.AdminToken == "" || fixture.PublishedReleaseID == "" ||
		fixture.PublishedBuildID == "" || fixture.PublishedPointerRevision < 1 ||
		os.Getenv("BTW_WORKER_TEST_CONTENT_DSN") == "" ||
		os.Getenv("BTW_WORKER_TEST_SESSION_DSN") == "" {
		t.Fatal("cancel/new-release acceptance needs actual RTW/DC/BGE and disposable PG")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	bge := readLiveBGERuntime(t, fixture.BGERuntimeFile)
	settings := bge.indexSettings()
	rtw, err := ridethewind.New(httpclient.Config{BaseURL: fixture.BaseURL, Token: fixture.WorkerToken,
		HTTPClient: &http.Client{Timeout: 20 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	platform, err := datacenter.New(httpclient.Config{BaseURL: fixture.DCJobURL,
		Token: fixture.DCJobToken, HTTPClient: &http.Client{Timeout: 30 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	dcPool, err := pgxpool.New(ctx, fixture.DCJobDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer dcPool.Close()
	if err := dcPool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := artifacts.NewLocal(fixture.ObjectsDir)
	if err != nil {
		t.Fatal(err)
	}
	oldBuild, err := rtw.GetBuild(ctx, fixture.BuildID)
	oldRelease, releaseErr := rtw.GetRelease(ctx, fixture.ReleaseID)
	if err != nil || releaseErr != nil || oldBuild.State != "BUILDING" ||
		oldBuild.AttemptId != "" || oldBuild.LeaseEpoch != 0 ||
		oldBuild.ReleaseId != oldRelease.ReleaseId || oldRelease.ModuleId != fixture.ModuleID ||
		len(oldRelease.RetrievalProfiles) != 3 {
		t.Fatalf("old candidate is not a fixed unclaimed BGE Build: build=%+v release=%+v errors=%v %v",
			oldBuild, oldRelease, err, releaseErr)
	}
	var publishedBefore cancelReleaseSnapshot
	cancelReleaseAdmin(t, ctx, fixture, "GET", "/v1/knowledge/modules/"+fixture.ModuleID+"/releases/current",
		nil, &publishedBefore)
	if publishedBefore.ActiveReleaseID != fixture.PublishedReleaseID ||
		publishedBefore.ActiveBuildID != fixture.PublishedBuildID ||
		publishedBefore.PointerRevision != fixture.PublishedPointerRevision {
		t.Fatalf("original published pointer changed before candidate execution: %+v", publishedBefore)
	}

	var modelCalls atomic.Int64
	var heldResponse atomic.Bool
	heldResponse.Store(true)
	firstModelReady := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseFirst) })
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-only-bge-cancel-proxy" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		endpoint, token, timeout := fixture.DCJobURL, fixture.DCJobToken, 30*time.Second
		isModel := r.URL.Path == "/v1/representations"
		if isModel {
			endpoint, token, timeout = bge.Endpoint, bge.AccessToken, 3*time.Minute
		}
		forwarded, err := http.NewRequestWithContext(r.Context(), r.Method, endpoint+r.URL.RequestURI(),
			io.LimitReader(r.Body, 8<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		forwarded.Header = r.Header.Clone()
		forwarded.Header.Set("Authorization", "Bearer "+token)
		response, err := (&http.Client{Timeout: timeout}).Do(forwarded)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		if isModel && response.StatusCode == http.StatusOK {
			modelCalls.Add(1)
			if heldResponse.CompareAndSwap(true, false) {
				close(firstModelReady)
				select {
				case <-releaseFirst:
				case <-r.Context().Done():
				}
			}
		}
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(response.Body, 8<<20))
	}))
	defer func() { releaseOnce.Do(func() { close(releaseFirst) }); proxy.Close() }()
	var nativePrepare, nativeIndex atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var export tracecollector.ExportTraceServiceRequest
		if err := proto.Unmarshal(raw, &export); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, resource := range export.GetResourceSpans() {
			for _, scope := range resource.GetScopeSpans() {
				if scope.GetScope().GetName() != "trpc.agent.go" {
					continue
				}
				for _, span := range scope.GetSpans() {
					switch span.GetName() {
					case "invoke_agent content_prepare":
						nativePrepare.Add(1)
					case "invoke_agent content_index":
						nativeIndex.Add(1)
					}
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	binary := filepath.Join(t.TempDir(), "worker-race")
	compile := exec.Command("go", "build", "-race", "-mod=readonly", "-o", binary, "./cmd/worker")
	compile.Dir = "../.."
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile real worker: %v\n%s", err, output)
	}
	version, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	common := validEnvironment()
	common["BTW_DC_URL"], common["BTW_DC_TOKEN"] = proxy.URL, "test-only-bge-cancel-proxy"
	common["BTW_RTW_URL"], common["BTW_RTW_TOKEN"] = fixture.BaseURL, fixture.WorkerToken
	common["BTW_CONTENT_POSTGRES_DSN"] = os.Getenv("BTW_WORKER_TEST_CONTENT_DSN")
	common["BTW_SESSION_POSTGRES_DSN"] = os.Getenv("BTW_WORKER_TEST_SESSION_DSN")
	common["BTW_ARTIFACT_DIR"] = fixture.ObjectsDir
	common["BTW_SERVICE_VERSION"], common["BTW_ENVIRONMENT"] = strings.TrimSpace(string(version)), "test"
	common["BTW_OTLP_TRACES_URL"] = collector.URL + "/v1/traces"
	common["BTW_HTTP_TIMEOUT"], common["BTW_LEASE_SECONDS"], common["BTW_POLL_INTERVAL"] = "60s", "900", "100ms"
	common["BTW_CHUNK_PROFILE_ID"] = fixture.ChunkProfile
	common["BTW_CHUNK_SIZE"], common["BTW_CHUNK_OVERLAP"] = fmt.Sprint(fixture.ChunkSize), fmt.Sprint(fixture.ChunkOverlap)
	common["BTW_CONTENT_MIGRATE"], common["BTW_SESSION_INITIALIZE"] = "true", "true"
	common["BTW_SESSION_TABLE_PREFIX"] = "cancel_old_prepare_"
	common["BTW_METRICS_ADDR"] = bgeProcessMetricsAddr(t)
	submit := func(operation, jobType string, raw []byte) jobs.SubmissionReceipt {
		t.Helper()
		job, err := platform.SubmitJob(ctx, jobs.Submit{Producer: "ridethewind", OperationID: operation,
			RunRef: operation, JobType: jobType, ResourceProfile: "cpu", Input: raw,
			MaxAttempts: 2, Deadline: time.Now().UTC().Add(17 * time.Minute).Format(time.RFC3339Nano)})
		if err != nil {
			t.Fatalf("submit real DC job %s: %v", jobType, err)
		}
		return job
	}
	oldPrepareInput := content.BuildInput{BuildID: oldBuild.BuildId, ModuleID: oldBuild.ModuleId,
		ReleaseID: oldBuild.ReleaseId, Generation: oldBuild.Generation, InputHash: oldBuild.ManifestHash,
		OperationID: "cancel-old-prepare-" + oldBuild.BuildId,
		Revisions:   append(append([]string(nil), oldRelease.SourceRevisionIds...), oldRelease.WikiRevisionIds...)}
	oldPrepareRaw, _ := json.Marshal(oldPrepareInput)
	oldPrepareJob := submit(oldPrepareInput.OperationID, app.PrepareJobType, oldPrepareRaw)
	oldPrepare := runBGEWorkerProcess(t, binary, common)
	waitBGEProcess(t, oldPrepare, 2*time.Minute, func() bool {
		job, err := platform.GetJob(ctx, oldPrepareJob.ID)
		return err == nil && job.State == "succeeded" && job.Result != nil && job.Result.Ref != nil
	})
	oldPrepare.stop(t)
	contentPool, err := pgxpool.New(ctx, common["BTW_CONTENT_POSTGRES_DSN"])
	if err != nil {
		t.Fatal(err)
	}
	defer contentPool.Close()
	store := content.NewStore(contentPool)
	oldLocal, err := store.Get(ctx, oldBuild.BuildId)
	if err != nil || oldLocal.Chunks == nil {
		t.Fatalf("old prepare did not commit chunks: %+v err=%v", oldLocal, err)
	}
	oldChunkRef := *oldLocal.Chunks
	oldIndexInput := app.IndexJobInput{BuildID: oldBuild.BuildId, ReleaseID: oldBuild.ReleaseId,
		Generation: oldBuild.Generation, InputManifestHash: oldBuild.ManifestHash}
	oldIndexRaw, _ := json.Marshal(oldIndexInput)
	oldIndexJob := submit("cancel-old-index-"+oldBuild.BuildId, app.IndexJobType, oldIndexRaw)
	settingsRaw, _ := json.Marshal(settings)
	settingsPath := filepath.Join(t.TempDir(), "bge-index.json")
	if err := os.WriteFile(settingsPath, settingsRaw, 0600); err != nil {
		t.Fatal(err)
	}
	common["BTW_JOB_TYPE"] = app.IndexJobType
	common["BTW_INDEX_CONFIG_FILE"], common["BTW_INDEX_BACKEND"] = settingsPath, "exact"
	common["BTW_CONTENT_MIGRATE"] = "false"
	common["BTW_SESSION_TABLE_PREFIX"] = "cancel_old_index_"
	common["BTW_METRICS_ADDR"] = bgeProcessMetricsAddr(t)
	oldIndex := runBGEWorkerProcess(t, binary, common)
	waitBGEProcess(t, oldIndex, 3*time.Minute, func() bool {
		select {
		case <-firstModelReady:
		default:
			return false
		}
		job, err := platform.GetJob(ctx, oldIndexJob.ID)
		if err != nil || job.State != "running" || job.AttemptID == "" {
			return false
		}
		remote, err := rtw.GetBuild(ctx, oldBuild.BuildId)
		return err == nil && remote.State == "BUILDING" && remote.LeaseEpoch == 2 &&
			remote.AttemptId == job.AttemptID
	})
	oldAttempt, err := platform.GetJob(ctx, oldIndexJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelReceipt, err := platform.CancelJob(ctx, oldIndexJob.ID, jobs.Cancel{
		OperationID:           "cancel-old-index-operation-" + oldBuild.BuildId,
		ExpectedCancelVersion: oldAttempt.CancelVersion, Reason: "new_release_candidate"})
	if err != nil || cancelReceipt.CancelVersion != 1 || cancelReceipt.TechnicalState != "cancel_requested" {
		t.Fatalf("DC did not persist explicit cancellation: %+v err=%v", cancelReceipt, err)
	}
	var cancelledOld struct {
		BuildID          string `json:"build_id"`
		State            string `json:"state"`
		CancelVersion    int64  `json:"cancel_version"`
		IndexManifestRef string `json:"index_manifest_ref"`
	}
	cancelReleaseAdmin(t, ctx, fixture, "POST", "/v1/knowledge/builds/"+oldBuild.BuildId+"/cancel",
		map[string]any{"reason": "new Release generation", "idempotency_key": "cancel-old-build"}, &cancelledOld)
	if cancelledOld.State != "CANCELLED" || cancelledOld.CancelVersion != 1 || cancelledOld.IndexManifestRef != "" {
		t.Fatalf("RTW old Build did not cancel atomically: %+v", cancelledOld)
	}
	releaseOnce.Do(func() { close(releaseFirst) })
	waitBGEProcess(t, oldIndex, 2*time.Minute, func() bool {
		return strings.Contains(oldIndex.output.String(), "\"event\":\"content.worker.index.finished\"")
	})
	oldIndex.stop(t)
	if _, err := platform.AcknowledgeCancellation(ctx, oldIndexJob.ID, jobs.Lease{WorkerID: oldAttempt.WorkerID,
		AttemptID: oldAttempt.AttemptID, LeaseEpoch: oldAttempt.LeaseEpoch,
		CancelVersion: cancelReceipt.CancelVersion}); err != nil {
		t.Fatalf("DC cancel ACK: %v", err)
	}
	oldAfter, err := platform.GetJob(ctx, oldIndexJob.ID)
	oldRTW, remoteErr := rtw.GetBuild(ctx, oldBuild.BuildId)
	if err != nil || remoteErr != nil || oldAfter.State != "cancelled" || oldRTW.State != "CANCELLED" ||
		oldRTW.IndexManifestRef != "" {
		t.Fatalf("old candidate survived cancellation: dc=%+v rtw=%+v errors=%v %v", oldAfter, oldRTW, err, remoteErr)
	}

	var newSource ridethewind.Revision
	cancelReleaseAdmin(t, ctx, fixture, "POST", "/v1/knowledge/modules/"+fixture.ModuleID+"/sources",
		map[string]any{"title": "Replacement source", "content": "west\n\nsouth",
			"media_type": "text/markdown", "provenance": "synthetic", "idempotency_key": "cancel-new-source"}, &newSource)
	if newSource.ModuleId != fixture.ModuleID || newSource.RevisionId == fixture.SourceRevisionIDs[0] {
		t.Fatalf("new source was not an independent revision: %+v", newSource)
	}
	var newRelease ridethewind.Release
	cancelReleaseAdmin(t, ctx, fixture, "POST", "/v1/knowledge/modules/"+fixture.ModuleID+"/releases",
		map[string]any{"source_revision_ids": []string{newSource.RevisionId}, "wiki_revision_ids": []string{},
			"chunking_profile": fixture.ChunkProfile, "retrieval_profiles": oldRelease.RetrievalProfiles,
			"idempotency_key": "cancel-new-release"}, &newRelease)
	if newRelease.ReleaseId == oldRelease.ReleaseId || newRelease.Ordinal != oldRelease.Ordinal+1 ||
		newRelease.ManifestHash == oldRelease.ManifestHash {
		t.Fatalf("new Release ordinal/manifest not distinct: %+v", newRelease)
	}
	var firstNew, newBuild ridethewind.Build
	cancelReleaseAdmin(t, ctx, fixture, "POST", "/v1/knowledge/releases/"+newRelease.ReleaseId+"/index-builds",
		map[string]any{"idempotency_key": "cancel-new-build-first"}, &firstNew)
	cancelReleaseAdmin(t, ctx, fixture, "POST", "/v1/knowledge/releases/"+newRelease.ReleaseId+"/index-builds",
		map[string]any{"idempotency_key": "cancel-new-build-second"}, &newBuild)
	if firstNew.Generation != 1 || newBuild.Generation != 2 || firstNew.BuildId == newBuild.BuildId ||
		newBuild.State != "BUILDING" || newBuild.ReleaseId != newRelease.ReleaseId {
		t.Fatalf("new Release did not assign a distinct Build generation: first=%+v second=%+v", firstNew, newBuild)
	}
	firstNew, err = rtw.GetBuild(ctx, firstNew.BuildId)
	if err != nil || firstNew.State != "SUPERSEDED" {
		t.Fatalf("earlier new generation not superseded: %+v err=%v", firstNew, err)
	}
	newPrepareInput := content.BuildInput{BuildID: newBuild.BuildId, ModuleID: newBuild.ModuleId,
		ReleaseID: newBuild.ReleaseId, Generation: newBuild.Generation, InputHash: newBuild.ManifestHash,
		OperationID: "cancel-new-prepare-" + newBuild.BuildId, Revisions: []string{newSource.RevisionId}}
	newPrepareRaw, _ := json.Marshal(newPrepareInput)
	newPrepareJob := submit(newPrepareInput.OperationID, app.PrepareJobType, newPrepareRaw)
	delete(common, "BTW_JOB_TYPE")
	delete(common, "BTW_INDEX_CONFIG_FILE")
	delete(common, "BTW_INDEX_BACKEND")
	common["BTW_CONTENT_MIGRATE"], common["BTW_SESSION_INITIALIZE"] = "false", "true"
	common["BTW_SESSION_TABLE_PREFIX"] = "cancel_new_prepare_"
	common["BTW_METRICS_ADDR"] = bgeProcessMetricsAddr(t)
	newPrepare := runBGEWorkerProcess(t, binary, common)
	waitBGEProcess(t, newPrepare, 2*time.Minute, func() bool {
		job, err := platform.GetJob(ctx, newPrepareJob.ID)
		return err == nil && job.State == "succeeded" && job.Result != nil && job.Result.Ref != nil
	})
	newPrepare.stop(t)
	newLocal, err := store.Get(ctx, newBuild.BuildId)
	if err != nil || newLocal.Chunks == nil || *newLocal.Chunks == oldChunkRef {
		t.Fatalf("new Release chunk manifest did not differ: %+v err=%v", newLocal, err)
	}
	newIndexInput := app.IndexJobInput{BuildID: newBuild.BuildId, ReleaseID: newBuild.ReleaseId,
		Generation: newBuild.Generation, InputManifestHash: newBuild.ManifestHash}
	newIndexRaw, _ := json.Marshal(newIndexInput)
	newIndexJob := submit("cancel-new-index-"+newBuild.BuildId, app.IndexJobType, newIndexRaw)
	common["BTW_JOB_TYPE"] = app.IndexJobType
	common["BTW_INDEX_CONFIG_FILE"], common["BTW_INDEX_BACKEND"] = settingsPath, "exact"
	common["BTW_SESSION_INITIALIZE"] = "true"
	common["BTW_SESSION_TABLE_PREFIX"] = "cancel_new_index_"
	common["BTW_METRICS_ADDR"] = bgeProcessMetricsAddr(t)
	modelCallsBeforeNew := modelCalls.Load()
	newIndex := runBGEWorkerProcess(t, binary, common)
	waitBGEProcess(t, newIndex, 3*time.Minute, func() bool {
		job, err := platform.GetJob(ctx, newIndexJob.ID)
		if err != nil || job.State != "succeeded" || job.Result == nil || job.Result.Ref == nil {
			return false
		}
		remote, err := rtw.GetBuild(ctx, newBuild.BuildId)
		if err != nil || remote.State != "READY" {
			return false
		}
		var delivered bool
		err = contentPool.QueryRow(ctx, "SELECT delivered_at IS NOT NULL FROM content_outbox WHERE build_id=$1",
			newBuild.BuildId).Scan(&delivered)
		return err == nil && delivered
	})
	newIndex.stop(t)
	finalJob, err := platform.GetJob(ctx, newIndexJob.ID)
	ready, readyErr := rtw.GetBuild(ctx, newBuild.BuildId)
	newLocal, localErr := store.Get(ctx, newBuild.BuildId)
	if err != nil || readyErr != nil || localErr != nil || newLocal.Result == nil ||
		finalJob.Result == nil || finalJob.Result.Ref == nil || ready.Generation != 2 ||
		ready.ReleaseId != newRelease.ReleaseId || ready.LeaseEpoch != 2 ||
		finalJob.LeaseEpoch != 1 || finalJob.Result.Ref.Hash != newLocal.Result.SHA256 ||
		ready.IndexManifestHash != newLocal.Result.SHA256 || modelCalls.Load() <= modelCallsBeforeNew ||
		nativePrepare.Load() < 2 || nativeIndex.Load() < 1 {
		t.Fatalf("replacement Release did not complete real three-lane chain: dc=%+v rtw=%+v local=%+v calls=%d/%d spans=%d/%d errors=%v %v %v",
			finalJob, ready, newLocal, modelCallsBeforeNew, modelCalls.Load(), nativePrepare.Load(), nativeIndex.Load(),
			err, readyErr, localErr)
	}
	manifestRaw, err := objects.Get(ctx, *newLocal.Result)
	if err != nil {
		t.Fatal(err)
	}
	var manifest corpus.IndexManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil || manifest.Generation != 2 ||
		manifest.ReleaseID != newRelease.ReleaseId || len(manifest.Lanes) != 3 {
		t.Fatalf("new index manifest invalid: %+v err=%v", manifest, err)
	}
	for _, lane := range manifest.Lanes {
		if !lane.ProbePassed || lane.Artifact.SHA256 == "" {
			t.Fatalf("new lane unverified: %+v", lane)
		}
		if _, err := objects.Get(ctx, lane.Artifact); err != nil {
			t.Fatal(err)
		}
	}
	indexes := make(map[string]corpus.Ref, len(manifest.Lanes))
	for _, lane := range manifest.Lanes {
		indexes[lane.Profile.Lane] = lane.Artifact
	}
	if _, err := rtw.AcceptBuild(ctx, ridethewind.AcceptBuildReq{BuildId: oldBuild.BuildId,
		Generation: oldBuild.Generation, ManifestHash: oldBuild.ManifestHash,
		AttemptId: oldAttempt.AttemptID, LeaseEpoch: 2, CancelVersion: oldAttempt.CancelVersion,
		State: "READY", IndexManifestRef: newLocal.Result.Key, IndexManifestHash: newLocal.Result.SHA256}); err == nil {
		t.Fatal("cancelled old worker accepted replacement READY")
	}
	if _, err := platform.CompleteJob(ctx, oldIndexJob.ID, jobs.Complete{Lease: jobs.Lease{
		WorkerID: oldAttempt.WorkerID, AttemptID: oldAttempt.AttemptID,
		LeaseEpoch: oldAttempt.LeaseEpoch, CancelVersion: oldAttempt.CancelVersion},
		Result: jobs.Result{State: "succeeded", Ref: finalJob.Result.Ref}}); err == nil {
		t.Fatal("cancelled old DC attempt ACKed replacement manifest")
	}
	var publishedAfter cancelReleaseSnapshot
	cancelReleaseAdmin(t, ctx, fixture, "GET", "/v1/knowledge/modules/"+fixture.ModuleID+"/releases/current",
		nil, &publishedAfter)
	if publishedAfter != publishedBefore {
		t.Fatalf("new READY moved old published pointer: before=%+v after=%+v", publishedBefore, publishedAfter)
	}
	var cancelledCount, completedOld int
	if err := dcPool.QueryRow(ctx, "SELECT count(*),count(*) FILTER (WHERE result_hash IS NOT NULL) FROM jobs.attempt WHERE job_id=$1",
		oldIndexJob.ID).Scan(&cancelledCount, &completedOld); err != nil || cancelledCount != 1 || completedOld != 0 {
		t.Fatalf("old DC attempt got terminal result: count=%d completed=%d err=%v", cancelledCount, completedOld, err)
	}
	report, err := json.Marshal(map[string]any{
		"index_manifest": newLocal.Result, "indexes": indexes,
		"chunk_count": manifest.ChunkCount, "api_index_settings": json.RawMessage(settingsRaw),
		"new_source_revision_id": newSource.RevisionId,
		"new_source_content_id":  newSource.EntityId,
		"build_id":               newBuild.BuildId, "old_build_id": oldBuild.BuildId,
		"old_release_id": oldRelease.ReleaseId, "first_new_build_id": firstNew.BuildId,
		"new_release_id": newRelease.ReleaseId, "new_release_ordinal": newRelease.Ordinal,
		"old_release_manifest_hash": oldRelease.ManifestHash,
		"new_release_manifest_hash": newRelease.ManifestHash,
		"old_dc_job_id":             oldIndexJob.ID, "dc_job_id": newIndexJob.ID,
		"index_manifest_ref": newLocal.Result.Key, "index_manifest_hash": newLocal.Result.SHA256,
		"dc_ack_ref": finalJob.Result.Ref.URI, "dc_ack_hash": finalJob.Result.Ref.Hash,
		"dc_lease_epoch": finalJob.LeaseEpoch, "rtw_lease_epoch": ready.LeaseEpoch,
		"rtw_state": ready.State, "rtw_generation": ready.Generation,
		"old_dc_cancel_version": cancelReceipt.CancelVersion, "old_dc_state": oldAfter.State,
		"old_rtw_state": oldRTW.State, "old_rtw_lease_epoch": oldRTW.LeaseEpoch,
		"first_new_build_state":        firstNew.State,
		"old_stale_rtw_ready_rejected": true, "old_stale_dc_complete_rejected": true,
		"published_release_before":   publishedBefore.ActiveReleaseID,
		"published_release_after":    publishedAfter.ActiveReleaseID,
		"published_build_before":     publishedBefore.ActiveBuildID,
		"published_build_after":      publishedAfter.ActiveBuildID,
		"published_pointer_revision": publishedAfter.PointerRevision,
		"old_chunk_manifest_hash":    oldChunkRef.SHA256, "new_chunk_manifest_hash": newLocal.Chunks.SHA256,
		"old_model_calls": modelCallsBeforeNew, "new_model_calls": modelCalls.Load() - modelCallsBeforeNew,
		"native_prepare_spans": nativePrepare.Load(), "native_index_spans": nativeIndex.Load(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.ResultPath, report, 0600); err != nil {
		t.Fatal(err)
	}
	if evidence := os.Getenv("BTW_WORKER_EVIDENCE_DIR"); evidence != "" {
		if err := os.MkdirAll(evidence, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{
			"cancel-release-report.json": report,
			"cancel-old-index.jsonl":     []byte(oldIndex.output.String()),
			"cancel-new-index.jsonl":     []byte(newIndex.output.String()),
			"cancel-new-prepare.jsonl":   []byte(newPrepare.output.String()),
		} {
			if err := os.WriteFile(filepath.Join(evidence, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("cancelled candidate and built new Release ordinal=%d generation=%d ref=%s",
		newRelease.Ordinal, newBuild.Generation, newLocal.Result.SHA256)
}
