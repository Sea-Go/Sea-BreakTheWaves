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
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	if err != nil || release.ReleaseId != fixture.ReleaseID || release.ModuleId != fixture.ModuleID ||
		release.ManifestHash != remote.ManifestHash || release.ChunkingProfile != fixture.ChunkProfile ||
		!reflect.DeepEqual(release.RetrievalProfiles, expectedProfiles) ||
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
	expires := time.Now().UTC().Add(75 * time.Second).Truncate(time.Microsecond)
	fence := content.Fence{BuildID: remote.BuildId, AttemptID: attemptID, LeaseEpoch: 1,
		CancelVersion: remote.CancelVersion, ExpiresAt: expires}
	claim := ridethewind.ClaimBuildReq{BuildId: remote.BuildId, Generation: remote.Generation,
		ManifestHash: remote.ManifestHash, AttemptId: attemptID, LeaseEpoch: fence.LeaseEpoch,
		CancelVersion: fence.CancelVersion, LeaseExpiresAt: expires.Format(time.RFC3339Nano)}
	expired := claim
	expired.AttemptId, expired.LeaseExpiresAt = "expired-claim", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := rtw.ClaimBuild(ctx, expired); err == nil {
		t.Fatal("real RTW accepted expired claim")
	}
	if after, err := rtw.GetBuild(ctx, remote.BuildId); err != nil || after.State != "BUILDING" || after.AttemptId != "" {
		t.Fatalf("expired claim mutated RTW build: %+v err=%v", after, err)
	}
	claimed, err := rtw.ClaimBuild(ctx, claim)
	if err != nil || claimed.AttemptId != attemptID || claimed.LeaseEpoch != fence.LeaseEpoch {
		t.Fatalf("claim real RTW build: %+v err=%v", claimed, err)
	}
	old := ridethewind.AcceptBuildReq{BuildId: remote.BuildId, Generation: remote.Generation,
		ManifestHash: remote.ManifestHash, AttemptId: "old-attempt", LeaseEpoch: 0,
		CancelVersion: remote.CancelVersion, State: "FAILED", ErrorCode: "stale_fence"}
	if _, err := rtw.AcceptBuild(ctx, old); err == nil {
		t.Fatal("real RTW accepted old fence")
	}
	if after, err := rtw.GetBuild(ctx, remote.BuildId); err != nil || after.State != "BUILDING" || after.AttemptId != attemptID {
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
	prepared, err := preparer.Prepare(ctx, stable, fence)
	if err != nil || len(prepared.Manifest.Chunks) == 0 || prepared.Build.Chunks == nil {
		t.Fatalf("prepare real RTW revisions into BTW chunk ledger: chunks=%d err=%v", len(prepared.Manifest.Chunks), err)
	}
	if len(prepared.Manifest.Chunks) > 2 {
		t.Fatalf("real RTW fixture requires at most two chunks for the fixed 2D exact-probe contract, got %d", len(prepared.Manifest.Chunks))
	}
	worker, state := newRealRTWIndexWorker(t, ctx, fixture, settings, rtw, objects, store, bundle, fence)
	worked, err := worker.RunOnce(ctx)
	if err != nil || !worked {
		t.Fatalf("real RTW index dispatch: worked=%t err=%v", worked, err)
	}
	if state.RepresentationCalls() < 6 || !state.Completed() {
		t.Fatal("three real BTW lanes or DC technical ACK missing")
	}
	local, err := store.Get(ctx, remote.BuildId)
	if err != nil || local.State != "READY" || local.Result == nil || len(local.Lanes) != 3 {
		t.Fatalf("local index not fixed READY: %+v err=%v", local, err)
	}
	accepted, err := rtw.GetBuild(ctx, remote.BuildId)
	if err != nil || accepted.State != "READY" || accepted.IndexManifestRef != local.Result.Key ||
		accepted.IndexManifestHash != local.Result.SHA256 || accepted.AttemptId != attemptID ||
		accepted.LeaseEpoch != fence.LeaseEpoch {
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
		ManifestHash: remote.ManifestHash, AttemptId: attemptID, LeaseEpoch: fence.LeaseEpoch,
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
		RTWState          string `json:"rtw_state"`
		RTWGeneration     int64  `json:"rtw_generation"`
	}{remote.BuildId, local.Result.Key, local.Result.SHA256, dcRef.URI,
		dcRef.Hash, accepted.State, accepted.Generation})
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result != nil && s.result.State == "succeeded"
}
func (s *realRTWDCState) ResultMatches(ref corpus.Ref) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result != nil && s.result.Ref != nil &&
		*s.result.Ref == (jobs.ResultRef{URI: "sha256:" + ref.SHA256, Hash: ref.SHA256,
			MediaType: "application/vnd.sea.index-manifest+json"})
}
func (s *realRTWDCState) ResultRef() (jobs.ResultRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result == nil || s.result.Ref == nil {
		return jobs.ResultRef{}, false
	}
	return *s.result.Ref, true
}

func newRealRTWIndexWorker(t *testing.T, ctx context.Context, fixture realRTWIndexFixture,
	settings indexSettings, rtw *ridethewind.Client, objects artifacts.Store, store *content.Store,
	bundle *telemetry.Bundle, fence content.Fence) (*app.IndexWorker, *realRTWDCState) {
	t.Helper()
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-dc-token" {
			w.WriteHeader(http.StatusUnauthorized)
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
	dc, err := datacenter.New(httpclient.Config{BaseURL: server.URL, Token: "fixture-dc-token",
		HTTPClient: &http.Client{Timeout: 10 * time.Second}})
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
	worker, err := app.NewIndexWorker(app.IndexWorkerConfig{WorkerID: state.job.WorkerID,
		ResourceProfile: "cpu", LeaseSeconds: 45}, dc, rtw, runner, store, objects, bundle)
	if err != nil {
		t.Fatal(err)
	}
	return worker, state
}
