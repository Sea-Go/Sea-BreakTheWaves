package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	contentmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/content"
	"github.com/jackc/pgx/v5/pgxpool"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

type realRTWIndexFixture struct {
	BaseURL           string   `json:"base_url"`
	WorkerToken       string   `json:"worker_token"`
	DCJobURL          string   `json:"dc_job_url"`
	DCJobToken        string   `json:"dc_job_token"`
	DCJobDSN          string   `json:"dc_job_dsn"`
	BGERuntimeFile    string   `json:"bge_runtime_file"`
	ObjectsDir        string   `json:"objects_dir"`
	BuildID           string   `json:"build_id"`
	ReleaseID         string   `json:"release_id"`
	ModuleID          string   `json:"module_id"`
	SourceRevisionIDs []string `json:"source_revision_ids"`
	WikiRevisionIDs   []string `json:"wiki_revision_ids"`
	ChunkProfile      string   `json:"chunk_profile"`
	ChunkSize         int      `json:"chunk_size"`
	ChunkOverlap      int      `json:"chunk_overlap"`
	ResultPath        string   `json:"result_path"`
}

func readRealRTWIndexFixture(t *testing.T) (realRTWIndexFixture, bool) {
	t.Helper()
	path := os.Getenv("SEA_RTW_REAL_INDEX_FIXTURE")
	if path == "" {
		return realRTWIndexFixture{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 16*1024 {
		t.Fatal("RTW index fixture exceeds 16 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var fixture realRTWIndexFixture
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatal("RTW index fixture has trailing data")
	}
	parsed, err := url.Parse(fixture.BaseURL)
	if err != nil || parsed.Scheme != "http" || net.ParseIP(parsed.Hostname()) == nil ||
		!net.ParseIP(parsed.Hostname()).IsLoopback() || fixture.WorkerToken == "" ||
		fixture.ObjectsDir == "" || fixture.ResultPath == "" || fixture.BuildID == "" || fixture.ReleaseID == "" ||
		fixture.ModuleID == "" || fixture.ChunkProfile == "" || fixture.ChunkSize <= 0 ||
		fixture.ChunkOverlap < 0 || fixture.ChunkOverlap >= fixture.ChunkSize ||
		len(fixture.SourceRevisionIDs) == 0 {
		t.Fatal("RTW index fixture requires loopback provider, fixed build/release, shared objects and chunk profile")
	}
	if fixture.DCJobURL != "" || fixture.DCJobToken != "" {
		platform, parseErr := url.Parse(fixture.DCJobURL)
		if parseErr != nil || platform.Scheme != "http" || net.ParseIP(platform.Hostname()) == nil ||
			!net.ParseIP(platform.Hostname()).IsLoopback() || fixture.DCJobToken == "" {
			t.Fatal("actual DC job platform requires a paired loopback URL and disposable token")
		}
	}
	if fixture.DCJobDSN != "" {
		pgConfig, parseErr := pgxpool.ParseConfig(fixture.DCJobDSN)
		if parseErr != nil || pgConfig.ConnConfig.Host != "127.0.0.1" || fixture.DCJobURL == "" {
			t.Fatal("actual DC job database must be task-owned loopback PostgreSQL")
		}
	}
	return fixture, true
}

type indexTraceSink struct{}

func (indexTraceSink) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (indexTraceSink) Shutdown(context.Context) error                             { return nil }

// TestRTWRealProviderIndexDispatch is called only by the RTW isolated HTTP/PG
// acceptance test. No RTW READY response is fabricated in this consumer.
func TestRTWRealProviderIndexDispatch(t *testing.T) {
	fixture, enabled := readRealRTWIndexFixture(t)
	if !enabled {
		t.Skip("set SEA_RTW_REAL_INDEX_FIXTURE from real RTW go-zero HTTP test")
	}
	dsn := os.Getenv("BTW_WORKER_TEST_CONTENT_DSN")
	if dsn == "" {
		t.Fatal("run through cmd/worker/acceptance.sh for disposable BTW PG16")
	}
	deadline := 90 * time.Second
	if fixture.BGERuntimeFile != "" {
		deadline = 20 * time.Minute
		if fixture.DCJobURL == "" {
			t.Fatal("live BGE index requires the actual DC technical job platform")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	rtw, err := ridethewind.New(httpclient.Config{BaseURL: fixture.BaseURL, Token: fixture.WorkerToken,
		HTTPClient: &http.Client{Timeout: 15 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := artifacts.NewLocal(fixture.ObjectsDir)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := rtw.GetBuild(ctx, fixture.BuildID)
	if err != nil || remote.State != "BUILDING" || remote.AttemptId != "" || remote.LeaseEpoch != 0 ||
		remote.ReleaseId != fixture.ReleaseID || remote.ModuleId != fixture.ModuleID || remote.IndexManifestRef != "" {
		t.Fatalf("fixture must contain one fresh unclaimed RTW BUILDING build: %+v err=%v", remote, err)
	}
	release, err := rtw.GetRelease(ctx, fixture.ReleaseID)
	settings, expectedProfiles := fixedIndexSettings()
	if fixture.BGERuntimeFile != "" {
		settings = readLiveBGERuntime(t, fixture.BGERuntimeFile).indexSettings()
		expectedProfiles = []ridethewind.RetrievalProfile{
			{Lane: "dense", Encoder: settings.Dense.Document.PhysicalModel, Tokenizer: settings.Dense.Contract.TokenizerID,
				Space: settings.Dense.Space, Dimensions: settings.Dense.Contract.Dimensions},
			{Lane: "sparse", Encoder: settings.Sparse.Document.PhysicalModel, Tokenizer: settings.Sparse.Contract.TokenizerID,
				Space: settings.Sparse.Space, Dimensions: settings.Sparse.Contract.Dimensions},
			{Lane: "multivector", Encoder: settings.MultiVector.Document.PhysicalModel,
				Tokenizer: settings.MultiVector.Contract.TokenizerID, Space: settings.MultiVector.Space,
				Dimensions: settings.MultiVector.Contract.Dimensions, Mask: "valid",
				Aggregation: settings.MultiVector.Contract.Aggregation},
		}
	}
	actualProfiles := append([]ridethewind.RetrievalProfile(nil), release.RetrievalProfiles...)
	wantedProfiles := append([]ridethewind.RetrievalProfile(nil), expectedProfiles...)
	slices.SortFunc(actualProfiles, func(a, b ridethewind.RetrievalProfile) int { return strings.Compare(a.Lane, b.Lane) })
	slices.SortFunc(wantedProfiles, func(a, b ridethewind.RetrievalProfile) int { return strings.Compare(a.Lane, b.Lane) })
	if err != nil || release.ReleaseId != fixture.ReleaseID || release.ModuleId != fixture.ModuleID ||
		release.ManifestHash != remote.ManifestHash || release.ChunkingProfile != fixture.ChunkProfile ||
		!reflect.DeepEqual(actualProfiles, wantedProfiles) ||
		!slices.Equal(release.SourceRevisionIds, fixture.SourceRevisionIDs) ||
		!slices.Equal(release.WikiRevisionIds, fixture.WikiRevisionIDs) {
		t.Fatalf("real RTW release differs from fixed BTW index contract: release=%+v err=%v", release, err)
	}
	if _, err := objects.Get(ctx, structRef(release.ManifestRef, release.ManifestHash)); err != nil {
		t.Fatalf("RTW frozen release object is not shared with BTW: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, contentmigration.SQL); err != nil {
		t.Fatal(err)
	}
	store := content.NewStore(pool)
	bundle, err := telemetry.New(ctx, telemetry.Config{Service: "sea-btw-index-real-rtw-test", Environment: "test",
		Version: strings.Repeat("a", 40), InstanceID: "real-rtw-consumer", Output: io.Discard,
		Level: slog.LevelInfo, TraceExporter: indexTraceSink{}, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := bundle.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := bundle.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	attemptID := fmt.Sprintf("btw-real-index-%d", time.Now().UnixNano())
	leaseDuration := 75 * time.Second
	if fixture.BGERuntimeFile != "" {
		leaseDuration = 15 * time.Minute
	}
	expires := time.Now().UTC().Add(leaseDuration).Truncate(time.Microsecond)
	// Preparation used an earlier technical job/fence. The index DC job starts
	// its own epoch at one; RTW must allocate build epoch two independently.
	prepareFence := content.Fence{BuildID: remote.BuildId, AttemptID: "prepare-" + attemptID, LeaseEpoch: 1,
		CancelVersion: remote.CancelVersion, ExpiresAt: expires}
	fence := content.Fence{BuildID: remote.BuildId, AttemptID: attemptID, LeaseEpoch: 1,
		CancelVersion: remote.CancelVersion, ExpiresAt: expires}
	claim := ridethewind.ClaimBuildReq{BuildId: remote.BuildId, Generation: remote.Generation,
		ManifestHash: remote.ManifestHash, AttemptId: prepareFence.AttemptID, LeaseEpoch: 0,
		CancelVersion: prepareFence.CancelVersion, LeaseExpiresAt: expires.Format(time.RFC3339Nano)}
	expired := claim
	expired.AttemptId, expired.LeaseExpiresAt = "expired-claim", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := rtw.ClaimBuild(ctx, expired); err == nil {
		t.Fatal("real RTW accepted expired claim")
	}
	if after, err := rtw.GetBuild(ctx, remote.BuildId); err != nil || after.State != "BUILDING" || after.AttemptId != "" {
		t.Fatalf("expired claim mutated RTW build: %+v err=%v", after, err)
	}
	claimed, err := rtw.ClaimBuild(ctx, claim)
	if err != nil || claimed.AttemptId != prepareFence.AttemptID || claimed.LeaseEpoch != prepareFence.LeaseEpoch {
		t.Fatalf("claim real RTW build: %+v err=%v", claimed, err)
	}
	old := ridethewind.AcceptBuildReq{BuildId: remote.BuildId, Generation: remote.Generation,
		ManifestHash: remote.ManifestHash, AttemptId: "old-attempt", LeaseEpoch: 0,
		CancelVersion: remote.CancelVersion, State: "FAILED", ErrorCode: "stale_fence"}
	if _, err := rtw.AcceptBuild(ctx, old); err == nil {
		t.Fatal("real RTW accepted old fence")
	}
	if after, err := rtw.GetBuild(ctx, remote.BuildId); err != nil || after.State != "BUILDING" || after.AttemptId != prepareFence.AttemptID {
		t.Fatalf("old result changed claimed RTW build: %+v err=%v", after, err)
	}
	chunker, err := content.NewChunker(content.ChunkConfig{ID: fixture.ChunkProfile,
		Size: fixture.ChunkSize, Overlap: fixture.ChunkOverlap})
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := content.NewPreparer(rtw, objects, store, chunker, bundle)
	if err != nil {
		t.Fatal(err)
	}
	stable := content.BuildInput{BuildID: remote.BuildId, ModuleID: remote.ModuleId, ReleaseID: remote.ReleaseId,
		Generation: remote.Generation, InputHash: remote.ManifestHash, OperationID: "real-index-preparation-" + attemptID,
		Revisions: append(append([]string(nil), release.SourceRevisionIds...), release.WikiRevisionIds...)}
	prepared, err := preparer.Prepare(ctx, stable, prepareFence)
	if err != nil || len(prepared.Manifest.Chunks) == 0 || prepared.Build.Chunks == nil {
		t.Fatalf("prepare real RTW revisions into BTW chunk ledger: chunks=%d err=%v", len(prepared.Manifest.Chunks), err)
	}
	if len(prepared.Manifest.Chunks) > 2 {
		t.Fatalf("real RTW fixture requires at most two chunks for bounded exact-probe acceptance, got %d", len(prepared.Manifest.Chunks))
	}
	worker, state := newRealRTWIndexWorker(t, ctx, fixture, settings, rtw, objects, store, bundle, fence)
	worked, err := worker.RunOnce(ctx)
	if err != nil || !worked {
		t.Fatalf("real RTW index dispatch: worked=%t err=%v", worked, err)
	}
	if state.RepresentationCalls() < 6 || !state.Completed() {
		t.Fatal("three real BTW lanes or DC technical ACK missing")
	}
	technicalJob, err := state.CurrentJob(ctx)
	if err != nil || technicalJob.State != "succeeded" || technicalJob.AttemptID == "" ||
		technicalJob.LeaseEpoch != 1 || technicalJob.Result == nil || technicalJob.Result.State != "succeeded" {
		t.Fatalf("actual/fixed DC job did not finish its own epoch one: %+v %v", technicalJob, err)
	}
	local, err := store.Get(ctx, remote.BuildId)
	if err != nil || local.State != "READY" || local.Result == nil || len(local.Lanes) != 3 {
		t.Fatalf("local index not fixed READY: %+v err=%v", local, err)
	}
	accepted, err := rtw.GetBuild(ctx, remote.BuildId)
	if err != nil || accepted.State != "READY" || accepted.IndexManifestRef != local.Result.Key ||
		accepted.IndexManifestHash != local.Result.SHA256 || accepted.AttemptId != technicalJob.AttemptID ||
		accepted.LeaseEpoch != 2 {
		t.Fatalf("real RTW READY differs from BTW/ DC accepted result: %+v err=%v", accepted, err)
	}
	if !state.ResultMatches(*local.Result) {
		t.Fatal("DC technical ACK did not carry exact RTW accepted Ref")
	}
	var delivered bool
	if err := pool.QueryRow(ctx, "SELECT delivered_at IS NOT NULL FROM content_outbox WHERE build_id=$1", remote.BuildId).Scan(&delivered); err != nil || !delivered {
		t.Fatalf("RTW and DC receipts did not deliver PG outbox: delivered=%t err=%v", delivered, err)
	}
	replay := ridethewind.AcceptBuildReq{BuildId: remote.BuildId, Generation: remote.Generation,
		ManifestHash: remote.ManifestHash, AttemptId: technicalJob.AttemptID, LeaseEpoch: accepted.LeaseEpoch,
		CancelVersion: fence.CancelVersion, State: "READY", IndexManifestRef: local.Result.Key,
		IndexManifestHash: local.Result.SHA256}
	if repeated, err := rtw.AcceptBuild(ctx, replay); err != nil || repeated.State != "READY" ||
		repeated.IndexManifestHash != local.Result.SHA256 {
		t.Fatalf("real RTW refused same-fence same-Ref replay: %+v err=%v", repeated, err)
	}
	dcRef, ok := state.ResultRef()
	if !ok {
		t.Fatal("DC result missing after accepted RTW replay")
	}
	evidence, err := json.Marshal(struct {
		BuildID           string `json:"build_id"`
		IndexManifestRef  string `json:"index_manifest_ref"`
		IndexManifestHash string `json:"index_manifest_hash"`
		DCAckRef          string `json:"dc_ack_ref"`
		DCAckHash         string `json:"dc_ack_hash"`
		DCJobID           string `json:"dc_job_id"`
		DCLeaseEpoch      int64  `json:"dc_lease_epoch"`
		RTWLeaseEpoch     int64  `json:"rtw_lease_epoch"`
		RTWState          string `json:"rtw_state"`
		RTWGeneration     int64  `json:"rtw_generation"`
	}{remote.BuildId, local.Result.Key, local.Result.SHA256, dcRef.URI,
		dcRef.Hash, technicalJob.ID, technicalJob.LeaseEpoch, accepted.LeaseEpoch,
		accepted.State, accepted.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.ResultPath, evidence, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("real RTW build accepted BTW exact index and DC ACK: build=%s generation=%d ref=%s", remote.BuildId, remote.Generation, local.Result.SHA256)
}

func structRef(key, hash string) corpus.Ref { return corpus.Ref{Key: key, SHA256: hash} }

type realRTWDCState struct {
	mu                  sync.Mutex
	job                 jobs.Job
	platform            *datacenter.Client
	platformJobID       string
	claimed             bool
	result              *jobs.Result
	representationCalls int
}

func (s *realRTWDCState) RepresentationCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.representationCalls
}
func (s *realRTWDCState) Completed() bool {
	if s.platform != nil {
		job, err := s.platform.GetJob(context.Background(), s.platformJobID)
		return err == nil && job.State == "succeeded" && job.Result != nil && job.Result.State == "succeeded"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result != nil && s.result.State == "succeeded"
}
func (s *realRTWDCState) ResultMatches(ref corpus.Ref) bool {
	if s.platform != nil {
		job, err := s.platform.GetJob(context.Background(), s.platformJobID)
		return err == nil && job.Result != nil && job.Result.Ref != nil &&
			*job.Result.Ref == (jobs.ResultRef{URI: "sha256:" + ref.SHA256, Hash: ref.SHA256,
				MediaType: "application/vnd.sea.index-manifest+json"})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result != nil && s.result.Ref != nil &&
		*s.result.Ref == (jobs.ResultRef{URI: "sha256:" + ref.SHA256, Hash: ref.SHA256,
			MediaType: "application/vnd.sea.index-manifest+json"})
}
func (s *realRTWDCState) ResultRef() (jobs.ResultRef, bool) {
	if s.platform != nil {
		job, err := s.platform.GetJob(context.Background(), s.platformJobID)
		if err != nil || job.Result == nil || job.Result.Ref == nil {
			return jobs.ResultRef{}, false
		}
		return *job.Result.Ref, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result == nil || s.result.Ref == nil {
		return jobs.ResultRef{}, false
	}
	return *s.result.Ref, true
}

func (s *realRTWDCState) CurrentJob(ctx context.Context) (jobs.Job, error) {
	if s.platform != nil {
		return s.platform.GetJob(ctx, s.platformJobID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.job
	if s.result != nil {
		job.State = "succeeded"
		copy := *s.result
		job.Result = &copy
	}
	return job, nil
}

func newRealRTWIndexWorker(t *testing.T, ctx context.Context, fixture realRTWIndexFixture,
	settings indexSettings, rtw *ridethewind.Client, objects artifacts.Store, store *content.Store,
	bundle *telemetry.Bundle, fence content.Fence) (*app.IndexWorker, *realRTWDCState) {
	t.Helper()
	var bge liveBGERuntime
	if fixture.BGERuntimeFile != "" {
		bge = readLiveBGERuntime(t, fixture.BGERuntimeFile)
	}
	input := app.IndexJobInput{BuildID: fixture.BuildID, ReleaseID: fixture.ReleaseID,
		Generation: 1, InputManifestHash: ""}
	build, err := rtw.GetBuild(ctx, fixture.BuildID)
	if err != nil {
		t.Fatal(err)
	}
	input.Generation, input.InputManifestHash = build.Generation, build.ManifestHash
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	state := &realRTWDCState{job: jobs.Job{ID: "real-index-job-" + fence.AttemptID,
		InputHash: artifacts.Hash(raw), State: "running", WorkerID: "real-index-worker",
		AttemptID: fence.AttemptID, LeaseEpoch: fence.LeaseEpoch, CancelVersion: fence.CancelVersion,
		LeaseExpiresAt: fence.ExpiresAt.Format(time.RFC3339Nano),
		Request: jobs.Submit{Producer: "ridethewind", OperationID: "real-index-operation-" + fence.AttemptID,
			RunRef: "real-index-run-" + fence.AttemptID, JobType: app.IndexJobType,
			ResourceProfile: "cpu", Input: raw, MaxAttempts: 2}}}
	if fixture.DCJobURL != "" {
		platform, err := datacenter.New(httpclient.Config{BaseURL: fixture.DCJobURL,
			Token: fixture.DCJobToken, HTTPClient: &http.Client{Timeout: 10 * time.Second}})
		if err != nil {
			t.Fatal(err)
		}
		submission := state.job.Request
		submission.Deadline = time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339Nano)
		if fixture.BGERuntimeFile != "" {
			submission.Deadline = time.Now().UTC().Add(18 * time.Minute).Format(time.RFC3339Nano)
		}
		receipt, err := platform.SubmitJob(ctx, submission)
		if err != nil || receipt.ID == "" {
			t.Fatalf("submit actual DC index job: %+v %v", receipt, err)
		}
		state.platform, state.platformJobID = platform, receipt.ID
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-dc-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if state.platform != nil && strings.HasPrefix(r.URL.Path, "/v1/jobs") {
			// Only the representation endpoint remains a fixed numerical fixture.
			// Job HTTP bytes go to the real DC cmd/platform and PostgreSQL ledger.
			forwarded, err := http.NewRequestWithContext(r.Context(), r.Method,
				fixture.DCJobURL+r.URL.RequestURI(), io.LimitReader(r.Body, 1<<20))
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			forwarded.Header = r.Header.Clone()
			forwarded.Header.Set("Authorization", "Bearer "+fixture.DCJobToken)
			response, err := (&http.Client{Timeout: 10 * time.Second}).Do(forwarded)
			if err != nil {
				t.Errorf("actual DC job proxy failed: %v", err)
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			defer response.Body.Close()
			w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
			w.WriteHeader(response.StatusCode)
			if _, err := io.Copy(w, io.LimitReader(response.Body, 1<<20)); err != nil {
				t.Errorf("copy actual DC job reply: %v", err)
			}
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/jobs/claim":
			var request jobs.Claim
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil ||
				request.JobType != app.IndexJobType || request.WorkerID != state.job.WorkerID {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			state.mu.Lock()
			if state.claimed {
				state.mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
				return
			}
			state.claimed = true
			job := state.job
			state.mu.Unlock()
			_ = json.NewEncoder(w).Encode(job)
		case "GET /v1/jobs/" + state.job.ID:
			state.mu.Lock()
			job := state.job
			if state.result != nil {
				job.State = "succeeded"
				copy := *state.result
				job.Result = &copy
			}
			state.mu.Unlock()
			_ = json.NewEncoder(w).Encode(job)
		case "POST /v1/jobs/" + state.job.ID + "/complete":
			var request jobs.Complete
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Result.State != "succeeded" ||
				request.Result.Ref == nil || request.AttemptID != fence.AttemptID ||
				request.LeaseEpoch != fence.LeaseEpoch || request.CancelVersion != fence.CancelVersion {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			accepted, err := rtw.GetBuild(r.Context(), fixture.BuildID)
			if err != nil || accepted.State != "READY" || accepted.IndexManifestHash != request.Result.Ref.Hash ||
				accepted.IndexManifestRef != "sha256/"+request.Result.Ref.Hash {
				w.WriteHeader(http.StatusConflict)
				return
			}
			state.mu.Lock()
			copy := request.Result
			state.result = &copy
			state.mu.Unlock()
			_ = json.NewEncoder(w).Encode(jobs.CompletionReceipt{JobID: state.job.ID,
				AttemptID: fence.AttemptID, LeaseEpoch: fence.LeaseEpoch,
				CancelVersion: fence.CancelVersion, TechnicalState: "succeeded",
				ResultHash: artifacts.Hash([]byte("real-rtw-index-dc-ack"))})
		case "POST /v1/representations":
			if fixture.BGERuntimeFile != "" {
				forwarded, err := http.NewRequestWithContext(r.Context(), r.Method,
					bge.Endpoint+r.URL.RequestURI(), io.LimitReader(r.Body, 8<<20))
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				forwarded.Header = r.Header.Clone()
				forwarded.Header.Set("Authorization", "Bearer "+bge.AccessToken)
				response, err := (&http.Client{Timeout: 3 * time.Minute}).Do(forwarded)
				if err != nil {
					t.Errorf("actual DC BGE gateway call failed: %v", err)
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				defer response.Body.Close()
				w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
				w.WriteHeader(response.StatusCode)
				if _, err := io.Copy(w, io.LimitReader(response.Body, 8<<20)); err != nil {
					t.Errorf("copy actual DC BGE gateway response: %v", err)
				}
				state.mu.Lock()
				state.representationCalls++
				state.mu.Unlock()
				return
			}
			var request representation.Request
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || r.Header.Get("Idempotency-Key") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			contract := settings.Dense.Contract
			switch request.ContractID {
			case settings.Dense.Contract.ID:
			case settings.Sparse.Contract.ID:
				contract = settings.Sparse.Contract
			case settings.MultiVector.Contract.ID:
				contract = settings.MultiVector.Contract
			default:
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			response := representation.Response{Model: "fixture_model", ConfigurationID: request.ConfigurationID,
				OutputContract: request.OutputContract, ContractID: request.ContractID,
				Space: request.Space, Role: request.Role, TokenizerID: contract.TokenizerID,
				VocabularyID: contract.VocabularyID,
				Usage:        &representation.Usage{PromptTokens: int64(len(request.Input)), TotalTokens: int64(len(request.Input))}}
			for _, item := range request.Input {
				entry := representation.Item{ID: item.ID}
				switch contract.Kind {
				case representation.Dense:
					entry.Dense = &representation.DenseValues{Values: []float64{1, 0}}
				case representation.Sparse:
					entry.Sparse = &representation.SparseValues{Indices: []int{1}, Weights: []float64{1}}
				case representation.TokenMatrix:
					entry.TokenMatrix = &representation.TokenValues{
						Shape: []int{1, 2}, Values: [][]float64{{1, 0}}, Mask: []bool{true}}
				}
				response.Data = append(response.Data, entry)
			}
			state.mu.Lock()
			state.representationCalls++
			state.mu.Unlock()
			_ = json.NewEncoder(w).Encode(response)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	requestTimeout := 10 * time.Second
	if fixture.BGERuntimeFile != "" {
		requestTimeout = 4 * time.Minute
	}
	dc, err := datacenter.New(httpclient.Config{BaseURL: server.URL, Token: "fixture-dc-token",
		HTTPClient: &http.Client{Timeout: requestTimeout}})
	if err != nil {
		t.Fatal(err)
	}
	ag, err := newIndexGraph(settings, dc, rtw, objects, store, bundle)
	if err != nil {
		t.Fatal(err)
	}
	sessions := inmemory.NewSessionService()
	t.Cleanup(func() { _ = sessions.Close() })
	runner, err := runtime.New("real-rtw-index-consumer", ag, sessions, bundle)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Error(err)
		}
	})
	leaseSeconds := 45
	if fixture.BGERuntimeFile != "" {
		leaseSeconds = 900
	}
	worker, err := app.NewIndexWorker(app.IndexWorkerConfig{WorkerID: state.job.WorkerID,
		ResourceProfile: "cpu", LeaseSeconds: leaseSeconds}, dc, rtw, runner, store, objects, bundle)
	if err != nil {
		t.Fatal(err)
	}
	return worker, state
}
