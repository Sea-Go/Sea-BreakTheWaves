package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
	tracecollector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// The provider endpoints below are protocol fixtures. The child process runs
// real local exact lane Build/VerifyAndProbe implementations and real PG/Runner.
func TestActualIndexWorkerBuildsThreeLanes(t *testing.T) {
	contentDSN, sessionDSN := os.Getenv("BTW_WORKER_TEST_CONTENT_DSN"), os.Getenv("BTW_WORKER_TEST_SESSION_DSN")
	if contentDSN == "" || sessionDSN == "" {
		t.Skip("use cmd/worker/acceptance.sh for disposable PostgreSQL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, contentDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := content.MigratePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	artifactDir := t.TempDir()
	objects, err := artifacts.NewLocal(artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	settings, profiles := fixedIndexSettings()
	settingsRaw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(t.TempDir(), "fixed-lanes.json")
	if err := os.WriteFile(settingsPath, settingsRaw, 0600); err != nil {
		t.Fatal(err)
	}
	releaseManifest := content.ReleaseManifest{SchemaVersion: 1, ModuleID: "index-module", ReleaseID: "index-release",
		SourceRevisionIDs: []string{"index-revision"}, WikiRevisionIDs: []string{}, ChunkingProfile: "index-paragraph-v1",
		RetrievalProfiles: profiles}
	releaseRaw, err := json.Marshal(releaseManifest)
	if err != nil {
		t.Fatal(err)
	}
	releaseRef, err := objects.Put(ctx, releaseRaw)
	if err != nil {
		t.Fatal(err)
	}
	original := "east\r\n\r\nnorth"
	originalRef, err := objects.Put(ctx, []byte(original))
	if err != nil {
		t.Fatal(err)
	}
	revision := ridethewind.Revision{RevisionId: "index-revision", ModuleId: "index-module", EntityId: "book", Kind: "source",
		Title: "Book", MediaType: "text/plain", Content: original, ContentHash: originalRef.SHA256, ObjectKey: originalRef.Key}
	chunker, err := content.NewChunker(content.ChunkConfig{ID: "index-paragraph-v1", Size: 64, Overlap: 0})
	if err != nil {
		t.Fatal(err)
	}
	chunkManifest, err := chunker.Build(ctx, content.ChunkInput{ModuleID: "index-module", ReleaseID: "index-release", InputManifestHash: releaseRef.SHA256,
		Revisions: []corpus.Revision{{RevisionID: revision.RevisionId, ModuleID: revision.ModuleId, EntityID: revision.EntityId,
			Kind: revision.Kind, Title: revision.Title, MediaType: revision.MediaType, Object: originalRef, Content: original}}})
	if err != nil || len(chunkManifest.Chunks) != 2 {
		t.Fatalf("prepare fixed chunk fixture: %v, chunks=%d", err, len(chunkManifest.Chunks))
	}
	chunkRaw, err := json.Marshal(chunkManifest)
	if err != nil {
		t.Fatal(err)
	}
	chunkRef, err := objects.Put(ctx, chunkRaw)
	if err != nil {
		t.Fatal(err)
	}
	profileRaw, _ := json.Marshal([]any{"index-paragraph-v1", 64, 0, "sea.paragraph.v1", "trpc.fixed.v1.8.1"})
	if _, err := pool.Exec(ctx, "INSERT INTO content_chunk_profiles(profile_id,definition_hash) VALUES($1,$2)", "index-paragraph-v1", artifacts.Hash(profileRaw)); err != nil {
		t.Fatal(err)
	}
	stable := content.BuildInput{BuildID: "index-build", ModuleID: "index-module", ReleaseID: "index-release", Generation: 1,
		InputHash: releaseRef.SHA256, OperationID: "index-prepare-operation", Revisions: []string{"index-revision"}}
	store := content.NewStore(pool)
	if _, err := store.Claim(ctx, stable, content.Fence{BuildID: stable.BuildID, AttemptID: "prepare-attempt", LeaseEpoch: 1,
		ExpiresAt: time.Now().UTC().Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	chunkRefRaw, _ := json.Marshal(chunkRef)
	if _, err := pool.Exec(ctx, "UPDATE content_builds SET chunks=$2 WHERE build_id=$1", stable.BuildID, chunkRefRaw); err != nil {
		t.Fatal(err)
	}
	leaseExpires := time.Now().UTC().Add(90 * time.Second).Truncate(time.Microsecond).Format(time.RFC3339Nano)
	input := struct {
		BuildID           string `json:"build_id"`
		ReleaseID         string `json:"release_id"`
		Generation        int64  `json:"generation"`
		InputManifestHash string `json:"input_manifest_hash"`
	}{stable.BuildID, stable.ReleaseID, stable.Generation, stable.InputHash}
	inputRaw, _ := json.Marshal(input)
	job := jobs.Job{ID: "index-job", InputHash: artifacts.Hash(inputRaw), State: "running", WorkerID: "local-worker-1",
		AttemptID: "index-attempt", LeaseEpoch: 1, LeaseExpiresAt: leaseExpires,
		Request: jobs.Submit{Producer: "ridethewind", OperationID: "index-operation", RunRef: "run-index", JobType: "content.build.v1",
			ResourceProfile: "cpu", Input: inputRaw}}
	var claims atomic.Int64
	var representationCalls atomic.Int64
	var rtwAccepted atomic.Bool
	var rtwAcceptCount atomic.Int64
	var dcMu sync.Mutex
	var dcResult *jobs.Result
	completed := make(chan jobs.Result, 1)
	dc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer worker-dc-test" {
			t.Errorf("DC lost worker token: %s", r.URL.Path)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/jobs/index-job":
			copy := job
			dcMu.Lock()
			if dcResult != nil {
				copy.State = "succeeded"
				result := *dcResult
				copy.Result = &result
			}
			dcMu.Unlock()
			_ = json.NewEncoder(w).Encode(copy)
		case "POST /v1/jobs/claim":
			var claim jobs.Claim
			_ = json.NewDecoder(r.Body).Decode(&claim)
			if claim.JobType != "content.build.v1" {
				t.Errorf("wrong claimed job type: %+v", claim)
			}
			if claims.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(job)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		case "POST /v1/jobs/index-job/complete":
			if !rtwAccepted.Load() {
				t.Error("DC technical ACK preceded RTW READY acceptance")
			}
			var receipt jobs.Complete
			if err := json.NewDecoder(r.Body).Decode(&receipt); err != nil {
				t.Errorf("decode DC completion: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			select {
			case completed <- receipt.Result:
			default:
				t.Error("duplicate index completion")
			}
			dcMu.Lock()
			result := receipt.Result
			dcResult = &result
			dcMu.Unlock()
			_ = json.NewEncoder(w).Encode(jobs.CompletionReceipt{JobID: job.ID, AttemptID: job.AttemptID,
				LeaseEpoch: job.LeaseEpoch, CancelVersion: job.CancelVersion, TechnicalState: receipt.Result.State,
				ResultHash: artifacts.Hash([]byte("index-ack"))})
		case "POST /v1/representations":
			representationCalls.Add(1)
			var q representation.Request
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				t.Errorf("decode typed representation: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.Header.Get("Idempotency-Key") == "" || r.Header.Get("Traceparent") == "" {
				t.Error("model request lost idempotency key or traceparent")
			}
			contract := settings.Dense.Contract
			switch q.ContractID {
			case settings.Sparse.Contract.ID:
				contract = settings.Sparse.Contract
			case settings.MultiVector.Contract.ID:
				contract = settings.MultiVector.Contract
			case settings.Dense.Contract.ID:
			default:
				t.Errorf("unknown representation contract: %s", q.ContractID)
			}
			response := representation.Response{Model: "fixture_model", ConfigurationID: q.ConfigurationID,
				OutputContract: q.OutputContract, ContractID: q.ContractID, Space: q.Space, Role: q.Role,
				TokenizerID: contract.TokenizerID, VocabularyID: contract.VocabularyID,
				Usage: &representation.Usage{PromptTokens: int64(len(q.Input)), TotalTokens: int64(len(q.Input))}}
			for _, item := range q.Input {
				first := strings.Contains(item.Text, "east")
				vector, token := []float64{0, 1}, 2
				if first {
					vector, token = []float64{1, 0}, 1
				}
				result := representation.Item{ID: item.ID}
				switch contract.Kind {
				case representation.Dense:
					result.Dense = &representation.DenseValues{Values: vector}
				case representation.Sparse:
					result.Sparse = &representation.SparseValues{Indices: []int{token}, Weights: []float64{1}}
				case representation.TokenMatrix:
					result.TokenMatrix = &representation.TokenValues{Shape: []int{1, 2}, Values: [][]float64{vector}, Mask: []bool{true}}
				}
				response.Data = append(response.Data, result)
			}
			_ = json.NewEncoder(w).Encode(response)
		default:
			t.Errorf("unexpected DC request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer dc.Close()
	build := ridethewind.Build{BuildId: stable.BuildID, ModuleId: stable.ModuleID, ReleaseId: stable.ReleaseID,
		Generation: stable.Generation, ManifestHash: stable.InputHash, State: "BUILDING",
		AttemptId: "prepare-attempt", LeaseEpoch: 1,
		LeaseExpiresAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)}
	release := ridethewind.Release{ReleaseId: stable.ReleaseID, ModuleId: stable.ModuleID, Ordinal: 1,
		SourceRevisionIds: releaseManifest.SourceRevisionIDs, WikiRevisionIds: releaseManifest.WikiRevisionIDs,
		ChunkingProfile: releaseManifest.ChunkingProfile, RetrievalProfiles: profiles, ManifestHash: releaseRef.SHA256, ManifestRef: releaseRef.Key}
	var buildMu sync.Mutex
	var rtwTraceID string
	rtw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-rtw" {
			t.Errorf("RTW lost worker token: %s", r.URL.Path)
		}
		if parent := strings.Split(r.Header.Get("Traceparent"), "-"); len(parent) == 4 {
			buildMu.Lock()
			rtwTraceID = parent[1]
			buildMu.Unlock()
		}
		var data any
		switch r.Method + " " + r.URL.Path {
		case "GET /internal/v1/knowledge/builds/index-build":
			buildMu.Lock()
			data = build
			buildMu.Unlock()
		case "POST /internal/v1/knowledge/builds/index-build/claim":
			var q ridethewind.ClaimBuildReq
			_ = json.NewDecoder(r.Body).Decode(&q)
			buildMu.Lock()
			if q.LeaseEpoch != 0 {
				t.Errorf("worker supplied DC job epoch as RTW build fence: %d", q.LeaseEpoch)
			}
			if build.AttemptId != q.AttemptId {
				build.LeaseEpoch++
			}
			build.AttemptId, build.CancelVersion, build.LeaseExpiresAt = q.AttemptId, q.CancelVersion, q.LeaseExpiresAt
			data = build
			buildMu.Unlock()
		case "POST /internal/v1/knowledge/builds/index-build/results":
			var q ridethewind.AcceptBuildReq
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				t.Errorf("decode RTW result: %v", err)
			}
			local, err := store.Get(r.Context(), stable.BuildID)
			if err != nil || local.State != "READY" || local.Result == nil ||
				q.State != "READY" || q.Generation != stable.Generation || q.ManifestHash != stable.InputHash ||
				q.AttemptId != job.AttemptID || q.LeaseEpoch != 2 ||
				q.IndexManifestRef != local.Result.Key || q.IndexManifestHash != local.Result.SHA256 {
				t.Errorf("RTW result lacks same fixed local READY: %+v local=%+v err=%v", q, local, err)
				w.WriteHeader(http.StatusConflict)
				return
			}
			buildMu.Lock()
			build.State, build.IndexManifestRef, build.IndexManifestHash = q.State, q.IndexManifestRef, q.IndexManifestHash
			data = build
			buildMu.Unlock()
			rtwAcceptCount.Add(1)
			rtwAccepted.Store(true)
		case "GET /internal/v1/knowledge/releases/index-release":
			data = release
		case "GET /internal/v1/knowledge/revisions/index-revision":
			data = revision
		default:
			t.Errorf("unexpected RTW request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": data})
	}))
	defer rtw.Close()
	var traceMu sync.Mutex
	var traceID string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		var export tracecollector.ExportTraceServiceRequest
		if err := proto.Unmarshal(body.Bytes(), &export); err != nil {
			t.Errorf("decode framework OTLP: %v", err)
		}
		for _, resource := range export.GetResourceSpans() {
			for _, scope := range resource.GetScopeSpans() {
				for _, span := range scope.GetSpans() {
					if span.GetName() == "invoke_agent content_index" && scope.GetScope().GetName() == "trpc.agent.go" {
						traceMu.Lock()
						traceID = hex.EncodeToString(span.GetTraceId())
						traceMu.Unlock()
					}
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	metricsAddr := listener.Addr().String()
	_ = listener.Close()
	binary := filepath.Join(t.TempDir(), "worker")
	if output, err := exec.Command("go", "build", "-mod=readonly", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build real worker: %v: %s", err, output)
	}
	version, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	env := validEnvironment()
	env["BTW_JOB_TYPE"], env["BTW_INDEX_CONFIG_FILE"], env["BTW_INDEX_BACKEND"] = "content.build.v1", settingsPath, "exact"
	env["BTW_DC_URL"], env["BTW_RTW_URL"], env["BTW_DC_TOKEN"] = dc.URL, rtw.URL, "worker-dc-test"
	env["BTW_CONTENT_POSTGRES_DSN"], env["BTW_SESSION_POSTGRES_DSN"] = contentDSN, sessionDSN
	env["BTW_CONTENT_MIGRATE"], env["BTW_SESSION_INITIALIZE"] = "false", "true"
	env["BTW_SESSION_TABLE_PREFIX"] = "content_index_"
	env["BTW_ARTIFACT_DIR"], env["BTW_METRICS_ADDR"] = artifactDir, metricsAddr
	env["BTW_OTLP_TRACES_URL"], env["BTW_SERVICE_VERSION"] = collector.URL+"/v1/traces", strings.TrimSpace(string(version))
	var output lockedOutput
	cmd := exec.Command(binary)
	cmd.Stdout, cmd.Stderr = &output, &output
	for _, inherited := range os.Environ() {
		key, _, _ := strings.Cut(inherited, "=")
		if _, override := env[key]; !override {
			cmd.Env = append(cmd.Env, inherited)
		}
	}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	var completion jobs.Result
	select {
	case completion = <-completed:
	case <-time.After(25 * time.Second):
		t.Fatalf("index worker did not finish: %s", output.String())
	}
	if completion.State != "succeeded" || completion.Ref == nil || completion.Ref.MediaType != "application/vnd.sea.index-manifest+json" {
		t.Fatalf("invalid index technical ACK: %+v; logs=%s", completion, output.String())
	}
	response, err := http.Get("http://" + metricsAddr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	var metrics bytes.Buffer
	_, _ = metrics.ReadFrom(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(metrics.String(), "trpc_agent_go_agent_") {
		t.Fatal("index process omitted native framework metrics")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("worker exit: %v logs=%s", err, output.String())
	}
	if representationCalls.Load() < 6 || claims.Load() == 0 {
		t.Fatalf("three lanes and probes were not invoked: DC calls=%d claims=%d", representationCalls.Load(), claims.Load())
	}
	var state string
	var resultRaw []byte
	if err := pool.QueryRow(ctx, "SELECT state,result FROM content_builds WHERE build_id=$1", stable.BuildID).Scan(&state, &resultRaw); err != nil {
		t.Fatal(err)
	}
	var resultRef corpus.Ref
	if err := json.Unmarshal(resultRaw, &resultRef); err != nil || state != "READY" || resultRef.SHA256 != completion.Ref.Hash {
		t.Fatalf("local READY and DC ACK diverged: state=%s ref=%+v err=%v", state, resultRef, err)
	}
	manifestRaw, err := objects.Get(ctx, resultRef)
	if err != nil {
		t.Fatal(err)
	}
	var indexed corpus.IndexManifest
	if err := json.Unmarshal(manifestRaw, &indexed); err != nil || indexed.BuildID != stable.BuildID ||
		indexed.Generation != stable.Generation || indexed.ChunkManifest != chunkRef || len(indexed.Lanes) != 3 {
		t.Fatalf("invalid three-lane manifest: %+v err=%v", indexed, err)
	}
	for _, lane := range indexed.Lanes {
		if !lane.ProbePassed || lane.Artifact.SHA256 == "" {
			t.Fatalf("index lane missing real probe/ref: %+v", lane)
		}
		raw, err := objects.Get(ctx, lane.Artifact)
		if err != nil {
			t.Fatalf("read %s lane artifact: %v", lane.Profile.Lane, err)
		}
		var index corpus.LaneIndex
		if err := json.Unmarshal(raw, &index); err != nil || index.BuildID != stable.BuildID ||
			index.Generation != stable.Generation || index.ChunkManifest != chunkRef || index.Profile != lane.Profile ||
			len(index.Shards) == 0 {
			t.Fatalf("%s lane did not persist a fixed numeric index: %+v err=%v", lane.Profile.Lane, index, err)
		}
	}
	buildMu.Lock()
	remoteState, remoteTrace, remoteRef, remoteHash := build.State, rtwTraceID, build.IndexManifestRef, build.IndexManifestHash
	buildMu.Unlock()
	if remoteState != "READY" || remoteRef != resultRef.Key || remoteHash != resultRef.SHA256 || rtwAcceptCount.Load() != 1 {
		t.Fatal("RTW READY does not match exact accepted local index")
	}
	var delivered bool
	if err := pool.QueryRow(ctx, "SELECT delivered_at IS NOT NULL FROM content_outbox WHERE build_id=$1", stable.BuildID).Scan(&delivered); err != nil || !delivered {
		t.Fatalf("DC and RTW acceptance did not close durable outbox: delivered=%t err=%v", delivered, err)
	}
	traceMu.Lock()
	frameworkTrace := traceID
	traceMu.Unlock()
	if frameworkTrace == "" || remoteTrace == "" || frameworkTrace != remoteTrace {
		t.Fatalf("native Graph trace %q and RTW client trace %q differ", frameworkTrace, remoteTrace)
	}
	logs := output.String()
	for _, event := range []string{`"event":"content.worker.index.finished"`, `"event":"content.index_build.finished"`, `"event":"content.worker.stopped"`} {
		if !strings.Contains(logs, event) {
			t.Fatalf("missing structured %s: %s", event, logs)
		}
	}
	if strings.Contains(logs, contentDSN) || strings.Contains(logs, env["BTW_DC_TOKEN"]) {
		t.Fatal("index worker leaked configured credential")
	}
	if evidence := os.Getenv("BTW_WORKER_EVIDENCE_DIR"); evidence != "" {
		if err := os.MkdirAll(evidence, 0700); err != nil {
			t.Fatal(err)
		}
		summary, _ := json.MarshalIndent(map[string]any{"job_type": "content.build.v1", "backend": "local-exact",
			"content_state": state, "rtw_state": remoteState, "dc_state": completion.State,
			"index_manifest_sha256": resultRef.SHA256, "lane_count": len(indexed.Lanes),
			"representation_calls": representationCalls.Load(), "framework_trace_id": frameworkTrace,
			"rtw_trace_id": remoteTrace}, "", "  ")
		if err := os.WriteFile(filepath.Join(evidence, "index-summary.json"), append(summary, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(evidence, "index-worker.jsonl"), []byte(logs), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("real index process accepted three local lanes; index=%s representations=%d", resultRef.SHA256, representationCalls.Load())
}
