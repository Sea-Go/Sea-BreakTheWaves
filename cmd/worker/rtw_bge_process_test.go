package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	"github.com/jackc/pgx/v5/pgxpool"
	tracecollector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

type bgeWorkerProcess struct {
	command *exec.Cmd
	output  *lockedOutput
	stopped bool
}

func (p *bgeWorkerProcess) stop(t *testing.T) {
	t.Helper()
	if p.stopped {
		return
	}
	p.stopped = true
	if err := p.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker process did not stop cleanly: %v\n%s", err, p.output.String())
		}
	case <-time.After(20 * time.Second):
		_ = p.command.Process.Kill()
		<-done
		t.Fatal("worker process did not stop after SIGTERM")
	}
}

func runBGEWorkerProcess(t *testing.T, binary string, env map[string]string) *bgeWorkerProcess {
	t.Helper()
	output := &lockedOutput{}
	cmd := exec.Command(binary)
	cmd.Stdout, cmd.Stderr = output, output
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
	p := &bgeWorkerProcess{command: cmd, output: output}
	t.Cleanup(func() {
		if !p.stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return p
}

func bgeProcessMetricsAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func waitBGEProcess(t *testing.T, process *bgeWorkerProcess, limit time.Duration, check func() bool) {
	t.Helper()
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if check() {
			return
		}
	}
	t.Fatalf("worker did not reach expected state; log=%s", process.output.String())
}

// Opt-in parent: real RTW go-zero/PG Release and Build, actual DC cmd/platform
// jobs, locked DC Gateway -> official BGE-M3, and two cmd/worker processes.
// The only proxy bridges two disposable DC HTTP test surfaces and credentials.
func TestRTWRealBGEWorkerProcesses(t *testing.T) {
	fixture, enabled := readRealRTWIndexFixture(t)
	if !enabled {
		t.Skip("set the RTW-owned BGE process fixture")
	}
	if fixture.BGERuntimeFile == "" || fixture.DCJobURL == "" ||
		os.Getenv("BTW_WORKER_TEST_CONTENT_DSN") == "" || os.Getenv("BTW_WORKER_TEST_SESSION_DSN") == "" {
		t.Fatal("real BGE process acceptance requires locked provider, DC jobs and disposable BTW PG")
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
	build, err := rtw.GetBuild(ctx, fixture.BuildID)
	if err != nil || build.State != "BUILDING" || build.AttemptId != "" || build.LeaseEpoch != 0 ||
		build.ModuleId != fixture.ModuleID || build.ReleaseId != fixture.ReleaseID {
		t.Fatalf("RTW did not supply an unclaimed fixed Build: %+v err=%v", build, err)
	}
	objects, err := artifacts.NewLocal(fixture.ObjectsDir)
	if err != nil {
		t.Fatal(err)
	}
	release, err := rtw.GetRelease(ctx, fixture.ReleaseID)
	if err != nil || release.ManifestHash != build.ManifestHash || release.ChunkingProfile != fixture.ChunkProfile ||
		len(release.RetrievalProfiles) != 3 {
		t.Fatalf("RTW fixed Release differs from Build: %+v err=%v", release, err)
	}
	if _, err := objects.Get(ctx, corpus.Ref{Key: release.ManifestRef, SHA256: release.ManifestHash}); err != nil {
		t.Fatalf("RTW original Release object unavailable: %v", err)
	}
	platform, err := datacenter.New(httpclient.Config{BaseURL: fixture.DCJobURL, Token: fixture.DCJobToken,
		HTTPClient: &http.Client{Timeout: 30 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	var representations, blockedCompletions atomic.Int64
	var blockCompletion atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-only-bge-worker-proxy" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/complete") && blockCompletion.Load() {
			blockedCompletions.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		endpoint, token, timeout := fixture.DCJobURL, fixture.DCJobToken, 30*time.Second
		if r.URL.Path == "/v1/representations" {
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
		if r.URL.Path == "/v1/representations" && response.StatusCode == http.StatusOK {
			representations.Add(1)
		}
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(response.Body, 8<<20))
	}))
	defer proxy.Close()
	var spans, nativePrepare, nativeIndex atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" && r.Method == http.MethodPost {
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
			spans.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer collector.Close()
	version, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "worker-race")
	compile := exec.Command("go", "build", "-race", "-mod=readonly", "-o", binary, "./cmd/worker")
	compile.Dir = "../.."
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile actual race worker: %v\n%s", err, output)
	}
	contentDSN, sessionDSN := os.Getenv("BTW_WORKER_TEST_CONTENT_DSN"), os.Getenv("BTW_WORKER_TEST_SESSION_DSN")
	common := validEnvironment()
	common["BTW_DC_URL"], common["BTW_DC_TOKEN"] = proxy.URL, "test-only-bge-worker-proxy"
	common["BTW_RTW_URL"], common["BTW_RTW_TOKEN"] = fixture.BaseURL, fixture.WorkerToken
	common["BTW_CONTENT_POSTGRES_DSN"], common["BTW_SESSION_POSTGRES_DSN"] = contentDSN, sessionDSN
	common["BTW_ARTIFACT_DIR"] = fixture.ObjectsDir
	common["BTW_SERVICE_VERSION"], common["BTW_ENVIRONMENT"] = strings.TrimSpace(string(version)), "test"
	common["BTW_OTLP_TRACES_URL"] = collector.URL + "/v1/traces"
	common["BTW_HTTP_TIMEOUT"], common["BTW_LEASE_SECONDS"] = "60s", "900"
	common["BTW_POLL_INTERVAL"] = "100ms"
	common["BTW_CHUNK_PROFILE_ID"] = fixture.ChunkProfile
	common["BTW_CHUNK_SIZE"], common["BTW_CHUNK_OVERLAP"] = fmt.Sprint(fixture.ChunkSize), fmt.Sprint(fixture.ChunkOverlap)
	common["BTW_CONTENT_MIGRATE"], common["BTW_SESSION_INITIALIZE"] = "true", "true"
	common["BTW_SESSION_TABLE_PREFIX"] = "real_bge_prepare_"
	common["BTW_METRICS_ADDR"] = bgeProcessMetricsAddr(t)
	prepareInput := content.BuildInput{BuildID: build.BuildId, ModuleID: build.ModuleId, ReleaseID: build.ReleaseId,
		Generation: build.Generation, InputHash: build.ManifestHash, OperationID: "bge-process-prepare-" + build.BuildId,
		Revisions: append(append([]string(nil), release.SourceRevisionIds...), release.WikiRevisionIds...)}
	prepareRaw, err := json.Marshal(prepareInput)
	if err != nil {
		t.Fatal(err)
	}
	prepareJob, err := platform.SubmitJob(ctx, jobs.Submit{Producer: "ridethewind", OperationID: prepareInput.OperationID,
		RunRef: "bge-process-prepare", JobType: app.PrepareJobType, ResourceProfile: "cpu", Input: prepareRaw,
		MaxAttempts: 2, Deadline: time.Now().UTC().Add(17 * time.Minute).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatalf("submit actual DC prepare job: %v", err)
	}
	prepare := runBGEWorkerProcess(t, binary, common)
	waitBGEProcess(t, prepare, 2*time.Minute, func() bool {
		job, err := platform.GetJob(ctx, prepareJob.ID)
		return err == nil && job.State == "succeeded" && job.Result != nil && job.Result.Ref != nil
	})
	prepare.stop(t)
	prepared, err := rtw.GetBuild(ctx, build.BuildId)
	if err != nil || prepared.State != "BUILDING" || prepared.LeaseEpoch != 1 {
		t.Fatalf("prepare process changed RTW Build incorrectly: %+v err=%v", prepared, err)
	}
	pool, err := pgxpool.New(ctx, contentDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := content.NewStore(pool)
	local, err := store.Get(ctx, build.BuildId)
	prepareFinal, prepareErr := platform.GetJob(ctx, prepareJob.ID)
	if err != nil || prepareErr != nil || local.Chunks == nil || local.State == "READY" ||
		prepareFinal.Result == nil || prepareFinal.Result.Ref == nil ||
		prepareFinal.Result.Ref.Hash != local.Chunks.SHA256 {
		t.Fatalf("prepare process did not commit chunk manifest: %+v err=%v", local, err)
	}
	settingsRaw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(t.TempDir(), "bge-index.json")
	if err := os.WriteFile(settingsPath, settingsRaw, 0600); err != nil {
		t.Fatal(err)
	}
	indexInput := app.IndexJobInput{BuildID: build.BuildId, ReleaseID: build.ReleaseId,
		Generation: build.Generation, InputManifestHash: build.ManifestHash}
	indexRaw, err := json.Marshal(indexInput)
	if err != nil {
		t.Fatal(err)
	}
	indexJob, err := platform.SubmitJob(ctx, jobs.Submit{Producer: "ridethewind", OperationID: "bge-process-index-" + build.BuildId,
		RunRef: "bge-process-index", JobType: app.IndexJobType, ResourceProfile: "cpu", Input: indexRaw,
		MaxAttempts: 2, Deadline: time.Now().UTC().Add(17 * time.Minute).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatalf("submit actual DC index job: %v", err)
	}
	common["BTW_JOB_TYPE"] = app.IndexJobType
	common["BTW_INDEX_CONFIG_FILE"], common["BTW_INDEX_BACKEND"] = settingsPath, "exact"
	common["BTW_CONTENT_MIGRATE"] = "false"
	common["BTW_SESSION_TABLE_PREFIX"] = "real_bge_index_"
	common["BTW_METRICS_ADDR"] = bgeProcessMetricsAddr(t)
	blockCompletion.Store(true)
	index := runBGEWorkerProcess(t, binary, common)
	waitBGEProcess(t, index, 3*time.Minute, func() bool {
		fixed, err := store.Get(ctx, build.BuildId)
		if err != nil || fixed.State != "READY" || fixed.Result == nil || len(fixed.Lanes) != 3 {
			return false
		}
		remote, err := rtw.GetBuild(ctx, build.BuildId)
		return err == nil && remote.State == "READY" && blockedCompletions.Load() > 0
	})
	modelCallsBeforeRestart := representations.Load()
	index.stop(t)
	jobBeforeRestart, err := platform.GetJob(ctx, indexJob.ID)
	if err != nil || jobBeforeRestart.State != "running" {
		t.Fatalf("DC job was completed despite withheld ACK: %+v err=%v", jobBeforeRestart, err)
	}
	blockCompletion.Store(false)
	common["BTW_SESSION_INITIALIZE"] = "false"
	common["BTW_METRICS_ADDR"] = bgeProcessMetricsAddr(t)
	restarted := runBGEWorkerProcess(t, binary, common)
	waitBGEProcess(t, restarted, 2*time.Minute, func() bool {
		job, err := platform.GetJob(ctx, indexJob.ID)
		if err != nil || job.State != "succeeded" || job.Result == nil || job.Result.Ref == nil {
			return false
		}
		var delivered bool
		err = pool.QueryRow(ctx, "SELECT delivered_at IS NOT NULL FROM content_outbox WHERE build_id=$1", build.BuildId).Scan(&delivered)
		return err == nil && delivered
	})
	restarted.stop(t)
	if representations.Load() != modelCallsBeforeRestart || modelCallsBeforeRestart < 6 ||
		spans.Load() < 2 || nativePrepare.Load() == 0 || nativeIndex.Load() == 0 {
		t.Fatalf("restart re-encoded model or omitted native Graph trace: before=%d after=%d otlp=%d prepare=%d index=%d",
			modelCallsBeforeRestart, representations.Load(), spans.Load(), nativePrepare.Load(), nativeIndex.Load())
	}
	finalJob, err := platform.GetJob(ctx, indexJob.ID)
	remote, remoteErr := rtw.GetBuild(ctx, build.BuildId)
	local, localErr := store.Get(ctx, build.BuildId)
	if err != nil || remoteErr != nil || localErr != nil || finalJob.Result == nil || finalJob.Result.Ref == nil ||
		local.Result == nil || finalJob.Result.Ref.Hash != local.Result.SHA256 ||
		remote.IndexManifestHash != local.Result.SHA256 || remote.LeaseEpoch != 2 {
		t.Fatalf("final DC/RTW/BTW receipt mismatch: dc=%+v rtw=%+v btw=%+v errors=%v %v %v",
			finalJob, remote, local, err, remoteErr, localErr)
	}
	manifestRaw, err := objects.Get(ctx, *local.Result)
	if err != nil {
		t.Fatal(err)
	}
	var manifest corpus.IndexManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil || len(manifest.Lanes) != 3 {
		t.Fatalf("three immutable lane artifacts missing: %+v err=%v", manifest, err)
	}
	for _, lane := range manifest.Lanes {
		if !lane.ProbePassed || lane.Artifact.SHA256 == "" {
			t.Fatalf("real BGE lane missing probe or artifact: %+v", lane)
		}
		if _, err := objects.Get(ctx, lane.Artifact); err != nil {
			t.Fatalf("real BGE lane artifact does not match SHA256: %s: %v", lane.Profile.Lane, err)
		}
	}
	for _, log := range []string{prepare.output.String(), index.output.String(), restarted.output.String()} {
		if !strings.Contains(log, `"event":"content.worker.started"`) ||
			!strings.Contains(log, `"event":"content.worker.stopped"`) ||
			strings.Contains(log, fixture.WorkerToken) || strings.Contains(log, fixture.DCJobToken) ||
			strings.Contains(log, bge.AccessToken) {
			t.Fatal("worker lifecycle log incomplete or contains a credential")
		}
	}
	report, err := json.Marshal(map[string]any{"build_id": build.BuildId,
		"index_manifest_ref": local.Result.Key, "index_manifest_hash": local.Result.SHA256,
		"dc_ack_ref": finalJob.Result.Ref.URI, "dc_ack_hash": finalJob.Result.Ref.Hash,
		"dc_job_id": indexJob.ID, "dc_lease_epoch": finalJob.LeaseEpoch,
		"rtw_lease_epoch": remote.LeaseEpoch, "rtw_state": remote.State,
		"rtw_generation": remote.Generation, "prepare_job_id": prepareJob.ID,
		"model_calls_before_restart": modelCallsBeforeRestart, "model_calls_after_restart": representations.Load(),
		"blocked_completions": blockedCompletions.Load(), "native_otlp_exports": spans.Load(),
		"native_prepare_spans": nativePrepare.Load(), "native_index_spans": nativeIndex.Load()})
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
			"real-bge-prepare-worker.jsonl": []byte(prepare.output.String()),
			"real-bge-index-worker.jsonl":   []byte(index.output.String()),
			"real-bge-index-restart.jsonl":  []byte(restarted.output.String()),
			"real-bge-process-report.json":  report,
		} {
			if err := os.WriteFile(filepath.Join(evidence, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("actual race worker processes built locked BGE index and recovered DC ACK: ref=%s model_calls=%d",
		local.Result.SHA256, modelCallsBeforeRestart)
}
